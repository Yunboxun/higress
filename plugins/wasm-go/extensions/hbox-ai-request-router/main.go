// Higress WASM 插件：根据 app_id、app_id+原模型、header 规则或 token 阈值，将 AI 请求路由到本地/外网模型。
package main

import (
	"hbox-ai-request-router/internal/config"
	"hbox-ai-request-router/internal/matcher"
	"hbox-ai-request-router/internal/router"
	"hbox-ai-request-router/internal/token"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
)

func main() {}

// init 注册插件上下文：配置解析、请求头/体处理回调及 body 缓冲上限。
func init() {
	wrapper.SetCtx(
		"hbox-ai-request-router",
		wrapper.ParseConfig(config.ParseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.WithRebuildMaxMemBytes[config.PluginConfig](200*1024*1024),
	)
}

// onHttpRequestHeaders 在请求头阶段做快速过滤与路由预判。
// 优先 app_id+原模型路由，其次 app_id 本地路由，再评估 header 规则外网路由；若需读取 body 则暂停并等待 body。
func onHttpRequestHeaders(ctx wrapper.HttpContext, cfg config.PluginConfig) types.Action {
	path := ctx.Path()
	log.Debugf("request headers phase: enabled=%v path=%q appModelRules=%d appIdRules=%d headerRules=%d tokenThresholdEnabled=%v",
		cfg.Enabled, path, len(cfg.AppModelRules), len(cfg.AppIDRules), len(cfg.HeaderRules), cfg.TokenThreshold.Enabled)

	if !cfg.Enabled {
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}

	pathMatched := config.PathMatchesSuffix(path, cfg.EnableOnPathSuffix)
	if !pathMatched {
		log.Debugf("path not matched, skip routing: path=%q", path)
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}

	appID := matcher.GetAppID(ctx, config.DefaultAppIDHeader)
	if len(cfg.AppModelRules) > 0 && ctx.HasRequestBody() && appID != "" {
		log.Debugf("app_model rules configured, defer to body phase: appID=%q", appID)
		ctx.SetContext(config.CtxKeyPendingRouteReason, config.RouteReasonAppModelRule)
		proxywasm.RemoveHttpRequestHeader("content-length")
		ctx.SetRequestBodyBufferLimit(config.DefaultMaxBodyBytes)
		return types.HeaderStopIteration
	}

	if matched, routeCfg, reason := matcher.MatchAppIDRules(ctx, cfg.AppIDRules); matched {
		if routeCfg.RewriteBodyModel && ctx.HasRequestBody() {
			log.Debugf("defer local routing to body phase for model rewrite")
			ctx.SetContext(config.CtxKeyPendingRouteTarget, routeCfg)
			ctx.SetContext(config.CtxKeyPendingRouteReason, reason)
			proxywasm.RemoveHttpRequestHeader("content-length")
			ctx.SetRequestBodyBufferLimit(config.DefaultMaxBodyBytes)
			return types.HeaderStopIteration
		}
		router.ApplyRouting(ctx, *routeCfg, nil, reason, "local")
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}

	if matched, reason := matcher.MatchHeaderRules(ctx, cfg.HeaderRules); matched {
		if cfg.ExternalRouting.Provider == "" || cfg.ExternalRouting.Model == "" {
			log.Warnf("externalRouting config is missing provider/model, skipping external routing")
			ctx.DontReadRequestBody()
			return types.ActionContinue
		}

		if cfg.ExternalRouting.RewriteBodyModel && ctx.HasRequestBody() {
			log.Debugf("defer external routing to body phase for model rewrite")
			ctx.SetContext(config.CtxKeyPendingRouteTarget, cfg.ExternalRouting)
			ctx.SetContext(config.CtxKeyPendingRouteReason, reason)
			proxywasm.RemoveHttpRequestHeader("content-length")
			ctx.SetRequestBodyBufferLimit(config.DefaultMaxBodyBytes)
			return types.HeaderStopIteration
		}
		router.ApplyRouting(ctx, cfg.ExternalRouting, nil, reason, "external")
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}

	if !cfg.TokenThreshold.Enabled {
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}
	if !ctx.HasRequestBody() {
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}

	log.Debugf("wait for request body to evaluate token threshold, mode=%q threshold=%d",
		cfg.TokenThreshold.Mode, cfg.TokenThreshold.Threshold)
	proxywasm.RemoveHttpRequestHeader("content-length")
	ctx.SetRequestBodyBufferLimit(config.DefaultMaxBodyBytes)
	return types.HeaderStopIteration
}

// onHttpRequestBody 在 body 就绪后完成路由：优先 app_id+原模型，再处理 app_id/header 延迟路由，最后按 token 阈值决策。
func onHttpRequestBody(ctx wrapper.HttpContext, cfg config.PluginConfig, body []byte) types.Action {
	log.Debugf("request body phase: enabled=%v bodyLen=%d", cfg.Enabled, len(body))
	if !cfg.Enabled {
		return types.ActionContinue
	}

	if len(cfg.AppModelRules) > 0 {
		appID := matcher.GetAppID(ctx, config.DefaultAppIDHeader)
		sourceModel := token.ExtractSourceModel(body, "model")
		log.Debugf("checking app_model rules: appID=%q sourceModel=%q", appID, sourceModel)
		if matched, routeCfg, reason := matcher.MatchAppModelRules(appID, sourceModel, cfg.AppModelRules); matched {
			router.ApplyRouting(ctx, *routeCfg, body, reason, "local")
			return types.ActionContinue
		}
	}

	if pending := ctx.GetContext(config.CtxKeyPendingRouteTarget); pending != nil {
		routeCfg, ok := pending.(config.RouteTargetConfig)
		if !ok {
			if routeCfgPtr, ok := pending.(*config.RouteTargetConfig); ok && routeCfgPtr != nil {
				routeCfg = *routeCfgPtr
			} else {
				log.Errorf("unexpected pending route target type: %T", pending)
				return types.ActionContinue
			}
		}
		reason := ctx.GetStringContext(config.CtxKeyPendingRouteReason, "")
		target := "external"
		if reason == config.RouteReasonAppIDRule || reason == config.RouteReasonAppModelRule {
			target = "local"
		}
		router.ApplyRouting(ctx, routeCfg, body, reason, target)
		return types.ActionContinue
	}

	if !cfg.TokenThreshold.Enabled {
		return types.ActionContinue
	}

	switch cfg.TokenThreshold.Mode {
	case config.TokenModeHeuristic:
		tokenCount := token.EstimateInputTokensHeuristic(body, cfg.TokenThreshold.Heuristic.CharsPerToken)
		log.Debugf("heuristic token estimate: %d, threshold: %d", tokenCount, cfg.TokenThreshold.Threshold)
		if tokenCount > cfg.TokenThreshold.Threshold {
			if cfg.ExternalRouting.Provider == "" || cfg.ExternalRouting.Model == "" {
				log.Warnf("externalRouting config is missing provider/model, skipping token threshold routing")
				return types.ActionContinue
			}
			router.ApplyRouting(ctx, cfg.ExternalRouting, body, config.RouteReasonTokenThreshold, "external")
		}
		return types.ActionContinue
	case config.TokenModeTokenize:
		if cfg.ExternalRouting.Provider == "" || cfg.ExternalRouting.Model == "" {
			log.Warnf("externalRouting config is missing provider/model, skipping tokenize threshold routing")
			return types.ActionContinue
		}
		return token.HandleTokenizeRequest(ctx, cfg, body)
	default:
		return types.ActionContinue
	}
}
