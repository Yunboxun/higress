package main

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func main() {}

const pluginName = "strip-cch"

type StripCchConfig struct {
	Enabled bool
}

func init() {
	wrapper.SetCtx(
		pluginName,
		wrapper.ParseConfigBy(parseConfig),
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
		wrapper.ProcessRequestBodyBy(onHttpRequestBody),
	)
}

func parseConfig(json gjson.Result, config *StripCchConfig, log log.Log) error {
	config.Enabled = json.Get("enabled").Bool()
	if !config.Enabled {
		log.Infof("[strip-cch] plugin is disabled by config")
	} else {
		log.Infof("[strip-cch] plugin is enabled")
	}
	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config StripCchConfig, log log.Log) types.Action {
	if !config.Enabled {
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}

	method, _ := proxywasm.GetHttpRequestHeader(":method")
	path, _ := proxywasm.GetHttpRequestHeader(":path")

	if method != "POST" || !strings.HasPrefix(path, "/v1/chat/completions") {
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config StripCchConfig, body []byte, log log.Log) types.Action {
	if !bytes.Contains(body, []byte("cch=")) && !bytes.Contains(body, []byte("cc_version=")) {
		return types.ActionContinue
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return types.ActionContinue
	}

	modified := false

	messages.ForEach(func(key, msg gjson.Result) bool {
		if msg.Get("role").String() != "system" {
			return true
		}

		msgIdx := strconv.Itoa(int(key.Int()))
		content := msg.Get("content")
		if !content.Exists() {
			return true
		}

		if content.Type == gjson.String {
			original := content.String()
			stripped := stripCchFromBillingHeader(original)
			if stripped != original {
				jsonPath := "messages." + msgIdx + ".content"
				var err error
				body, err = sjson.SetBytes(body, jsonPath, stripped)
				if err != nil {
					log.Warnf("[strip-cch] failed to update content at %s: %v", jsonPath, err)
					return true
				}
				modified = true
				log.Debugf("[strip-cch] stripped fields from system message at index %s", msgIdx)
			}
			return true
		}

		if content.IsArray() {
			content.ForEach(func(contentKey, item gjson.Result) bool {
				if item.Get("type").String() != "text" {
					return true
				}
				textValue := item.Get("text")
				if !textValue.Exists() || textValue.Type != gjson.String {
					return true
				}
				original := textValue.String()
				stripped := stripCchFromBillingHeader(original)
				if stripped != original {
					contentIdx := strconv.Itoa(int(contentKey.Int()))
					jsonPath := "messages." + msgIdx + ".content." + contentIdx + ".text"
					var err error
					body, err = sjson.SetBytes(body, jsonPath, stripped)
					if err != nil {
						log.Warnf("[strip-cch] failed to update text at %s: %v", jsonPath, err)
						return true
					}
					modified = true
					log.Debugf("[strip-cch] stripped fields from system message at messages.%s.content.%s", msgIdx, contentIdx)
				}
				return true
			})
		}

		return true
	})

	if modified {
		if err := proxywasm.ReplaceHttpRequestBody(body); err != nil {
			log.Warnf("[strip-cch] failed to replace request body: %v", err)
		} else {
			log.Debugf("[strip-cch] request body updated successfully")
		}
	}

	return types.ActionContinue
}

// Example input:  "x-anthropic-billing-header: cc_version=2.1.37.3a3; cc_entrypoint=claude-vscode; cch=abc123; type:text"
// Example output: "x-anthropic-billing-header: cc_entrypoint=claude-vscode; type:text"
func stripCchFromBillingHeader(text string) string {
	const billingHeaderPrefix = "x-anthropic-billing-header:"
	if !strings.HasPrefix(text, billingHeaderPrefix) {
		return text
	}

	// Extract the part after the prefix
	rest := strings.TrimPrefix(text, billingHeaderPrefix)

	// Split by semicolon
	parts := strings.Split(rest, ";")
	var keptParts []string

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		// Filter out the fields we want to remove
		if strings.HasPrefix(trimmed, "cch=") || strings.HasPrefix(trimmed, "cc_version=") {
			continue
		}
		if trimmed != "" {
			keptParts = append(keptParts, trimmed)
		}
	}

	// Rejoin the remaining parts
	if len(keptParts) == 0 {
		return billingHeaderPrefix
	}

	return billingHeaderPrefix + " " + strings.Join(keptParts, "; ")
}
