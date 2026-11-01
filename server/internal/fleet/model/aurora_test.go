package model

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const auroraConfig = `{"namespace":"local","fleet_id":"fleet-1","image":"trusted/image:dev","api_url":"http://127.0.0.1:8080","specs":{"small":{}},"aurora":{"server_url":"http://api.internal:8080","anthropic_base_url":"https://ark.example.com","anthropic_model":"ark-model","readonly_rootfs":true}}`

func TestLoadConfigAuroraProfile(t *testing.T) {
	cfg, err := loadTestConfig(t, auroraConfig)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Aurora == nil || cfg.Aurora.ServerURL != "http://api.internal:8080" ||
		!cfg.Aurora.ReadonlyRootfs {
		t.Fatalf("aurora profile lost: %+v", cfg.Aurora)
	}
	// A configuration without the profile keeps the default Claude-only node.
	plain, err := loadTestConfig(t, validConfig)
	if err != nil || plain.Aurora != nil {
		t.Fatalf("default profile = %+v err=%v", plain.Aurora, err)
	}
}

// TestLoadConfigAuroraAnthropicBaseURL proves the managed Claude endpoint accepts
// the real ARK Agent Plan prefix for a full config load while a bare origin still
// works, and that the Validate error names the optional path prefix.
func TestLoadConfigAuroraAnthropicBaseURL(t *testing.T) {
	accepted := map[string]string{
		"ark agent plan": "https://ark.cn-beijing.volces.com/api/plan",
		"origin only":    "https://ark.example.com",
	}
	for name, base := range accepted {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadTestConfig(t, strings.Replace(auroraConfig, "https://ark.example.com", base, 1))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Aurora == nil || cfg.Aurora.AnthropicBaseURL != base {
				t.Fatalf("anthropic_base_url = %+v, want %q", cfg.Aurora, base)
			}
		})
	}
	_, err := loadTestConfig(t, strings.Replace(auroraConfig, "https://ark.example.com", "https://ark.example.com?x=1", 1))
	if err == nil || !strings.Contains(err.Error(), "optional path prefix") {
		t.Fatalf("query error = %v, want optional path prefix wording", err)
	}
}

func TestLoadConfigRejectsUnsafeAuroraProfile(t *testing.T) {
	cases := map[string]string{
		"missing server url":          strings.Replace(auroraConfig, `"server_url":"http://api.internal:8080"`, "", 1),
		"credentials in url":          strings.Replace(auroraConfig, "http://api.internal:8080", "http://user:pass@api.internal:8080", 1),
		"path in url":                 strings.Replace(auroraConfig, "http://api.internal:8080", "http://api.internal:8080/api", 1),
		"http anthropic base":         strings.Replace(auroraConfig, "https://ark.example.com", "http://ark.example.com", 1),
		"anthropic port":              strings.Replace(auroraConfig, "https://ark.example.com", "https://ark.example.com:8443", 1),
		"anthropic query":             strings.Replace(auroraConfig, "https://ark.example.com", "https://ark.example.com?x=1", 1),
		"anthropic fragment":          strings.Replace(auroraConfig, "https://ark.example.com", "https://ark.example.com#frag", 1),
		"anthropic credentials":       strings.Replace(auroraConfig, "https://ark.example.com", "https://user:pass@ark.example.com", 1),
		"claude env auth token":       strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"ANTHROPIC_AUTH_TOKEN":"leak"},`, 1),
		"claude env secret key":       strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"MY_SECRET_VALUE":"leak"},`, 1),
		"claude env unknown key":      strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"CLAUDE_CODE_UNKNOWN":"x"},`, 1),
		"claude env empty value":      strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"ENABLE_TOOL_SEARCH":""},`, 1),
		"claude env leading ws":       strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"ENABLE_TOOL_SEARCH":" true"},`, 1),
		"claude env control char":     strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"ENABLE_TOOL_SEARCH":"true\t1"},`, 1),
		"claude env bad timeout":      strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"API_TIMEOUT_MS":"soon"},`, 1),
		"claude env negative timeout": strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"API_TIMEOUT_MS":"-1"},`, 1),
		"claude env too long":         strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":{"ENABLE_TOOL_SEARCH":"`+strings.Repeat("a", 257)+`"},`, 1),
		"claude env array":            strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`, `"anthropic_model":"ark-model","claude_env":[],`, 1),
		"readonly false":              strings.Replace(auroraConfig, `"readonly_rootfs":true`, `"readonly_rootfs":false`, 1),
		"missing readonly":            strings.Replace(auroraConfig, `,"readonly_rootfs":true`, "", 1),
		"unknown nested key":          strings.Replace(auroraConfig, `"aurora":{"server_url"`, `"aurora":{"image":"evil","server_url"`, 1),
		"nested null":                 strings.Replace(auroraConfig, `"readonly_rootfs":true`, `"readonly_rootfs":null`, 1),
		"server url type":             strings.Replace(auroraConfig, `"server_url":"http://api.internal:8080"`, `"server_url":7`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadTestConfig(t, raw)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("LoadConfig error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// TestLoadConfigAuroraClaudeEnv proves the optional extra Claude Code map is
// accepted for every allowlisted key, including the bracketed model suffix, and
// is empty when the field is omitted.
func TestLoadConfigAuroraClaudeEnv(t *testing.T) {
	raw := strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`,
		`"anthropic_model":"ark-model","claude_env":{"ANTHROPIC_DEFAULT_SONNET_MODEL":"glm-5.3-flash[1M]","ANTHROPIC_DEFAULT_SONNET_MODEL_NAME":"glm-5.3-flash[1M]","CLAUDE_CODE_SUBAGENT_MODEL":"glm-5.3-flash[1M]","CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1","CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS":"1","ENABLE_TOOL_SEARCH":"true","API_TIMEOUT_MS":"600000"},`, 1)
	cfg, err := loadTestConfig(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL":           "glm-5.3-flash[1M]",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME":      "glm-5.3-flash[1M]",
		"CLAUDE_CODE_SUBAGENT_MODEL":               "glm-5.3-flash[1M]",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS":     "1",
		"ENABLE_TOOL_SEARCH":                       "true",
		"API_TIMEOUT_MS":                           "600000",
	}
	if !reflect.DeepEqual(cfg.Aurora.ClaudeEnv, want) {
		t.Fatalf("claude_env = %+v", cfg.Aurora.ClaudeEnv)
	}
	// An absent map adds no node environment and is not an empty-but-present one.
	plain, err := loadTestConfig(t, auroraConfig)
	if err != nil || len(plain.Aurora.ClaudeEnv) != 0 || plain.Aurora.ClaudeEnvPairs() != nil {
		t.Fatalf("absent claude_env not empty: %+v err=%v", plain.Aurora.ClaudeEnv, err)
	}
}

// TestLoadConfigAuroraProviderEnvCredentials proves the provider credentials the
// node runs with may travel through claude_env - the relay token as
// ANTHROPIC_API_KEY for the CLI, the media key the image and video skills call
// with, and the Volcengine speech key - while every other credential-bearing key
// stays refused, including the retired ARK_PLATFORM_API_KEY name.
func TestLoadConfigAuroraProviderEnvCredentials(t *testing.T) {
	raw := strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`,
		`"anthropic_model":"ark-model","claude_env":{"ANTHROPIC_API_KEY":"relay-key","AURORA_MEDIA_API_KEY":"media-key","VOLC_ASR_API_KEY":"asr-key"},`, 1)
	cfg, err := loadTestConfig(t, raw)
	if err != nil {
		t.Fatalf("provider credentials rejected: %v", err)
	}
	want := map[string]string{"ANTHROPIC_API_KEY": "relay-key", "AURORA_MEDIA_API_KEY": "media-key", "VOLC_ASR_API_KEY": "asr-key"}
	if !reflect.DeepEqual(cfg.Aurora.ClaudeEnv, want) {
		t.Fatalf("claude_env = %+v, want the provider credentials", cfg.Aurora.ClaudeEnv)
	}
	pairs := cfg.Aurora.ClaudeEnvPairs()
	if !reflect.DeepEqual(pairs, []string{"ANTHROPIC_API_KEY=relay-key", "AURORA_MEDIA_API_KEY=media-key", "VOLC_ASR_API_KEY=asr-key"}) {
		t.Fatalf("node env pairs = %v", pairs)
	}
	// A credential-bearing key outside the provider set is still refused.
	other := strings.Replace(auroraConfig, `"anthropic_model":"ark-model",`,
		`"anthropic_model":"ark-model","claude_env":{"ARK_API_KEY":"leak"},`, 1)
	if _, err := loadTestConfig(t, other); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ARK_API_KEY accepted: %v", err)
	}
}

// TestAuroraClaudeEnvPairsDeterministic pins the sorted KEY=value order and the
// nil result for an empty map, so one configuration yields one node env.
func TestAuroraClaudeEnvPairsDeterministic(t *testing.T) {
	cfg := AuroraConfig{ClaudeEnv: map[string]string{
		"ENABLE_TOOL_SEARCH":         "true",
		"API_TIMEOUT_MS":             "5",
		"CLAUDE_CODE_SUBAGENT_MODEL": "m",
	}}
	want := []string{"API_TIMEOUT_MS=5", "CLAUDE_CODE_SUBAGENT_MODEL=m", "ENABLE_TOOL_SEARCH=true"}
	if got := cfg.ClaudeEnvPairs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("pairs = %v, want %v", got, want)
	}
	if got := (AuroraConfig{}).ClaudeEnvPairs(); got != nil {
		t.Fatalf("empty pairs = %v, want nil", got)
	}
}

// TestAuroraClaudePathContract pins the one agent executable path the provider
// supplies at container start. The neutral image bakes no MULTICA_CLAUDE_PATH,
// so this exact pairing is the whole runtime contract the Fleet must satisfy.
func TestAuroraClaudePathContract(t *testing.T) {
	if AuroraClaudePathEnv != "MULTICA_CLAUDE_PATH" {
		t.Fatalf("AuroraClaudePathEnv = %q", AuroraClaudePathEnv)
	}
	if AuroraClaudePath != "/usr/local/bin/claude" {
		t.Fatalf("AuroraClaudePath = %q", AuroraClaudePath)
	}
}

// TestValidAnthropicBaseURL pins the exact endpoint shape: a single https host
// with no credentials, query or fragment, plus an optional absolute path prefix.
// The ARK Agent Plan Messages endpoint is the value that must be accepted; every
// other guard stays fail-closed.
func TestValidAnthropicBaseURL(t *testing.T) {
	valid := map[string]string{
		"origin":            "https://ark.cn-beijing.volces.com",
		"ark agent plan":    "https://ark.cn-beijing.volces.com/api/plan",
		"versioned prefix":  "https://ark.cn-beijing.volces.com/api/plan/v2",
		"unreserved prefix": "https://ark.example.com/api_plan-1.2~",
		"sub-delim prefix":  "https://ark.example.com/api!$&'()*+,;=plan",
		"canonical percent": "https://ark.example.com/api%25plan",
	}
	for name, raw := range valid {
		t.Run(name, func(t *testing.T) {
			if !ValidAnthropicBaseURL(raw) {
				t.Fatalf("ValidAnthropicBaseURL(%q) = false, want true", raw)
			}
		})
	}
	invalid := map[string]string{
		"empty":                               "",
		"surrounding whitespace":              " https://ark.example.com/api/plan",
		"trailing whitespace":                 "https://ark.example.com/api/plan ",
		"embedded space":                      "https://ark.example.com/api plan",
		"control character":                   "https://ark.example.com/api\x01plan",
		"http scheme":                         "http://ark.example.com/api/plan",
		"credentials":                         "https://user:pass@ark.example.com/api/plan",
		"port":                                "https://ark.example.com:8443/api/plan",
		"empty host":                          "https:///api/plan",
		"comma host":                          "https://a,b/api/plan",
		"query":                               "https://ark.example.com/api/plan?x=1",
		"force query":                         "https://ark.example.com/api/plan?",
		"fragment":                            "https://ark.example.com/api/plan#frag",
		"hash anywhere":                       "https://ark.example.com/api#plan",
		"opaque path not starting with slash": "https:api/plan",
		"doubled slash":                       "https://ark.example.com/api//plan",
		"trailing slash":                      "https://ark.example.com/api/plan/",
		"root slash":                          "https://ark.example.com/",
		"dot segment":                         "https://ark.example.com/api/../plan",
		"encoded traversal":                   "https://ark.example.com/api/%2e%2e/plan",
		"encoded slash":                       "https://ark.example.com/api%2Fplan",
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			if ValidAnthropicBaseURL(raw) {
				t.Fatalf("ValidAnthropicBaseURL(%q) = true, want false", raw)
			}
		})
	}
}

func TestValidEnrollmentToken(t *testing.T) {
	valid := "mse_" + strings.Repeat("a", 40)
	if !ValidEnrollmentToken(valid) {
		t.Fatal("server-issued enrollment token must be accepted")
	}
	for name, token := range map[string]string{
		"empty":        "",
		"wrong prefix": "msx_" + strings.Repeat("a", 40),
		"short":        "mse_" + strings.Repeat("a", 39),
		"long":         "mse_" + strings.Repeat("a", 41),
		"uppercase":    "mse_" + strings.Repeat("A", 40),
		"non hex":      "mse_" + strings.Repeat("z", 40),
		"node token":   "mcn_" + strings.Repeat("a", 43),
	} {
		t.Run(name, func(t *testing.T) {
			if ValidEnrollmentToken(token) {
				t.Fatalf("token %q must be rejected", token)
			}
		})
	}
}
