package model

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// The managed Aurora sandbox runtime reads exactly one single-use enrollment
// secret from a fixed read-only path. The Fleet writes that file into the node
// secrets volume and mounts the volume read-only at AuroraEnrollmentDir, so the
// path is part of the fixed layout contract, never caller input.
const (
	AuroraEnrollmentDir     = "/secrets"
	AuroraEnrollmentFile    = AuroraEnrollmentDir + "/aurora-enrollment"
	AuroraManagedEnv        = "MULTICA_MANAGED"
	AuroraServerURLEnv      = "MULTICA_SERVER_URL"
	AuroraEnrollmentFileEnv = "MULTICA_MANAGED_ENROLLMENT_TOKEN_FILE"

	// AuroraAnthropicBaseURLEnv and AuroraAnthropicModelEnv forward the optional
	// operator-configured managed Claude endpoint. They are never credentials.
	AuroraAnthropicBaseURLEnv = "ANTHROPIC_BASE_URL"
	AuroraAnthropicModelEnv   = "ANTHROPIC_MODEL"

	// AuroraClaudePathEnv and AuroraClaudePath are the one agent executable the
	// Fleet provides to the managed node. The neutral image no longer bakes the
	// path; the provider sets exactly this variable so the daemon resolves the
	// CLI, and the adoption authority rejects a live container that omits it or
	// carries a different value.
	AuroraClaudePathEnv = "MULTICA_CLAUDE_PATH"
	AuroraClaudePath    = "/usr/local/bin/claude"

	// AuroraWorkspaceMount, AuroraTmpMount and AuroraRunMount are the fixed
	// writable tmpfs surfaces of the Aurora node. Every one is nosuid,nodev,noexec
	// and owned by the sandbox user.
	AuroraWorkspaceMount = "/workspace"
	AuroraTmpMount       = "/tmp"
	AuroraRunMount       = "/run"
)

// enrollmentTokenPattern matches the server-issued managed enrollment secret:
// "mse_" plus 40 lowercase hex characters. Validation is local so junk secrets
// never reach the managed daemon or the enrollment endpoint.
var enrollmentTokenPattern = regexp.MustCompile("^mse_[0-9a-f]{40}$")

// ClaudeEnvPairs returns the configured extra node environment variables as
// KEY=value entries in deterministic key order, so one configuration always
// yields one node environment. An absent or empty map returns nil and adds
// nothing.
func (a AuroraConfig) ClaudeEnvPairs() []string {
	if len(a.ClaudeEnv) == 0 {
		return nil
	}
	keys := make([]string, 0, len(a.ClaudeEnv))
	for key := range a.ClaudeEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+a.ClaudeEnv[key])
	}
	return pairs
}

// AuroraConfig selects the managed-sandbox execution profile for one Fleet
// deployment. It is administrator-owned public configuration: the node image is
// Config.Image, and the node's provider credentials travel through ClaudeEnv
// into the container environment, never through a mounted file. When nil the
// deployment runs the default Claude-only node profile.
type AuroraConfig struct {
	// ServerURL is the container-reachable Multica API origin the managed
	// daemon enrolls against and calls back to. It is not a client-supplied
	// image, path or credential.
	ServerURL string `json:"server_url"`
	// AnthropicBaseURL and AnthropicModel forward the optional managed Claude
	// endpoint override. Empty preserves the provider default.
	AnthropicBaseURL string `json:"anthropic_base_url"`
	AnthropicModel   string `json:"anthropic_model"`
	// ClaudeEnv carries the node's extra environment variables for the managed
	// child. It is a fixed allowlist of model-routing and tool variables plus the
	// providerEnvNames credentials, validated at load; an absent map adds nothing
	// to the node environment. Every other credential-bearing key is refused, and
	// the values are visible to any process in the container.
	ClaudeEnv map[string]string `json:"claude_env"`
	// ReadonlyRootfs must be explicitly true: the managed profile never opts out
	// of the read-only root filesystem.
	ReadonlyRootfs bool `json:"readonly_rootfs"`
}

// Validate rejects an Aurora profile that could not safely enroll or isolate.
// Every field is checked locally before any Docker resource is built, so a
// malformed profile fails closed with no partial provisioning.
func (a AuroraConfig) Validate() error {
	if !ValidOrigin(a.ServerURL) {
		return fmt.Errorf("%w: aurora server_url must be an http(s) origin", ErrInvalidRequest)
	}
	if a.AnthropicBaseURL != "" && !ValidAnthropicBaseURL(a.AnthropicBaseURL) {
		return fmt.Errorf("%w: aurora anthropic_base_url must be a single https host without credentials, query or fragment, with an optional path prefix", ErrInvalidRequest)
	}
	if a.AnthropicModel != "" && !validModelName(a.AnthropicModel) {
		return fmt.Errorf("%w: aurora anthropic_model must be a non-empty model name", ErrInvalidRequest)
	}
	// claude_env is an operator-owned allowlist, not a general credential
	// channel. The secret-marker ban still applies to every key except the
	// providerEnvNames credentials, which exist because the node runs an
	// ordinary agent that calls the providers directly; their values are
	// supplied at deploy time and must never reach the image, SQL, logs, or Git.
	for key, value := range a.ClaudeEnv {
		provider := providerEnvNames[key]
		if !provider && isSecretClaudeEnvKey(key) {
			return fmt.Errorf("%w: aurora claude_env must never carry a credential", ErrInvalidRequest)
		}
		if !provider && !claudeCodeEnvAllowlist[key] {
			return fmt.Errorf("%w: aurora claude_env key is not in the fixed allowlist", ErrInvalidRequest)
		}
		if !validClaudeEnvValue(key, value) {
			return fmt.Errorf("%w: aurora claude_env value is invalid", ErrInvalidRequest)
		}
	}
	if !a.ReadonlyRootfs {
		return fmt.Errorf("%w: aurora readonly_rootfs must be explicitly true", ErrInvalidRequest)
	}

	return nil
}

// claudeCodeEnvAllowlist is the exact set of additional Claude Code variables
// an operator may set through AuroraConfig.ClaudeEnv. It is a fixed allowlist of
// non-secret model-routing and tool variables; every other key is refused at
// config load unless it is one of the providerEnvNames credentials, so the
// channel can never carry an unvetted variable.
var claudeCodeEnvAllowlist = map[string]bool{
	"ANTHROPIC_DEFAULT_FABLE_MODEL":            true,
	"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME":       true,
	"ANTHROPIC_DEFAULT_HAIKU_MODEL":            true,
	"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME":       true,
	"ANTHROPIC_DEFAULT_OPUS_MODEL":             true,
	"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":        true,
	"ANTHROPIC_DEFAULT_SONNET_MODEL":           true,
	"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME":      true,
	"CLAUDE_CODE_SUBAGENT_MODEL":               true,
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": true,
	"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS":     true,
	"ENABLE_TOOL_SEARCH":                       true,
	"API_TIMEOUT_MS":                           true,
}

// providerEnvNames are the only secret-bearing keys the operator may inject
// through the Fleet config's claude_env. They exist because the node runs an
// ordinary agent that calls the providers directly, so their values must be
// supplied at deploy time from an owner-only Fleet config and must never reach
// the image, SQL, logs, or Git.
//
// ANTHROPIC_API_KEY is the Ark Agent Plan key and only serves the node's Claude
// CLI. The image and video skills call the platform endpoint /api/v3 instead,
// which rejects an Agent Plan key with 401, so ARK_PLATFORM_API_KEY is a second,
// separate provider credential. OPENAI_API_KEY is deliberately absent: the
// OpenAI route is gone (decision 10). The Volcengine speech endpoint is not
// Anthropic-compatible either, so VOLC_ASR_API_KEY is a third.
var providerEnvNames = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ARK_PLATFORM_API_KEY": true,
	"VOLC_ASR_API_KEY":     true,
}

// claudeEnvSecretMarkers are the substrings that mark a claude_env key as
// credential-bearing. Such a key is refused unless it is one of the
// providerEnvNames credentials, before the allowlist is even consulted.
var claudeEnvSecretMarkers = []string{"API_KEY", "AUTH_TOKEN", "TOKEN", "SECRET", "PASSWORD"}

// maxClaudeEnvValueLen bounds one operator-supplied claude_env value.
const maxClaudeEnvValueLen = 256

// apiTimeoutPattern admits only a whole number of milliseconds.
var apiTimeoutPattern = regexp.MustCompile("^[0-9]+$")

// isSecretClaudeEnvKey reports whether a claude_env key names a credential. The
// test is case-insensitive so an unusual spelling cannot smuggle a secret past
// the exact allowlist.
func isSecretClaudeEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range claudeEnvSecretMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// anthropicBaseURLPathPattern matches the optional path prefix of an Anthropic
// base URL. It starts with a single slash and then permits only URL path-segment
// characters: unreserved (A-Z a-z 0-9 - . _ ~), percent-encoding, the standard
// sub-delims (! $ & ' ( ) * + , ; =) and "/" separators.
var anthropicBaseURLPathPattern = regexp.MustCompile(`^/[A-Za-z0-9-._~/%!$&'()*+,;=]*$`)

// ValidAnthropicBaseURL requires a single https host with no credentials, query
// or fragment, plus an optional path prefix. An empty path is the bare origin; a
// non-empty path must be an absolute, already-decoded and unambiguous request
// prefix: it starts with "/", contains only unreserved/sub-delim path characters
// or percent encoding, never contains "//", never ends in "/", never has a "."
// or ".." segment, and u.RawPath agrees with u.Path so encoded bytes cannot
// decode to a different request path. This admits an endpoint such as
// https://ark.cn-beijing.volces.com/api/plan while staying as strict as the
// managed daemon's own startup validation.
func ValidAnthropicBaseURL(raw string) bool {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.Contains(raw, "#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil ||
		u.Host == "" || u.Hostname() == "" || strings.ContainsAny(u.Host, ", \t") || u.Port() != "" {
		return false
	}
	if u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.RawPath != "" && u.RawPath != u.Path) {
		return false
	}
	if u.Path == "" {
		return true
	}
	if !anthropicBaseURLPathPattern.MatchString(u.Path) || strings.Contains(u.Path, "//") || strings.HasSuffix(u.Path, "/") {
		return false
	}
	for _, segment := range strings.Split(u.Path[1:], "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// validModelName requires a single opaque model token with no whitespace.
func validModelName(raw string) bool {
	if raw == "" || strings.TrimSpace(raw) != raw || len(raw) > 128 {
		return false
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// validClaudeEnvValue accepts a non-empty value with no surrounding or embedded
// whitespace or control characters, within the length bound; API_TIMEOUT_MS is
// additionally digits-only. Bracketed model suffixes such as glm-5.3-flash[1M]
// are ordinary printable characters and remain valid.
func validClaudeEnvValue(key, value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maxClaudeEnvValueLen {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return key != "API_TIMEOUT_MS" || apiTimeoutPattern.MatchString(value)
}

// ValidEnrollmentToken reports whether token matches the server-issued managed
// enrollment format. The Fleet never derives identity from the secret.
func ValidEnrollmentToken(token string) bool {
	return enrollmentTokenPattern.MatchString(token)
}

// ValidOrigin accepts only a bare http(s) origin, so a configured server URL
// cannot smuggle credentials, a path, a query or a fragment into the managed
// daemon environment.
func ValidOrigin(raw string) bool {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.Contains(raw, "#") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Path == "" && u.RawPath == ""
}
