package aurora

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AntomConfig is the merchant side of the Antom (Ant International "Ams")
// payment channel. Empty optional fields take the production defaults; a
// configuration that cannot sign or verify is rejected by NewAntomClient, so a
// half-configured channel fails at startup rather than at the first payment.
type AntomConfig struct {
	// ClientID identifies the merchant at Antom and is part of every signature.
	ClientID string
	// MerchantPrivateKey signs outgoing API calls.
	MerchantPrivateKey string
	// AntomPublicKey verifies incoming notifications.
	AntomPublicKey string
	// KeyVersion is the signing key version Antom expects back in the header.
	KeyVersion string
	// APIBase is the Antom gateway origin; empty selects the Singapore gateway.
	APIBase string
	// Currency is the settlement currency; empty selects SGD.
	Currency string
}

const (
	antomDefaultAPIBase    = "https://open-sea-global.alipay.com"
	antomDefaultCurrency   = "SGD"
	antomDefaultKeyVersion = "1"
	antomMinKeyBits        = 2048
)

// Antom subscription endpoints, appended to the configured API base.
const (
	antomPathCreateSubscription  = "/ams/api/v1/subscriptions/create"
	antomPathCancelSubscription  = "/ams/api/v1/subscriptions/cancel"
	antomPathInquirySubscription = "/ams/api/v1/subscriptions/inquireSubscription"
)

// AntomProvider is the subscription surface the Aurora handler programs
// against. AntomClient implements it; tests substitute a fake. The
// notification method is part of the provider contract, so the client only
// satisfies the interface once that path exists as well.
type AntomProvider interface {
	// CreateSubscription returns the Antom-hosted authorization URL. Antom
	// returns no subscription id here, so callers match the later notification
	// by RequestID.
	CreateSubscription(ctx context.Context, req AntomSubscriptionRequest) (string, error)
	// CancelSubscription stops future renewals (CANCEL) and leaves the current
	// period active; it never terminates the subscription immediately.
	CancelSubscription(ctx context.Context, subscriptionID string) error
	// InquirySubscription reads the live subscription state for reconciliation.
	InquirySubscription(ctx context.Context, subscriptionID string) (AntomSubscriptionState, error)
	// VerifyNotification authenticates and parses either notification type.
	VerifyNotification(method, path string, headers http.Header, body []byte) (AntomNotification, error)
}

// AntomSubscriptionRequest is the server-side intent for one subscription.
// Amount is already in the two-decimal form Antom receives ("19.00"); the tier
// catalog owns the price and a request body never supplies it.
type AntomSubscriptionRequest struct {
	RequestID             string
	Tier                  string
	Amount                string
	Currency              string
	RedirectURL           string
	PaymentNotifyURL      string
	SubscriptionNotifyURL string
	StartTime             time.Time
}

// AntomSubscriptionState is an inquiry response reduced to the fields the
// reconciliation path uses.
type AntomSubscriptionState struct {
	SubscriptionID   string
	Status           string
	CurrentPeriodEnd time.Time
}

// AntomNotification is the verified, parsed form of one Antom notification.
// The notification path fills it; the shape is declared here because
// AntomProvider names it.
type AntomNotification struct {
	Type             string
	RequestID        string
	SubscriptionID   string
	Status           string
	NotificationType string
	PaymentID        string
	PhaseNo          string
	PeriodStart      time.Time
	PeriodEnd        time.Time
}

// antomAPIBaseAllowed pins the official Antom gateways. Every request carries
// the merchant private key's signature, so any other origin would receive
// signed payment requests. The comparison covers the whole origin, which also
// rejects lookalikes such as
// "https://open-sea-global.alipay.com.evil.example.com".
func antomAPIBaseAllowed(base string) bool {
	switch base {
	case "https://open-sea-global.alipay.com",
		"https://open-na-global.alipay.com",
		"https://open-de-global.alipay.com",
		"https://open.antglobal-us.com",
		"https://open-sea.alipay.com",
		"https://open-na.alipay.com":
		return true
	}
	return false
}

// AntomClient signs Antom API requests with the merchant key and keeps the
// Antom key for notification verification. The fields stay unexported so a
// caller cannot swap a key or re-point the gateway after construction.
type AntomClient struct {
	cfg        AntomConfig
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	http       *http.Client
}

// NewAntomClient builds the client or explains which part of the configuration
// is unusable. Callers treat the error as "the Antom channel is unavailable"
// rather than falling back to a partially configured client.
func NewAntomClient(cfg AntomConfig) (*AntomClient, error) {
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("antom config: ClientID is required")
	}
	if cfg.MerchantPrivateKey == "" {
		return nil, fmt.Errorf("antom config: MerchantPrivateKey is required")
	}
	if cfg.AntomPublicKey == "" {
		return nil, fmt.Errorf("antom config: AntomPublicKey is required")
	}

	if cfg.APIBase == "" {
		cfg.APIBase = antomDefaultAPIBase
	}
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	if !antomAPIBaseAllowed(cfg.APIBase) {
		return nil, fmt.Errorf("antom config: APIBase %q is not an official Antom gateway", cfg.APIBase)
	}
	if cfg.Currency == "" {
		cfg.Currency = antomDefaultCurrency
	}
	if cfg.KeyVersion == "" {
		cfg.KeyVersion = antomDefaultKeyVersion
	}

	privateKey, err := parseAntomPrivateKey(cfg.MerchantPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("antom config: MerchantPrivateKey: %w", err)
	}
	publicKey, err := parseAntomPublicKey(cfg.AntomPublicKey)
	if err != nil {
		return nil, fmt.Errorf("antom config: AntomPublicKey: %w", err)
	}

	return &AntomClient{
		cfg:        cfg,
		privateKey: privateKey,
		publicKey:  publicKey,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Antom answers in place. Following a redirect would replay the
			// signed body and signature headers to an origin the signature
			// never authorized, so the redirect is returned as the response.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// antomKeyDER accepts the two shapes merchant consoles export: a PEM block or
// the bare base64 DER that block would wrap.
func antomKeyDER(value string) ([]byte, error) {
	if block, _ := pem.Decode([]byte(value)); block != nil {
		return block.Bytes, nil
	}
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("key is neither PEM nor base64 DER")
	}
	return der, nil
}

func parseAntomPrivateKey(value string) (*rsa.PrivateKey, error) {
	der, err := antomKeyDER(value)
	if err != nil {
		return nil, err
	}
	// Antom's console exports PKCS#1, but PKCS#8 is the modern container and
	// keytool/openssl users produce it, so both are accepted.
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		if err := antomCheckKeySize(key.N.BitLen()); err != nil {
			return nil, err
		}
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want RSA", parsed)
	}
	if err := antomCheckKeySize(key.N.BitLen()); err != nil {
		return nil, err
	}
	return key, nil
}

func parseAntomPublicKey(value string) (*rsa.PublicKey, error) {
	der, err := antomKeyDER(value)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want RSA", parsed)
	}
	if err := antomCheckKeySize(key.N.BitLen()); err != nil {
		return nil, err
	}
	return key, nil
}

// antomCheckKeySize rejects a key too weak to carry a payment signature. Antom
// signs with 2048-bit RSA, so a shorter key is a configuration error rather
// than something to discover on the first signed request.
func antomCheckKeySize(bits int) error {
	if bits >= antomMinKeyBits {
		return nil
	}
	return fmt.Errorf("RSA key is %d bits, need at least %d", bits, antomMinKeyBits)
}

// antomSignContent builds the exact bytes Antom signs for a request or a
// notification: "<method> <path>\n<client-id>.<request-time>.<body>". The
// space, the newline and the dots are separators in the protocol, so the body
// must be the exact bytes that travel on the wire.
func antomSignContent(method, path, clientID, requestTime string, body []byte) []byte {
	head := method + " " + path + "\n" + clientID + "." + requestTime + "."
	content := make([]byte, 0, len(head)+len(body))
	content = append(content, head...)
	return append(content, body...)
}

// signatureHeader returns the Antom "signature" header for one request:
// sha256withRSA over antomSignContent, base64-encoded and then query-escaped
// because the header is a comma-separated key/value list in which a raw "+",
// "/" or "=" would break parsing.
func (c *AntomClient) signatureHeader(method, path, requestTime string, body []byte) (string, error) {
	digest := sha256.Sum256(antomSignContent(method, path, c.cfg.ClientID, requestTime, body))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("antom sign: %w", err)
	}
	escaped := url.QueryEscape(base64.StdEncoding.EncodeToString(signature))
	return fmt.Sprintf("algorithm=RSA256,keyVersion=%s,signature=%s", c.cfg.KeyVersion, escaped), nil
}

// antomAmount is the wire form of an Antom amount.
type antomAmount struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

type antomPeriodRule struct {
	PeriodType  string `json:"periodType"`
	PeriodCount string `json:"periodCount"`
}

type antomPaymentMethod struct {
	PaymentMethodType string `json:"paymentMethodType"`
}

type antomOrderInfo struct {
	OrderAmount antomAmount `json:"orderAmount"`
}

// antomSubscriptionCreateRequest is the /subscriptions/create body. The
// notification URLs are supplied by the caller because they are deployment
// facts (the public API origin), not client identity.
type antomSubscriptionCreateRequest struct {
	SubscriptionRequestID       string             `json:"subscriptionRequestId"`
	SubscriptionDescription     string             `json:"subscriptionDescription"`
	SubscriptionRedirectURL     string             `json:"subscriptionRedirectUrl"`
	SubscriptionStartTime       string             `json:"subscriptionStartTime"`
	PeriodRule                  antomPeriodRule    `json:"periodRule"`
	PaymentMethod               antomPaymentMethod `json:"paymentMethod"`
	SubscriptionNotificationURL string             `json:"subscriptionNotificationUrl"`
	PaymentNotificationURL      string             `json:"paymentNotificationUrl"`
	OrderInfo                   antomOrderInfo     `json:"orderInfo"`
	PaymentAmount               antomAmount        `json:"paymentAmount"`
}

// post signs and sends one Antom API request, then enforces the result
// envelope every Antom response carries. Anything other than resultStatus "S"
// is an error that names the Antom code and message; no caller parses a failed
// body on its own.
func (c *AntomClient) post(ctx context.Context, path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("antom %s: encode request: %w", path, err)
	}
	requestTime := strconv.FormatInt(time.Now().UnixMilli(), 10)
	signature, err := c.signatureHeader(http.MethodPost, path, requestTime, payload)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIBase+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("antom %s: build request: %w", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("client-id", c.cfg.ClientID)
	request.Header.Set("request-time", requestTime)
	request.Header.Set("signature", signature)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("antom %s: %w", path, err)
	}
	defer response.Body.Close()

	// A gateway error page is not a business result; the cap keeps a runaway
	// body from exhausting memory before the envelope is even inspected.
	const maxResponseBytes = 1 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("antom %s: read response: %w", path, err)
	}
	if len(responseBody) > maxResponseBytes {
		return nil, fmt.Errorf("antom %s: response exceeds %d bytes", path, maxResponseBytes)
	}

	var envelope struct {
		Result struct {
			ResultCode    string `json:"resultCode"`
			ResultStatus  string `json:"resultStatus"`
			ResultMessage string `json:"resultMessage"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, fmt.Errorf("antom %s: decode response (HTTP %d): %w", path, response.StatusCode, err)
	}
	if envelope.Result.ResultStatus != "S" {
		return nil, fmt.Errorf("antom %s failed: %s %s", path, envelope.Result.ResultCode, envelope.Result.ResultMessage)
	}
	return responseBody, nil
}

// CreateSubscription asks Antom for the authorization URL. Card is the method a
// recurring contract supports; the hosted page then collects the payment
// details that bind future periods.
func (c *AntomClient) CreateSubscription(ctx context.Context, req AntomSubscriptionRequest) (string, error) {
	// The channel settles in its configured currency; the tier catalog reports
	// the same code, so a configured deployment cannot diverge here.
	amount := antomAmount{Currency: c.cfg.Currency, Value: req.Amount}
	payload := antomSubscriptionCreateRequest{
		SubscriptionRequestID:       req.RequestID,
		SubscriptionDescription:     req.Tier + " monthly",
		SubscriptionRedirectURL:     req.RedirectURL,
		SubscriptionStartTime:       req.StartTime.Format(time.RFC3339),
		PeriodRule:                  antomPeriodRule{PeriodType: "MONTH", PeriodCount: "1"},
		PaymentMethod:               antomPaymentMethod{PaymentMethodType: "CARD"},
		SubscriptionNotificationURL: req.SubscriptionNotifyURL,
		PaymentNotificationURL:      req.PaymentNotifyURL,
		OrderInfo:                   antomOrderInfo{OrderAmount: amount},
		PaymentAmount:               amount,
	}

	responseBody, err := c.post(ctx, antomPathCreateSubscription, payload)
	if err != nil {
		return "", err
	}
	var response struct {
		NormalURL string `json:"normalUrl"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return "", fmt.Errorf("antom %s: decode response: %w", antomPathCreateSubscription, err)
	}
	if response.NormalURL == "" {
		// A "successful" response without a redirect URL leaves the user unable
		// to authorize, so it is a failure of this channel, not a partial success.
		return "", fmt.Errorf("antom %s failed: response has no normalUrl", antomPathCreateSubscription)
	}
	return response.NormalURL, nil
}

// CancelSubscription stops renewals at the end of the paid period. Antom's
// immediate termination (TERMINATE) is deliberately not exposed: the in-app
// cancel keeps the time the user already paid for.
func (c *AntomClient) CancelSubscription(ctx context.Context, subscriptionID string) error {
	_, err := c.post(ctx, antomPathCancelSubscription, struct {
		SubscriptionID   string `json:"subscriptionId"`
		CancellationType string `json:"cancellationType"`
	}{SubscriptionID: subscriptionID, CancellationType: "CANCEL"})
	return err
}

// InquirySubscription reads the subscription state used for reconciliation. A
// missing end time stays zero rather than failing an otherwise valid answer.
func (c *AntomClient) InquirySubscription(ctx context.Context, subscriptionID string) (AntomSubscriptionState, error) {
	responseBody, err := c.post(ctx, antomPathInquirySubscription, struct {
		SubscriptionID string `json:"subscriptionId"`
	}{SubscriptionID: subscriptionID})
	if err != nil {
		return AntomSubscriptionState{}, err
	}

	var response struct {
		SubscriptionID       string `json:"subscriptionId"`
		SubscriptionStatus   string `json:"subscriptionStatus"`
		CurrentPeriodEndTime string `json:"currentPeriodEndTime"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return AntomSubscriptionState{}, fmt.Errorf("antom %s: decode response: %w", antomPathInquirySubscription, err)
	}
	state := AntomSubscriptionState{SubscriptionID: response.SubscriptionID, Status: response.SubscriptionStatus}
	if response.CurrentPeriodEndTime != "" {
		parsed, err := time.Parse(time.RFC3339, response.CurrentPeriodEndTime)
		if err != nil {
			return AntomSubscriptionState{}, fmt.Errorf("antom %s: parse currentPeriodEndTime %q: %w", antomPathInquirySubscription, response.CurrentPeriodEndTime, err)
		}
		state.CurrentPeriodEnd = parsed
	}
	return state, nil
}
