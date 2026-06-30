package config

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestParseHeaderRuleAuthorization(t *testing.T) {
	rule, err := ParseHeaderRule(gjson.Parse(`{
		"source": "authorization",
		"matchType": "prefix",
		"values": ["sk-ext-"]
	}`))
	if err != nil {
		t.Fatalf("parse rule failed: %v", err)
	}
	if rule.Source != SourceAuthorization {
		t.Fatalf("unexpected source: %s", rule.Source)
	}
}

func TestParseHeaderRuleRegexp(t *testing.T) {
	rule, err := ParseHeaderRule(gjson.Parse(`{
		"source": "header",
		"key": "Authorization",
		"matchType": "regexp",
		"pattern": "^Bearer sk-ext-"
	}`))
	if err != nil {
		t.Fatalf("parse rule failed: %v", err)
	}
	if rule.Pattern == nil {
		t.Fatal("expected compiled pattern")
	}
}

func TestParseConfigRequiresExternalRouting(t *testing.T) {
	cfg := &PluginConfig{}
	err := ParseConfig(gjson.Parse(`{
		"externalRouting": {
			"provider": "openai"
		}
	}`), cfg)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestParseConfigTokenThresholdValidation(t *testing.T) {
	cfg := &PluginConfig{}
	err := ParseConfig(gjson.Parse(`{
		"externalRouting": {
			"provider": "openai",
			"model": "gpt-4o"
		},
		"tokenThreshold": {
			"enabled": true,
			"threshold": 0
		}
	}`), cfg)
	if err == nil {
		t.Fatal("expected error for invalid threshold")
	}
}

func TestParseConfigAppIDRules(t *testing.T) {
	cfg := &PluginConfig{}
	err := ParseConfig(gjson.Parse(`{
		"externalRouting": {
			"provider": "openai",
			"model": "gpt-4o"
		},
		"localRouting": {
			"provider": "internal",
			"model": "qwen-plus"
		},
		"appIdRules": [{
			"values": ["app_123"],
			"route": {
				"provider": "internal-gray",
				"model": "qwen-max"
			}
		}],
		"appModelRules": [{
			"appIds": ["app_123"],
			"sourceModels": ["deepseek-r1"],
			"route": {
				"provider": "internal-gray",
				"model": "qwen-max-32b"
			}
		}]
	}`), cfg)
	if err != nil {
		t.Fatalf("parse config failed: %v", err)
	}
	if cfg.LocalRouting == nil || cfg.LocalRouting.Provider != "internal" {
		t.Fatalf("unexpected localRouting: %#v", cfg.LocalRouting)
	}
	if len(cfg.AppIDRules) != 1 {
		t.Fatalf("expected 1 appIdRule, got %d", len(cfg.AppIDRules))
	}
	if cfg.AppIDRules[0].Header != DefaultAppIDHeader {
		t.Fatalf("unexpected appId header: %s", cfg.AppIDRules[0].Header)
	}
	if cfg.AppIDRules[0].Route.Provider != "internal-gray" || cfg.AppIDRules[0].Route.Model != "qwen-max" {
		t.Fatalf("unexpected appId route: %#v", cfg.AppIDRules[0].Route)
	}
	if len(cfg.AppModelRules) != 1 {
		t.Fatalf("expected 1 appModelRule, got %d", len(cfg.AppModelRules))
	}
	if cfg.AppModelRules[0].Header != DefaultAppIDHeader {
		t.Fatalf("unexpected appModel header: %s", cfg.AppModelRules[0].Header)
	}
	if cfg.AppModelRules[0].SourceModels[0] != "deepseek-r1" {
		t.Fatalf("unexpected source model: %#v", cfg.AppModelRules[0].SourceModels)
	}
}

func TestPathMatchesSuffix(t *testing.T) {
	suffixes := []string{"/completions", "/messages"}
	if !PathMatchesSuffix("/v1/chat/completions?foo=bar", suffixes) {
		t.Fatal("expected path match")
	}
	if PathMatchesSuffix("/v1/models", suffixes) {
		t.Fatal("expected path not match")
	}
}
