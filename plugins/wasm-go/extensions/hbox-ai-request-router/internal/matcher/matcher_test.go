package matcher

import (
	"testing"

	"hbox-ai-request-router/internal/config"

	"github.com/tidwall/gjson"
)

func TestMatchValueExact(t *testing.T) {
	rule := config.HeaderRule{
		MatchType: config.MatchTypeExact,
		Values:    []string{"key-a", "key-b"},
	}
	if !MatchValue(rule, "key-a") {
		t.Fatal("expected exact match")
	}
	if MatchValue(rule, "key-c") {
		t.Fatal("expected no match")
	}
}

func TestMatchValuePrefix(t *testing.T) {
	rule := config.HeaderRule{
		MatchType: config.MatchTypePrefix,
		Values:    []string{"sk-ext-"},
	}
	if !MatchValue(rule, "sk-ext-123") {
		t.Fatal("expected prefix match")
	}
	if MatchValue(rule, "sk-int-123") {
		t.Fatal("expected no match")
	}
}

func TestMatchValueRegexp(t *testing.T) {
	rule, err := config.ParseHeaderRule(gjson.Parse(`{
		"source": "header",
		"key": "Authorization",
		"matchType": "regexp",
		"pattern": "^Bearer sk-ext-"
	}`))
	if err != nil {
		t.Fatalf("parse rule failed: %v", err)
	}
	if !MatchValue(rule, "Bearer sk-ext-abc") {
		t.Fatal("expected regexp match")
	}
	if MatchValue(rule, "Bearer sk-int-abc") {
		t.Fatal("expected no match")
	}
}

func TestMatchAppModelRules(t *testing.T) {
	rules := []config.AppModelRule{{
		Header:       config.DefaultAppIDHeader,
		AppIDs:       []string{"app_123"},
		SourceModels: []string{"deepseek-r1", "gpt-4o"},
		Route: config.RouteTargetConfig{
			Provider: "internal-gray",
			Model:    "qwen-max",
		},
	}}
	matched, route, reason := MatchAppModelRules("app_123", "gpt-4o", rules)
	if !matched {
		t.Fatal("expected appModel rule match")
	}
	if reason != config.RouteReasonAppModelRule {
		t.Fatalf("unexpected reason: %s", reason)
	}
	if route == nil || route.Provider != "internal-gray" || route.Model != "qwen-max" {
		t.Fatalf("unexpected route: %#v", route)
	}
}

func TestExtractBearerToken(t *testing.T) {
	if got := ExtractBearerToken("Bearer abc123"); got != "abc123" {
		t.Fatalf("unexpected token: %s", got)
	}
	if got := ExtractBearerToken("abc123"); got != "abc123" {
		t.Fatalf("unexpected token: %s", got)
	}
}

func TestExtractAuthorizationToken(t *testing.T) {
	token, ok := ExtractAuthorizationToken("Bearer sk-ext-123")
	if !ok || token != "sk-ext-123" {
		t.Fatalf("unexpected bearer token: %q ok=%v", token, ok)
	}
	_, ok = ExtractAuthorizationToken("Basic dGVzdA==")
	if ok {
		t.Fatal("expected non-bearer authorization to be rejected")
	}
	_, ok = ExtractAuthorizationToken("Bearer ")
	if ok {
		t.Fatal("expected empty bearer token to be rejected")
	}
}

func TestParsePathQuery(t *testing.T) {
	values, err := parsePathQuery("/v1/chat/completions?api_key=abc&foo=bar")
	if err != nil {
		t.Fatalf("parse query failed: %v", err)
	}
	if values.Get("api_key") != "abc" {
		t.Fatalf("unexpected api_key: %s", values.Get("api_key"))
	}
}
