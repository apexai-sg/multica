package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SignupError represents signup restriction errors
type SignupError struct {
	Message string
}

func (e SignupError) Error() string {
	return e.Message
}

var ErrSignupProhibited = SignupError{Message: "user registration is disabled on this self-hosted instance"}
var ErrEmailNotAllowed = SignupError{Message: "email address or domain not allowed on this instance"}

const (
	googleLoginCodeAccountDisabled     = "account_disabled"
	googleLoginCodeSignupProhibited    = "signup_prohibited"
	googleLoginCodeEmailNotAllowed     = "email_not_allowed"
	googleLoginCodeAccountWithoutEmail = "google_account_no_email"
	googleLoginCodeInvalidOAuthCode    = "oauth_code_invalid"
)

const devVerificationCodeEnv = "MULTICA_DEV_VERIFICATION_CODE"

// supportedLanguages mirrors `SUPPORTED_LOCALES` in packages/core/i18n/types.ts.
// Keep both lists in sync when adding a locale — the user-controlled `language`
// field round-trips through GetMe back into i18n.changeLanguage(), so without
// validation an arbitrary string would persist and echo to every device.
var supportedLanguages = map[string]struct{}{
	"en":      {},
	"zh-Hans": {},
	"ko":      {},
	"ja":      {},
	"fr":      {},
}

type UserResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	AvatarURL *string `json:"avatar_url"`
	Language  *string `json:"language"`
	// Pinned IANA tz; nil = no preference (use browser-detected tz).
	Timezone                *string         `json:"timezone"`
	OnboardedAt             *string         `json:"onboarded_at"`
	OnboardingQuestionnaire json.RawMessage `json:"onboarding_questionnaire"`
	ProfileDescription      string          `json:"profile_description"`
	CreatedAt               string          `json:"created_at"`
	UpdatedAt               string          `json:"updated_at"`
}

// MaxProfileDescriptionLen caps the user-supplied profile_description body.
// Picked at 2000 chars per MUL-2406: enough room for role / stack / a few
// preferences, short enough that injecting it into every agent brief
// doesn't move the needle on prompt cost.
const MaxProfileDescriptionLen = 2000

func (h *Handler) userToResponse(u db.User) UserResponse {
	// JSONB column is []byte with DEFAULT '{}', so it's never nil at the DB
	// level. Defensive coalesce just in case a future ALTER makes the column
	// nullable and some row comes back with no default applied.
	q := u.OnboardingQuestionnaire
	if len(q) == 0 {
		q = []byte("{}")
	}
	return UserResponse{
		ID:                      uuidToString(u.ID),
		Name:                    u.Name,
		Email:                   u.Email,
		AvatarURL:               h.resolveAvatarURLPtr(textToPtr(u.AvatarUrl)),
		Language:                textToPtr(u.Language),
		Timezone:                textToPtr(u.Timezone),
		OnboardedAt:             timestampToPtr(u.OnboardedAt),
		OnboardingQuestionnaire: json.RawMessage(q),
		ProfileDescription:      u.ProfileDescription,
		CreatedAt:               timestampToString(u.CreatedAt),
		UpdatedAt:               timestampToString(u.UpdatedAt),
	}
}

type LoginResponse struct {
	Token string       `json:"token"`
	User  UserResponse `json:"user"`
}

type SendCodeRequest struct {
	Email string `json:"email"`
}

type VerifyCodeRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func generateCode() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(buf[:]) % 1000000
	return fmt.Sprintf("%06d", n), nil
}

func isDevVerificationCode(code string) bool {
	if isProductionEnv() {
		return false
	}

	devCode := strings.TrimSpace(os.Getenv(devVerificationCodeEnv))
	if !isSixDigitCode(devCode) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(code), []byte(devCode)) == 1
}

func isProductionEnv() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production")
}

func isSixDigitCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func (h *Handler) issueJWT(user db.User) (string, error) {
	if auth.IsTemporarilyDisabledUser(uuidToString(user.ID), user.Email) {
		return "", auth.ErrTemporarilyDisabledUser
	}
	// `sid` identifies this login for as long as it lasts: sliding renewal
	// copies it forward, so it stays put while `exp` moves. The CSRF token is
	// bound to it rather than to the token string, which is what lets the
	// auth cookie be re-issued mid-session without invalidating CSRF tokens
	// other tabs are already holding (MUL-7436).
	sid, err := auth.NewSessionID()
	if err != nil {
		return "", err
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   uuidToString(user.ID),
		"email": user.Email,
		"name":  user.Name,
		"sid":   sid,
		"exp":   time.Now().Add(auth.AuthTokenTTL()).Unix(),
		"iat":   time.Now().Unix(),
	})
	return token.SignedString(auth.JWTSecret())
}

// nameFromEmail derives the placeholder display name for a new account: the
// local part of the address, or the whole address when it carries no "@".
func nameFromEmail(email string) string {
	if at := strings.Index(email, "@"); at > 0 {
		return email[:at]
	}
	return email
}

// createUserWithAuroraSignupBonusEligibility makes the signup edge durable.
// Production handlers always have TxStarter; the fallback keeps lightweight
// handler tests working while preserving the same write order.
func (h *Handler) createUserWithAuroraSignupBonusEligibility(ctx context.Context, params db.CreateUserParams) (db.User, error) {
	if h.TxStarter == nil {
		user, err := h.Queries.CreateUser(ctx, params)
		if err != nil {
			return db.User{}, err
		}
		if err := h.Queries.CreateAuroraSignupBonusEligibility(ctx, user.ID); err != nil {
			return db.User{}, err
		}
		return user, nil
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.User{}, err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	user, err := qtx.CreateUser(ctx, params)
	if err != nil {
		return db.User{}, err
	}
	if err := qtx.CreateAuroraSignupBonusEligibility(ctx, user.ID); err != nil {
		return db.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.User{}, err
	}
	return user, nil
}

// grantPendingAuroraSignupBonus retries only signups explicitly recorded after
// this feature shipped. Accounts without an eligibility row predate the bonus
// and must never be backfilled.
func (h *Handler) grantPendingAuroraSignupBonus(ctx context.Context, user db.User) {
	if h.Credit == nil {
		return
	}

	status, err := h.Queries.GetAuroraSignupBonusStatus(ctx, user.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("failed to load aurora signup bonus eligibility", "error", err, "user_id", uuidToString(user.ID))
		return
	}
	if status != "pending" {
		return
	}

	wsID, err := h.Queries.GetPersonalWorkspaceForUser(ctx, user.ID)
	if err != nil {
		slog.Warn("failed to resolve personal workspace for aurora signup bonus", "error", err, "user_id", uuidToString(user.ID))
		return
	}
	if err := h.Credit.Grant(ctx, user.ID, wsID, auroraSignupBonusMicro, aurora.LedgerKindAdjustment, "signup:"+uuidToString(user.ID)); err != nil {
		slog.Warn("failed to grant aurora signup bonus", "error", err, "user_id", uuidToString(user.ID))
		return
	}
	if err := h.Queries.MarkAuroraSignupBonusGranted(ctx, user.ID); err != nil {
		slog.Warn("failed to mark aurora signup bonus granted", "error", err, "user_id", uuidToString(user.ID))
	}
}

// findOrCreateUser returns the existing user for an email, or creates one if
// none exists. displayName is the name the caller already knows for the
// account — GoogleLogin passes the Google profile name, VerifyCode has none —
// and falls back to the email prefix when empty. It only applies to a user
// created by this call: an existing account keeps its stored name, which only
// GoogleLogin's profile sync ever upgrades.
//
// It is applied before the personal-workspace provisioning below, so the space
// is named after the display name rather than the email-prefix placeholder
// (#13).
//
// isNew reports whether this call created the user — the signup event fires on
// that edge, covering both the verification-code and Google OAuth entry points.
func (h *Handler) findOrCreateUser(ctx context.Context, email, displayName string) (user db.User, isNew bool, err error) {
	if auth.IsTemporarilyDisabledUserEmail(email) {
		return db.User{}, false, auth.ErrTemporarilyDisabledUser
	}

	user, err = h.Queries.GetUserByEmail(ctx, email)
	isNew = isNotFound(err)
	if err != nil && !isNew {
		return db.User{}, false, err
	}
	if !isNew && auth.IsTemporarilyDisabledUser(uuidToString(user.ID), user.Email) {
		return db.User{}, false, auth.ErrTemporarilyDisabledUser
	}

	if err := h.checkSignupAllowed(ctx, email, isNew); err != nil {
		return db.User{}, false, err
	}

	if isNew {
		name := strings.TrimSpace(displayName)
		if name == "" {
			name = nameFromEmail(email)
		}
		created, err := h.createUserWithAuroraSignupBonusEligibility(ctx, db.CreateUserParams{
			Name:  name,
			Email: email,
		})
		if err != nil {
			return db.User{}, false, err
		}
		user = created
	}

	// Best-effort personal-workspace provisioning. Runs on every login — not
	// just signup — so a transient failure self-heals on the next login, and
	// both VerifyCode and Google OAuth signups are covered because they are
	// the only two callers of findOrCreateUser. Guarded on TxStarter so the
	// handler unit tests that stub Queries with a mock (which cannot back a
	// transaction) skip the side effect.
	if h.TxStarter != nil {
		if err := h.ensurePersonalWorkspace(ctx, user.ID, user.Name); err != nil {
			slog.Warn("failed to provision personal workspace", "error", err, "user_id", uuidToString(user.ID))
		}
	} else if isNew {
		// A handler built outside New() (unit tests stub Queries) cannot back a
		// transaction; log so a real wiring mistake is never silent.
		slog.Warn("personal-workspace provisioning skipped: handler has no transaction starter", "user_id", uuidToString(user.ID))
	}

	// The eligibility row is created atomically with a new account. Leaving it
	// pending until the idempotent ledger write succeeds makes transient workspace
	// or credit failures retry on the next login without backfilling older users.
	h.grantPendingAuroraSignupBonus(ctx, user)

	return user, isNew, nil
}

// auroraSignupBonusMicro is the one-time Aurora credit grant every new account
// receives. Signup bonuses never expire, which is why the settlement's expiry
// phase sums only "sub:" references.
const auroraSignupBonusMicro = 500 * microCreditsPerCredit

// signupSourceFromRequest reads the attribution cookie the web frontend
// sets on the first pageview (UTM + referrer bundle). The frontend writes
// a JSON string URL-encoded into the cookie value — Go does not
// auto-decode Cookie.Value, so we have to unescape here before the string
// lands in PostHog. Missing cookie / decode failures collapse to the
// empty string; that simply omits signup_source from the event rather
// than sending percent-encoded garbage. Never fall back to r.Referer() —
// the frontend has already sanitised attribution and a raw referer can
// leak OAuth code/state from the callback URL.
//
// The cap is the server-side defence against a client that manages to set
// an oversize cookie; it matches SIGNUP_SOURCE_MAX_LEN on the frontend.
const signupSourceMaxLen = 512

func signupSourceFromRequest(r *http.Request) string {
	c, err := r.Cookie("multica_signup_source")
	if err != nil || c == nil {
		return ""
	}
	decoded, err := url.QueryUnescape(c.Value)
	if err != nil {
		return ""
	}
	if len(decoded) > signupSourceMaxLen {
		return ""
	}
	return decoded
}

func (h *Handler) checkSignupAllowed(ctx context.Context, email string, isNewUser bool) error {
	if !isNewUser {
		return nil // existing users always allowed to log in
	}

	email = strings.ToLower(email)
	domain := ""
	if at := strings.Index(email, "@"); at > 0 {
		domain = email[at+1:]
	}

	// 1. explicit email allowlist always wins
	if len(h.cfg.AllowedEmails) > 0 && contains(h.cfg.AllowedEmails, email) {
		return nil
	}

	// 2. domain allowlist always wins
	if len(h.cfg.AllowedEmailDomains) > 0 && contains(h.cfg.AllowedEmailDomains, domain) {
		return nil
	}

	// 3. unrestricted signup needs no invitation lookup.
	if h.cfg.AllowSignup && len(h.cfg.AllowedEmailDomains) == 0 && len(h.cfg.AllowedEmails) == 0 {
		return nil
	}

	// 4. A live invitation is an implicit per-email signup allowance. This
	// also permits invited emails outside configured allowlists, regardless
	// of ALLOW_SIGNUP. Recheck at account creation in findOrCreateUser.
	invited, err := h.Queries.HasPendingInvitationForEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("check pending invitation for signup: %w", err)
	}
	if invited {
		return nil
	}

	if !h.cfg.AllowSignup {
		return ErrSignupProhibited
	}
	return ErrEmailNotAllowed
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

func (h *Handler) SendCode(w http.ResponseWriter, r *http.Request) {
	var req SendCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if auth.IsTemporarilyDisabledUserEmail(email) {
		writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
		return
	}

	// Check signup restrictions before sending magic link
	existingUser, err := h.Queries.GetUserByEmail(r.Context(), email)
	if err != nil {
		if !isNotFound(err) {
			// Real database/query error → return 500
			writeError(w, http.StatusInternalServerError, "failed to lookup user")
			return
		}
		// User does not exist → treat as new user
		isNewUser := true
		if err := h.checkSignupAllowed(r.Context(), email, isNewUser); err != nil {
			var signupErr SignupError
			if errors.As(err, &signupErr) {
				writeError(w, http.StatusForbidden, signupErr.Error())
			} else {
				slog.Warn("signup eligibility check failed", append(logger.RequestAttrs(r), "error", err, "email", email)...)
				writeError(w, http.StatusInternalServerError, "failed to check signup eligibility")
			}
			return
		}
	} else {
		// User already exists → always allowed to login
		if auth.IsTemporarilyDisabledUser(uuidToString(existingUser.ID), existingUser.Email) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		isNewUser := false
		if err := h.checkSignupAllowed(r.Context(), email, isNewUser); err != nil {
			// This should rarely happen, but handle it anyway
			var signupErr SignupError
			if errors.As(err, &signupErr) {
				writeError(w, http.StatusForbidden, signupErr.Error())
			} else {
				slog.Warn("signup eligibility check failed", append(logger.RequestAttrs(r), "error", err, "email", email)...)
				writeError(w, http.StatusInternalServerError, "failed to check signup eligibility")
			}
			return
		}
	}

	// Rate limit: max 1 code per 60 seconds per email
	latest, err := h.Queries.GetLatestCodeByEmail(r.Context(), email)
	if err == nil && time.Since(latest.CreatedAt.Time) < 60*time.Second {
		writeError(w, http.StatusTooManyRequests, "please wait before requesting another code")
		return
	}

	code, err := generateCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate code")
		return
	}

	_, err = h.Queries.CreateVerificationCode(r.Context(), db.CreateVerificationCodeParams{
		Email:     email,
		Code:      code,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(10 * time.Minute), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store verification code")
		return
	}

	if err := h.EmailService.SendVerificationCode(email, code); err != nil {
		slog.Error("failed to send verification code", "email", email, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send verification code")
		return
	}

	// Best-effort cleanup of expired codes
	_ = h.Queries.DeleteExpiredVerificationCodes(r.Context())

	writeJSON(w, http.StatusOK, map[string]string{"message": "Verification code sent"})
}

func (h *Handler) VerifyCode(w http.ResponseWriter, r *http.Request) {
	var req VerifyCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	code := strings.TrimSpace(req.Code)

	if email == "" || code == "" {
		writeError(w, http.StatusBadRequest, "email and code are required")
		return
	}
	if auth.IsTemporarilyDisabledUserEmail(email) {
		writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
		return
	}

	dbCode, err := h.Queries.GetLatestVerificationCode(r.Context(), email)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	isDevCode := isDevVerificationCode(code)
	if !isDevCode && subtle.ConstantTimeCompare([]byte(code), []byte(dbCode.Code)) != 1 {
		_ = h.Queries.IncrementVerificationCodeAttempts(r.Context(), dbCode.ID)
		writeError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	if err := h.Queries.MarkVerificationCodeUsed(r.Context(), dbCode.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify code")
		return
	}

	user, isNew, err := h.findOrCreateUser(r.Context(), email, "")
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		var signupErr SignupError
		if errors.As(err, &signupErr) {
			writeError(w, http.StatusForbidden, signupErr.Error())
			return
		}
		slog.Warn("login user lookup or creation failed", append(logger.RequestAttrs(r), "error", err, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	if isNew {
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.Signup(uuidToString(user.ID), user.Email, signupSourceFromRequest(r)))
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		slog.Warn("login failed", append(logger.RequestAttrs(r), "error", err, "email", req.Email)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	// Set HttpOnly auth cookie (browser clients) + CSRF cookie.
	if err := auth.SetAuthCookies(w, tokenString); err != nil {
		slog.Warn("failed to set auth cookies", "error", err)
	}

	// Set CloudFront signed cookies for CDN access.
	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(auth.AuthTokenTTL())) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "email", user.Email)...)
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: tokenString,
		User:  h.userToResponse(user),
	})
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if errors.Is(err, pgx.ErrNoRows) {
		// The credential no longer identifies an existing user. Return the
		// same terminal status as an expired token so clients can sign in again.
		writeError(w, http.StatusUnauthorized, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}

	writeJSON(w, http.StatusOK, h.userToResponse(user))
}

type UpdateMeRequest struct {
	Name               *string `json:"name"`
	AvatarURL          *string `json:"avatar_url"`
	Language           *string `json:"language"`
	ProfileDescription *string `json:"profile_description"`
	// IANA tz to pin; "" clears back to NULL; nil leaves untouched.
	Timezone *string `json:"timezone"`
}

type GoogleLoginRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

type googleTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
}

type googleUserInfo struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func writeGoogleLoginActionableError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, auth.ErrTemporarilyDisabledUser):
		writeErrorCode(w, http.StatusForbidden, googleLoginCodeAccountDisabled, auth.TemporarilyDisabledUserError)
	case errors.Is(err, ErrSignupProhibited):
		writeErrorCode(w, http.StatusForbidden, googleLoginCodeSignupProhibited, ErrSignupProhibited.Error())
	case errors.Is(err, ErrEmailNotAllowed):
		writeErrorCode(w, http.StatusForbidden, googleLoginCodeEmailNotAllowed, ErrEmailNotAllowed.Error())
	default:
		var signupErr SignupError
		if !errors.As(err, &signupErr) {
			return false
		}
		// Preserve the actionable fallback for signup restrictions without a code.
		writeError(w, http.StatusForbidden, signupErr.Error())
	}
	return true
}

func (h *Handler) googleHTTPClient() *http.Client {
	if h.googleOAuthHTTPClient != nil {
		return h.googleOAuthHTTPClient
	}
	return http.DefaultClient
}

func (h *Handler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	var req GoogleLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		writeFeatureDisabled(w, "google_login_not_configured", "Google login is not configured")
		return
	}

	redirectURI := req.RedirectURI
	if redirectURI == "" {
		redirectURI = os.Getenv("GOOGLE_REDIRECT_URI")
	}

	// Exchange authorization code for tokens.
	tokenResp, err := h.googleHTTPClient().PostForm("https://oauth2.googleapis.com/token", url.Values{
		"code":          {req.Code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		slog.Error("google oauth token exchange failed", "error", err)
		writeError(w, http.StatusBadGateway, "failed to exchange code with Google")
		return
	}
	defer tokenResp.Body.Close()

	tokenBody, err := io.ReadAll(tokenResp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to read Google token response")
		return
	}

	if tokenResp.StatusCode != http.StatusOK {
		slog.Error("google oauth token exchange returned error", "status", tokenResp.StatusCode, "body", string(tokenBody))
		var tokenErr struct {
			Error string `json:"error"`
		}
		// Only a valid invalid_grant response identifies a rejected authorization
		// code. Configuration, provider and malformed responses are server failures.
		if err := json.Unmarshal(tokenBody, &tokenErr); err == nil &&
			tokenResp.StatusCode == http.StatusBadRequest && tokenErr.Error == "invalid_grant" {
			writeErrorCode(w, http.StatusBadRequest, googleLoginCodeInvalidOAuthCode, "failed to exchange code with Google")
			return
		}
		writeError(w, http.StatusBadGateway, "failed to exchange code with Google")
		return
	}

	var gToken googleTokenResponse
	if err := json.Unmarshal(tokenBody, &gToken); err != nil {
		writeError(w, http.StatusBadGateway, "failed to parse Google token response")
		return
	}
	if strings.TrimSpace(gToken.AccessToken) == "" {
		slog.Error("google oauth token response has no access token")
		writeError(w, http.StatusBadGateway, "invalid Google token response")
		return
	}

	// Fetch user info from Google.
	userInfoReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		slog.Error("failed to create userinfo request", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	userInfoReq.Header.Set("Authorization", "Bearer "+gToken.AccessToken)

	userInfoResp, err := h.googleHTTPClient().Do(userInfoReq)
	if err != nil {
		slog.Error("google userinfo fetch failed", "error", err)
		writeError(w, http.StatusBadGateway, "failed to fetch user info from Google")
		return
	}
	defer userInfoResp.Body.Close()

	if userInfoResp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(userInfoResp.Body, 4096))
		if readErr != nil {
			slog.Error("google userinfo error response could not be read", "status", userInfoResp.StatusCode, "error", readErr)
		} else {
			slog.Error("google userinfo returned error", "status", userInfoResp.StatusCode, "body", string(body))
		}
		writeError(w, http.StatusBadGateway, "failed to fetch user info from Google")
		return
	}

	var gUser *googleUserInfo
	if err := json.NewDecoder(userInfoResp.Body).Decode(&gUser); err != nil {
		writeError(w, http.StatusBadGateway, "failed to parse Google user info")
		return
	}
	if gUser == nil {
		writeError(w, http.StatusBadGateway, "invalid Google user info")
		return
	}

	email := strings.ToLower(strings.TrimSpace(gUser.Email))
	if email == "" {
		writeErrorCode(w, http.StatusBadRequest, googleLoginCodeAccountWithoutEmail, "Google did not provide an email address for this sign-in")
		return
	}

	if auth.IsTemporarilyDisabledUserEmail(email) {
		writeErrorCode(w, http.StatusForbidden, googleLoginCodeAccountDisabled, auth.TemporarilyDisabledUserError)
		return
	}

	// Hand the profile name to provisioning up front: findOrCreateUser creates
	// the account and its personal workspace in one step, so a name applied only
	// afterwards would leave the space named after the email prefix (#13).
	googleName := strings.TrimSpace(gUser.Name)
	user, isNew, err := h.findOrCreateUser(r.Context(), email, googleName)
	if err != nil {
		if writeGoogleLoginActionableError(w, err) {
			return
		}
		slog.Warn("login user lookup or creation failed", append(logger.RequestAttrs(r), "error", err, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	if isNew {
		evt := analytics.Signup(uuidToString(user.ID), user.Email, signupSourceFromRequest(r))
		evt.Properties["auth_method"] = "google"
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, evt)
	}

	// Upgrade name and avatar from the Google profile: an account that signed up
	// with an email code still carries the email-prefix placeholder, and any
	// account may be missing an avatar. A user created by this request already
	// carries the profile name, so only the avatar branch can match for them.
	needsUpdate := false
	previousName := user.Name
	newName := user.Name
	newAvatar := user.AvatarUrl

	if googleName != "" && user.Name == nameFromEmail(email) {
		newName = googleName
		needsUpdate = true
	}
	if gUser.Picture != "" && !user.AvatarUrl.Valid {
		newAvatar = pgtype.Text{String: gUser.Picture, Valid: true}
		needsUpdate = true
	}

	if needsUpdate {
		updated, err := h.Queries.UpdateUser(r.Context(), db.UpdateUserParams{
			ID:        user.ID,
			Name:      newName,
			AvatarUrl: newAvatar,
		})
		if err == nil {
			user = updated
		}
	}

	// The personal workspace is named after the display name it was provisioned
	// with, so an upgrade has to carry the space along — an account renamed here
	// would otherwise keep "<email prefix> 的个人空间" for good. Best-effort, like
	// provisioning itself: a failure just leaves the space under its old name.
	// Gated on the write above landing, so a failed user update never renames a
	// space out from under an unchanged row.
	if user.Name != previousName {
		if err := h.syncPersonalWorkspaceName(r.Context(), user.ID, previousName, user.Name); err != nil {
			slog.Warn("failed to rename personal workspace", "error", err, "user_id", uuidToString(user.ID))
		}
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		if writeGoogleLoginActionableError(w, err) {
			return
		}
		slog.Warn("google login failed", append(logger.RequestAttrs(r), "error", err, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	if err := auth.SetAuthCookies(w, tokenString); err != nil {
		slog.Warn("failed to set auth cookies", "error", err)
	}

	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(72 * time.Hour)) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in via google", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "email", user.Email)...)
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: tokenString,
		User:  h.userToResponse(user),
	})
}

// IssueCliToken returns a fresh JWT for the authenticated user.
// This allows cookie-authenticated browser sessions to obtain a bearer token
// that can be handed off to the CLI via the cli_callback redirect.
func (h *Handler) IssueCliToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		slog.Warn("cli-token: failed to issue JWT", append(logger.RequestAttrs(r), "error", err, "user_id", userID)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": tokenString})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req UpdateMeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	currentUser, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	name := currentUser.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
	}

	params := db.UpdateUserParams{
		ID:   currentUser.ID,
		Name: name,
	}
	if req.AvatarURL != nil {
		avatarURL, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, currentUser.AvatarUrl.String)
		if !ok {
			return
		}
		params.AvatarUrl = pgtype.Text{String: avatarURL, Valid: true}
	}
	if req.Language != nil {
		lang := strings.TrimSpace(*req.Language)
		if _, ok := supportedLanguages[lang]; !ok {
			writeError(w, http.StatusBadRequest, "unsupported language")
			return
		}
		params.Language = pgtype.Text{String: lang, Valid: true}
	}
	if req.ProfileDescription != nil {
		// Count runes, not bytes: 2000 chars of Chinese must not be rejected
		// as ~6000 bytes. utf8.RuneCountInString handles invalid UTF-8 by
		// counting each bad byte as one rune, which still bounds the column.
		desc := strings.TrimSpace(*req.ProfileDescription)
		if utf8.RuneCountInString(desc) > MaxProfileDescriptionLen {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("profile_description exceeds %d characters", MaxProfileDescriptionLen))
			return
		}
		params.ProfileDescription = pgtype.Text{String: desc, Valid: true}
	}

	if req.Timezone != nil {
		// Valid=false → column untouched; Valid=true + "" → clear to
		// NULL; Valid=true + IANA → set. Three-way semantics enforced
		// in the UpdateUser SQL CASE.
		tz := strings.TrimSpace(*req.Timezone)
		if tz != "" {
			if loc, err := time.LoadLocation(tz); err != nil || loc == nil {
				writeError(w, http.StatusBadRequest, "invalid timezone")
				return
			}
		}
		params.Timezone = pgtype.Text{String: tz, Valid: true}
	}

	updatedUser, err := h.Queries.UpdateUser(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update user")
		return
	}

	writeJSON(w, http.StatusOK, h.userToResponse(updatedUser))
}

// personalWorkspaceSlug is the deterministic slug of a user's auto-provisioned
// personal workspace. The full UUID (32 hex) is in the slug, not a truncated
// prefix: a 48-bit prefix could collide across users, and a collision would hit
// the workspace.slug unique constraint on every subsequent login, silently
// leaving the user without a personal workspace forever.
func personalWorkspaceSlug(userID pgtype.UUID) string {
	return "user-" + strings.ReplaceAll(uuidToString(userID), "-", "")
}

// personalWorkspaceName derives a personal workspace's name from its owner's
// display name.
func personalWorkspaceName(ownerName string) string {
	if name := strings.TrimSpace(ownerName); name != "" {
		return name + " 的个人空间"
	}
	return "Personal Workspace"
}

// ensurePersonalWorkspace gives a newly-created user a single-member owner
// workspace, so Aurora signups land with a place to create generations without
// going through the manual workspace-creation flow. Idempotent: if the user
// already belongs to any workspace it is a no-op. Runs in one transaction so
// the workspace, owner membership and issue-status seed commit or roll back
// together.
func (h *Handler) ensurePersonalWorkspace(ctx context.Context, userID pgtype.UUID, userName string) error {
	existing, err := h.Queries.CountWorkspacesForUser(ctx, userID)
	if err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	name := personalWorkspaceName(userName)
	slug := personalWorkspaceSlug(userID)

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	ws, err := qtx.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		Name:        name,
		Slug:        slug,
		Description: ptrToText(nil),
		Context:     ptrToText(nil),
		IssuePrefix: defaultIssuePrefixFromSlug(slug),
	})
	if err != nil {
		return err
	}
	if _, err := qtx.CreateMember(ctx, db.CreateMemberParams{
		WorkspaceID: ws.ID,
		UserID:      userID,
		Role:        "owner",
	}); err != nil {
		return err
	}
	if err := issuestatus.Ensure(ctx, qtx, ws.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// syncPersonalWorkspaceName carries a display-name upgrade through to the
// personal workspace that was named after the old name (#13). Only a space
// still carrying the previous derived name is renamed — one the user renamed
// themselves is left alone — and a user who owns no personal workspace is a
// no-op.
func (h *Handler) syncPersonalWorkspaceName(ctx context.Context, userID pgtype.UUID, previousName, newName string) error {
	workspace, err := h.Queries.GetWorkspaceBySlug(ctx, personalWorkspaceSlug(userID))
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if workspace.Name != personalWorkspaceName(previousName) {
		return nil
	}

	_, err = h.Queries.UpdateWorkspace(ctx, db.UpdateWorkspaceParams{
		ID:   workspace.ID,
		Name: pgtype.Text{String: personalWorkspaceName(newName), Valid: true},
	})
	return err
}
