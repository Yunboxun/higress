// token 提供启发式与 tokenize 两种 input token 估算方式，供阈值路由决策使用。
package token

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"hbox-ai-request-router/internal/config"
	"hbox-ai-request-router/internal/router"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

// EstimateInputTokensHeuristic 从请求 body 提取文本并按字符比例估算 token 数。
func EstimateInputTokensHeuristic(body []byte, charsPerToken float64) int64 {
	if len(body) == 0 || !json.Valid(body) {
		return 0
	}
	text := ExtractInputText(body)
	if text == "" {
		return 0
	}
	runeCount := float64(len([]rune(text)))
	if runeCount == 0 {
		return 0
	}
	return int64(math.Ceil(runeCount / charsPerToken))
}

// ExtractSourceModel 从请求 body 中提取原始 model 字段。
func ExtractSourceModel(body []byte, modelKey string) string {
	if len(body) == 0 || !json.Valid(body) {
		return ""
	}
	if modelKey == "" {
		modelKey = "model"
	}
	return gjson.GetBytes(body, modelKey).String()
}

// ExtractInputText 从请求 body 中提取用于 token 估算的文本。
func ExtractInputText(body []byte) string {
	var builder strings.Builder
	appendText := func(value string) {
		if value == "" {
			return
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(value)
	}

	messages := gjson.GetBytes(body, "messages")
	if messages.Exists() && messages.IsArray() {
		for _, msg := range messages.Array() {
			appendMessageContent(msg, appendText)
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {
		for _, tool := range tools.Array() {
			appendToolDefinition(tool, appendText)
		}
	}

	if input := gjson.GetBytes(body, "input"); input.Exists() {
		appendJSONText(input, appendText)
	}
	if prompt := gjson.GetBytes(body, "prompt"); prompt.Exists() {
		appendJSONText(prompt, appendText)
	}
	if system := gjson.GetBytes(body, "system"); system.Exists() {
		appendJSONText(system, appendText)
	}

	return builder.String()
}

// appendMessageContent 从 chat messages 单条消息中提取 content 与 tool_calls 文本。
func appendMessageContent(msg gjson.Result, appendText func(string)) {
	content := msg.Get("content")
	appendJSONText(content, appendText)

	toolCalls := msg.Get("tool_calls")
	if !toolCalls.Exists() || !toolCalls.IsArray() {
		return
	}
	for _, toolCall := range toolCalls.Array() {
		fn := toolCall.Get("function")
		appendText(fn.Get("name").String())
		appendText(fn.Get("arguments").String())
	}
}

// appendToolDefinition 从 tools 定义中提取 function 名称、描述与参数 schema。
func appendToolDefinition(tool gjson.Result, appendText func(string)) {
	fn := tool.Get("function")
	appendText(fn.Get("name").String())
	appendText(fn.Get("description").String())
	if params := fn.Get("parameters"); params.Exists() {
		appendText(params.Raw)
	}
}

// appendJSONText 兼容字符串、多模态数组（type=text）及 object 形式的 content。
func appendJSONText(value gjson.Result, appendText func(string)) {
	if !value.Exists() {
		return
	}
	switch {
	case value.Type == gjson.String:
		appendText(value.String())
	case value.IsArray():
		for _, item := range value.Array() {
			if item.Get("type").String() == "text" {
				appendText(item.Get("text").String())
				continue
			}
			if item.Type == gjson.String {
				appendText(item.String())
			}
		}
	case value.IsObject():
		appendText(value.Get("text").String())
	}
}

// HandleTokenizeRequest 异步调用 tokenize 服务并在回调中决定是否外网路由。
func HandleTokenizeRequest(ctx wrapper.HttpContext, cfg config.PluginConfig, body []byte) types.Action {
	tokenizeCfg := cfg.TokenThreshold.Tokenize
	if tokenizeCfg.Client == nil {
		logTokenizeFailure(ctx, "client_not_initialized")
		return types.ActionContinue
	}

	requestBody := buildTokenizeRequestBody(body)
	headers := [][2]string{{"Content-Type", "application/json"}}

	err := tokenizeCfg.Client.Post(tokenizeCfg.Path, headers, requestBody, func(statusCode int, _ http.Header, responseBody []byte) {
		defer proxywasm.ResumeHttpRequest()

		if statusCode != http.StatusOK {
			logTokenizeFailure(ctx, fmt.Sprintf("http_status_%d", statusCode))
			return
		}

		tokenCount := ParseTokenizeResponse(responseBody)
		log.Debugf("tokenize result: %d, threshold: %d", tokenCount, cfg.TokenThreshold.Threshold)
		if tokenCount > cfg.TokenThreshold.Threshold {
			router.ApplyExternalRouting(ctx, cfg, body, config.RouteReasonTokenThreshold)
		}
	}, tokenizeCfg.Timeout)
	if err != nil {
		logTokenizeFailure(ctx, "post_error")
		log.Errorf("tokenize call error: %v", err)
		return types.ActionContinue
	}
	return types.HeaderStopAllIterationAndWatermark
}

func buildTokenizeRequestBody(body []byte) []byte {
	// 透传原始 body，确保 tokenize 服务能读取完整上下文。
	return body
}

func logTokenizeFailure(ctx wrapper.HttpContext, reason string) {
	ctx.SetUserAttribute("tokenize_status", "failed")
	ctx.SetUserAttribute("tokenize_failure_reason", reason)
	ctx.WriteUserAttributeToLog()
	log.Warnf("tokenize failed (%s), keep original route", reason)
}

// ParseTokenizeResponse 解析 tokenize 服务响应中的 token 数量。
// 跳过值为 0 的字段，避免 count:0 掩盖 usage.prompt_tokens 等有效计数。
func ParseTokenizeResponse(body []byte) int64 {
	if len(body) == 0 {
		return 0
	}
	candidates := []string{
		"count",
		"tokens",
		"token_count",
		"usage.prompt_tokens",
		"usage.total_tokens",
	}
	for _, path := range candidates {
		if value := gjson.GetBytes(body, path); value.Exists() {
			if count := value.Int(); count > 0 {
				return count
			}
		}
	}
	return 0
}
