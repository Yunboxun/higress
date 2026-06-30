package main

import (
	"fmt"
	"strconv"

	"ai-model-token-tpm-limit/config"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/tokenusage"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/resp"
)

func main() {}

func init() {
	wrapper.SetCtx(
		"ai-model-token-tpm-limit",
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessStreamingResponseBody(onHttpStreamingBody),
	)
}

const (
	RedisKeyPrefix = "higress-model-token-tpm-limit"

	GlobalRateLimitKeyFormat = RedisKeyPrefix + ":%s:global:%d:%s"
	ModelRateLimitKeyFormat  = RedisKeyPrefix + ":%s:model:%s:%d:%s"

	RequestPhaseFixedWindowScript = `
		local current = redis.call('get', KEYS[1])
		local ttl = redis.call('ttl', KEYS[1])
		local threshold = tonumber(ARGV[1])
		local window = tonumber(ARGV[2])

		if not current then
			return {threshold, 0, window}
		end

		if ttl < 0 then
			ttl = window
		end

		return {threshold, tonumber(current), ttl}
	`

	ResponsePhaseFixedWindowScript = `
		local key = KEYS[1]
		local threshold = tonumber(ARGV[1])
		local window = tonumber(ARGV[2])
		local added = tonumber(ARGV[3])

		local current = tonumber(redis.call('get', key) or "0")

		if current <= threshold then
			current = redis.call('incrby', key, added)
			if current == added then
				redis.call('expire', key, window)
			else
				local ttl = redis.call('ttl', key)
				if ttl < 0 then
					redis.call('expire', key, window)
				end
			end
		end

		return {threshold, current, redis.call('ttl', key)}
	`

	HeaderValueKey       = "HeaderValue"
	RequestModelKey      = "RequestModel"
	NeedModelCheckKey    = "NeedModelCheck"
	RateLimitResetHeader = "X-TokenRateLimit-Reset"
)

func parseConfig(json gjson.Result, cfg *config.PluginConfig) error {
	return config.ParsePluginConfig(json, cfg)
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, cfg config.PluginConfig) types.Action {
	ctx.DisableReroute()

	headerName := cfg.LimitByHeader
	headerValue, err := proxywasm.GetHttpRequestHeader(headerName)
	if err != nil || headerValue == "" {
		return types.ActionContinue
	}

	ctx.SetContext(HeaderValueKey, headerValue)

	if cfg.GlobalThreshold != nil && cfg.GlobalThreshold.MatchKey(headerValue) {
		count, window := cfg.GlobalThreshold.GetLimitForKey(headerValue)
		if count <= 0 {
			return types.ActionContinue
		}

		globalKey := fmt.Sprintf(GlobalRateLimitKeyFormat, cfg.RuleName, window, headerValue)
		keys := []interface{}{globalKey}
		args := []interface{}{count, window}
		evalErr := cfg.RedisClient.Eval(RequestPhaseFixedWindowScript, 1, keys, args, func(response resp.Value) {
			resultArray := response.Array()
			if len(resultArray) != 3 {
				log.Errorf("[ai-model-token-tpm-limit] global redis response error")
				proxywasm.ResumeHttpRequest()
				return
			}

			threshold := resultArray[0].Integer()
			current := resultArray[1].Integer()
			ttl := resultArray[2].Integer()

			if current > threshold {
				ctx.SetUserAttribute("token_ratelimit_status", "global_limited")
				ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)
				rejected(cfg, ttl)
			} else {
				proxywasm.ResumeHttpRequest()
			}
		})
		if evalErr != nil {
			log.Errorf("[ai-model-token-tpm-limit] global redis eval failed: %v", evalErr)
			return types.ActionContinue
		}
		return types.HeaderStopAllIterationAndWatermark
	}

	if len(cfg.RuleItems) > 0 {
		ctx.SetContext(NeedModelCheckKey, true)
	}

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, cfg config.PluginConfig, body []byte) types.Action {
	needCheck, _ := ctx.GetContext(NeedModelCheckKey).(bool)
	if !needCheck {
		return types.ActionContinue
	}

	headerValue, _ := ctx.GetContext(HeaderValueKey).(string)
	if headerValue == "" {
		return types.ActionContinue
	}

	modelName := gjson.GetBytes(body, "model").String()
	if modelName == "" {
		return types.ActionContinue
	}
	ctx.SetContext(RequestModelKey, modelName)

	var matchedLimitKey *config.LimitKey
	for _, ruleItem := range cfg.RuleItems {
		for _, modelItem := range ruleItem.MatchModels {
			if modelItem.Key != modelName {
				continue
			}
			for i := range modelItem.LimitKeys {
				if modelItem.LimitKeys[i].Key == headerValue {
					matchedLimitKey = &modelItem.LimitKeys[i]
					break
				}
			}
			break
		}
		if matchedLimitKey != nil {
			break
		}
	}

	if matchedLimitKey == nil {
		return types.ActionContinue
	}

	redisKey := fmt.Sprintf(ModelRateLimitKeyFormat, cfg.RuleName, modelName, matchedLimitKey.TimeWindow, headerValue)
	keys := []interface{}{redisKey}
	args := []interface{}{matchedLimitKey.Count, matchedLimitKey.TimeWindow}
	err := cfg.RedisClient.Eval(RequestPhaseFixedWindowScript, 1, keys, args, func(response resp.Value) {
		resultArray := response.Array()
		if len(resultArray) != 3 {
			log.Errorf("[ai-model-token-tpm-limit] model redis response error")
			proxywasm.ResumeHttpRequest()
			return
		}

		threshold := resultArray[0].Integer()
		current := resultArray[1].Integer()
		ttl := resultArray[2].Integer()

		if current > threshold {
			ctx.SetUserAttribute("token_ratelimit_status", "model_limited")
			ctx.SetUserAttribute("token_ratelimit_model", modelName)
			ctx.WriteUserAttributeToLogWithKey(wrapper.AILogKey)
			rejected(cfg, ttl)
		} else {
			proxywasm.ResumeHttpRequest()
		}
	})
	if err != nil {
		log.Errorf("[ai-model-token-tpm-limit] model redis eval failed: model=%s err=%v", modelName, err)
		return types.ActionContinue
	}
	return types.ActionPause
}

func onHttpStreamingBody(ctx wrapper.HttpContext, cfg config.PluginConfig, data []byte, endOfStream bool) []byte {
	if usage := tokenusage.GetTokenUsage(ctx, data); usage.TotalToken > 0 {
		ctx.SetContext(tokenusage.CtxKeyInputToken, usage.InputToken)
		ctx.SetContext(tokenusage.CtxKeyOutputToken, usage.OutputToken)
		ctx.SetContext(tokenusage.CtxKeyModel, usage.Model)
	}

	if !endOfStream {
		return data
	}

	if ctx.GetContext(tokenusage.CtxKeyInputToken) == nil || ctx.GetContext(tokenusage.CtxKeyOutputToken) == nil {
		return data
	}
	inputToken := ctx.GetContext(tokenusage.CtxKeyInputToken).(int64)
	outputToken := ctx.GetContext(tokenusage.CtxKeyOutputToken).(int64)
	totalTokens := inputToken + outputToken

	if totalTokens <= 0 {
		return data
	}

	headerValue, _ := ctx.GetContext(HeaderValueKey).(string)
	if headerValue == "" {
		return data
	}

	if cfg.GlobalThreshold != nil && cfg.GlobalThreshold.MatchKey(headerValue) {
		count, window := cfg.GlobalThreshold.GetLimitForKey(headerValue)
		if count > 0 {
			globalKey := fmt.Sprintf(GlobalRateLimitKeyFormat, cfg.RuleName, window, headerValue)
			keys := []interface{}{globalKey}
			args := []interface{}{count, window, totalTokens}
			err := cfg.RedisClient.Eval(ResponsePhaseFixedWindowScript, 1, keys, args, nil)
			if err != nil {
				log.Errorf("[ai-model-token-tpm-limit] global accumulate failed: %v", err)
			}
		}
	}

	responseModelName, _ := ctx.GetContext(tokenusage.CtxKeyModel).(string)
	requestModelName, _ := ctx.GetContext(RequestModelKey).(string)
	modelName := requestModelName
	if modelName == "" || modelName == "unknown" {
		modelName = responseModelName
	}

	if modelName == "" || modelName == "unknown" {
		return data
	}

	matchedModelAccumulation := false
	for _, ruleItem := range cfg.RuleItems {
		for _, modelItem := range ruleItem.MatchModels {
			if modelItem.Key != modelName {
				continue
			}
			matchedModelAccumulation = true
			for _, limitKey := range modelItem.LimitKeys {
				if limitKey.Key != headerValue {
					continue
				}
				redisKey := fmt.Sprintf(ModelRateLimitKeyFormat, cfg.RuleName, modelName, limitKey.TimeWindow, headerValue)
				keys := []interface{}{redisKey}
				args := []interface{}{limitKey.Count, limitKey.TimeWindow, totalTokens}
				err := cfg.RedisClient.Eval(ResponsePhaseFixedWindowScript, 1, keys, args, nil)
				if err != nil {
					log.Errorf("[ai-model-token-tpm-limit] model accumulate failed: model=%s err=%v", modelName, err)
				}
				break
			}
			break
		}
	}
	if !matchedModelAccumulation {
		return data
	}

	return data
}

func rejected(cfg config.PluginConfig, reset int) {
	headers := [][2]string{
		{RateLimitResetHeader, strconv.Itoa(reset)},
	}
	_ = proxywasm.SendHttpResponseWithDetail(
		cfg.RejectedCode, "ai-model-token-tpm-limit.rejected", headers, []byte(cfg.RejectedMsg), -1)
}
