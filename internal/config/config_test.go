package config

import (
	"strings"
	"testing"
	"time"
)

func requireErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

// withKey satisfies the at-least-one-key rule (spec 04 §3) for fixtures
// that target a later validation check.
const withKey = "api-keys:\n  - value: sk-dummy\n"

func TestLoadMinimalAppliesAllDefaults(t *testing.T) {
	c, err := Load([]byte(withKey))
	if err != nil {
		t.Fatalf("Load(minimal) failed: %v", err)
	}
	if c.ModelsDocURL != "https://opencode.ai/v2/docs/console/go" {
		t.Errorf("ModelsDocURL = %q", c.ModelsDocURL)
	}
	if c.UsageURL != "https://opencode.ai/inference/go/v1/usage" {
		t.Errorf("UsageURL = %q", c.UsageURL)
	}
	if !c.ModelPrefix.Enabled || c.ModelPrefix.Value != "opencode-go" {
		t.Errorf("ModelPrefix = %+v", c.ModelPrefix)
	}
	if len(c.APIKeys) != 1 || c.APIKeys[0].Value != "sk-dummy" {
		t.Errorf("APIKeys = %+v", c.APIKeys)
	}
	if c.Catalog.RefreshInterval != 15*time.Minute {
		t.Errorf("RefreshInterval = %v", c.Catalog.RefreshInterval)
	}
	if !c.Catalog.StaleWhileUnavailable {
		t.Errorf("StaleWhileUnavailable = false")
	}
	if !c.Protocols.ChatCompletions || !c.Protocols.Messages || !c.Protocols.Responses || !c.Protocols.Gemini {
		t.Errorf("Protocols = %+v", c.Protocols)
	}
	if c.AllowHTTP {
		t.Errorf("AllowHTTP = true")
	}
	if c.RequestTimeout != 5*time.Minute {
		t.Errorf("RequestTimeout = %v", c.RequestTimeout)
	}
	if c.MaxResponseBytes != 67108864 {
		t.Errorf("MaxResponseBytes = %d", c.MaxResponseBytes)
	}
}

func TestLoadFullConfigHonorsEveryField(t *testing.T) {
	t.Setenv("TEST_OG_KEY", "test-secret-value")
	doc := `
enabled: true
future-key: 1
models-doc-url: http://localhost:9090/docs/go
usage-url: https://example.com/v1/usage
model-prefix:
  enabled: true
  value: myprefix
api-keys:
  - value: ${TEST_OG_KEY}
catalog:
  refresh-interval: 1h
  stale-while-unavailable: false
protocols:
  chat-completions: false
  messages: false
  responses: false
  gemini: false
allow-http: true
request-timeout: 5m
max-response-bytes: 1024
`
	c, err := Load([]byte(doc))
	if err != nil {
		t.Fatalf("Load(full) failed: %v", err)
	}
	if c.ModelsDocURL != "http://localhost:9090/docs/go" {
		t.Errorf("ModelsDocURL = %q", c.ModelsDocURL)
	}
	if c.UsageURL != "https://example.com/v1/usage" {
		t.Errorf("UsageURL = %q", c.UsageURL)
	}
	if !c.ModelPrefix.Enabled || c.ModelPrefix.Value != "myprefix" {
		t.Errorf("ModelPrefix = %+v", c.ModelPrefix)
	}
	if len(c.APIKeys) != 1 || c.APIKeys[0].Value != "test-secret-value" {
		t.Errorf("APIKeys = %+v", c.APIKeys)
	}
	if c.Catalog.RefreshInterval != time.Hour {
		t.Errorf("RefreshInterval = %v", c.Catalog.RefreshInterval)
	}
	if c.Catalog.StaleWhileUnavailable {
		t.Errorf("StaleWhileUnavailable = true")
	}
	if c.Protocols.ChatCompletions || c.Protocols.Messages || c.Protocols.Responses || c.Protocols.Gemini {
		t.Errorf("Protocols = %+v", c.Protocols)
	}
	if !c.AllowHTTP {
		t.Errorf("AllowHTTP = false")
	}
	if c.RequestTimeout != 5*time.Minute {
		t.Errorf("RequestTimeout = %v", c.RequestTimeout)
	}
	if c.MaxResponseBytes != 1024 {
		t.Errorf("MaxResponseBytes = %d", c.MaxResponseBytes)
	}
}

func TestLoadEnvExpansion(t *testing.T) {
	t.Setenv("TEST_OG_KEY", "expanded-secret")
	c, err := Load([]byte("api-keys:\n  - value: ${TEST_OG_KEY}\n"))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(c.APIKeys) != 1 || c.APIKeys[0].Value != "expanded-secret" {
		t.Fatalf("APIKeys = %+v", c.APIKeys)
	}
}

func TestLoadIgnoresRemovedSchedulerFields(t *testing.T) {
	doc := "routing:\n  strategy: round-robin\n  max-retries-per-request: 9\n  transient-retry-interval: 1s\n  fallback-cooldown: 1m\n" +
		"api-keys:\n  - value: sk-dummy\n    priority: 7\n"
	if _, err := Load([]byte(doc)); err != nil {
		t.Fatalf("removed scheduler fields must remain unknown and ignored: %v", err)
	}
}

// Routing now comes from the docs page; configs written for the old
// base-url/catalog-url/route-overrides keys still load, with those keys
// ignored like any other unknown field.
func TestLoadIgnoresRemovedRoutingFields(t *testing.T) {
	doc := "base-url: https://opencode.ai/zen/go/v1\n" +
		"catalog-url: https://opencode.ai/zen/go/v1/models\n" +
		"route-overrides:\n  m:\n    protocol: grpc\n    endpoint: x\n" + withKey
	c, err := Load([]byte(doc))
	if err != nil {
		t.Fatalf("removed routing fields must be ignored: %v", err)
	}
	if c.ModelsDocURL != DefaultModelsDocURL || c.UsageURL != DefaultUsageURL {
		t.Errorf("defaults not applied: %q %q", c.ModelsDocURL, c.UsageURL)
	}
}

func TestPrefixDisabledSkipsValueValidation(t *testing.T) {
	doc := `
model-prefix:
  enabled: false
  value: "has space"
` + withKey
	c, err := Load([]byte(doc))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := PublicID(c, "glm-5.2"); got != "glm-5.2" {
		t.Errorf("PublicID = %q", got)
	}
}

func TestPublicID(t *testing.T) {
	enabled := Config{ModelPrefix: ModelPrefix{Enabled: true, Value: "opencode-go"}}
	if got := PublicID(enabled, "glm-5.2"); got != "opencode-go/glm-5.2" {
		t.Errorf("PublicID(enabled) = %q", got)
	}
	disabled := Config{ModelPrefix: ModelPrefix{Enabled: false}}
	if got := PublicID(disabled, "glm-5.2"); got != "glm-5.2" {
		t.Errorf("PublicID(disabled) = %q", got)
	}
}

func TestLoadRejections(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"invalid yaml syntax", "[unclosed", "decode config"},
		{"unknown anchor has no line info", "request-timeout: *nope\n", "decode config: invalid YAML structure"},
		{"bare-scalar api key never leaks", "api-keys:\n  - sk-live-secret-123\n", "decode config: invalid YAML structure"},
		{"keyless config rejected", "", "api-keys: at least one key is required"},
		{"request-timeout zero rejected", "request-timeout: 0s\n" + withKey, "request-timeout: must be positive"},
		{
			"duplicate mapping keys",
			"protocols:\n  messages: true\n  messages: false\n",
			"decode config: invalid YAML structure near line",
		},
		{
			"key expands to empty",
			"api-keys:\n  - value: ${TEST_OG_DEFINITELY_UNSET_VAR}\n",
			"api-keys[0].value: expanded to empty",
		},
		{
			"second key expands to empty",
			"api-keys:\n  - value: literal-ok\n  - value: ${TEST_OG_DEFINITELY_UNSET_VAR}\n",
			"api-keys[1].value: expanded to empty",
		},
		{"explicitly empty models-doc-url", `models-doc-url: ""`, "models-doc-url: must not be empty"},
		{"explicitly empty usage-url", `usage-url: ""`, "usage-url: must not be empty"},
		{"malformed models-doc-url", `models-doc-url: "::::"`, "models-doc-url: invalid URL"},
		{"scheme-less models-doc-url", "models-doc-url: opencode.ai/v2/docs/console/go", "models-doc-url: invalid URL"},
		{"malformed usage-url", `usage-url: "http://exa mple.com"`, "usage-url: invalid URL"},
		{"http models-doc-url without allow-http", "models-doc-url: http://localhost:9090/v1", "models-doc-url: http scheme requires allow-http"},
		{"http usage-url without allow-http", "usage-url: http://localhost:9090/v1/usage", "usage-url: http scheme requires allow-http"},
		{"ftp models-doc-url", "models-doc-url: ftp://files.example.com/v1", `models-doc-url: unsupported scheme "ftp"`},
		{"file usage-url", "usage-url: file://host/share/usage", `usage-url: unsupported scheme "file"`},
		{"ftp models-doc-url with allow-http", "allow-http: true\nmodels-doc-url: ftp://files.example.com/v1", `models-doc-url: unsupported scheme "ftp"`},
		{"file usage-url with allow-http", "allow-http: true\nusage-url: file://host/share/usage", `usage-url: unsupported scheme "file"`},
		{"refresh-interval missing unit", "catalog:\n  refresh-interval: 5\n", "catalog.refresh-interval: invalid duration"},
		{"refresh-interval negative", "catalog:\n  refresh-interval: -5m\n" + withKey, "catalog.refresh-interval: must be at least 1m"},
		{"refresh-interval below floor", "catalog:\n  refresh-interval: 1ms\n" + withKey, "catalog.refresh-interval: must be at least 1m"},
		{"request-timeout unparsable", "request-timeout: bogus\n", "request-timeout: invalid duration"},
		{"request-timeout explicitly empty", `request-timeout: ""`, "request-timeout: invalid duration"},
		{"request-timeout zero rejected", "request-timeout: 0s\n" + withKey, "request-timeout: must be positive"},
		{"userinfo credentials never echoed", "models-doc-url: https://user:sekret@/v1", "models-doc-url: invalid URL https://"},
		{"max-response-bytes zero rejected", "max-response-bytes: 0\n" + withKey, "max-response-bytes: must be positive"},
		{"max-response-bytes negative rejected", "max-response-bytes: -5\n" + withKey, "max-response-bytes: must be positive"},
		{"prefix enabled empty value", "model-prefix:\n  enabled: true\n  value: \"\"\n" + withKey, "model-prefix.value: invalid provider-ID characters"},
		{"prefix with space", "model-prefix:\n  value: has space\n" + withKey, "model-prefix.value: invalid provider-ID characters"},
		{"prefix leading dash", "model-prefix:\n  value: -lead\n" + withKey, "model-prefix.value: invalid provider-ID characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(tt.yaml))
			requireErrContains(t, err, tt.wantErr)
			if tt.name == "key expands to empty" && strings.Contains(err.Error(), "${") {
				t.Errorf("error leaks environment reference: %q", err.Error())
			}
		})
	}
}

// TestLoadErrorsNeverLeakDecodedValues pins the security property: decode
// and validation failures must not echo raw scalars (a bare-scalar API key)
// or embedded userinfo credentials into the error the host logs.
func TestLoadErrorsNeverLeakDecodedValues(t *testing.T) {
	cases := []struct {
		name   string
		yaml   string
		secret string
	}{
		{"bare-scalar api key", "api-keys:\n  - sk-live-supersecret-42\n", "sk-live-supersecret-42"},
		{"duplicate api key values", "api-keys:\n  - value: sk-live-dupsecret-7\n  - value: sk-live-dupsecret-7\n", "sk-live-dupsecret-7"},
		{"userinfo credentials", "usage-url: https://admin:p4ssw0rd@/v1\n", "p4ssw0rd"},
	}
	for _, tc := range cases {
		_, err := Load([]byte(tc.yaml))
		if err == nil {
			t.Fatalf("%s: expected rejection", tc.name)
		}
		if strings.Contains(err.Error(), tc.secret) {
			t.Fatalf("%s: error leaks secret %q: %v", tc.name, tc.secret, err)
		}
	}
}

// TestValidateURLRejectsQueryFragmentUserinfo pins that configured URLs are
// plain locations: a query or fragment changes what is fetched, and
// userinfo would leak credentials into every request URL.
func TestValidateURLRejectsQueryFragmentUserinfo(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		httpOK  bool
		wantErr string
	}{
		{"query rejected naming setting", "https://host/v1?tenant=x", false, "usage-url: must not contain query"},
		{"fragment rejected", "https://host/v1#frag", false, "must not contain query, fragment, or userinfo"},
		{"userinfo rejected without echoing credentials", "https://user:key@host/v1", false, "must not contain query, fragment, or userinfo"},
		{"clean https passes", "https://host/v1", false, ""},
		{"http with allow-http passes", "http://localhost:9090/v1", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL("usage-url", tt.raw, tt.httpOK)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateURL(%q) = %v, want nil", tt.raw, err)
				}
				return
			}
			requireErrContains(t, err, tt.wantErr)
			if strings.Contains(err.Error(), "key") && strings.Contains(tt.raw, ":key@") {
				t.Errorf("error echoes credentials: %q", err.Error())
			}
		})
	}
}
