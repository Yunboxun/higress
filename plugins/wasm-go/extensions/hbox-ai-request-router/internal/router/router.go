// router 负责设置 Higress 路由 Header，并可选改写请求 body 中的 model 字段。
package router

import (
	"encoding/json"
	"errors"

	"hbox-ai-request-router/internal/config"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/sjson"
)

// ApplyRouting 设置路由 Header，并可选改写 body 中的 model 字段。
// 需改写 body 时先完成改写再写 Header，任一环节失败则放弃路由，避免 Header 与 body 不一致。
func ApplyRouting(ctx wrapper.HttpContext, route config.RouteTargetConfig, body []byte, reason string, target string) {
	log.Debugf("apply routing: target=%s reason=%s providerHeader=%s provider=%s modelHeader=%s model=%s rewriteBodyModel=%v bodyLen=%d",
		target, reason, route.ProviderHeader, route.Provider, route.ModelHeader, route.Model, route.RewriteBodyModel, len(body))

	if route.RewriteBodyModel && len(body) > 0 {
		newBody, err := rewriteRequestModel(body, route.ModelKey, route.Model)
		if err != nil {
			log.Errorf("abort %s routing: %v", target, err)
			return
		}
		if err := proxywasm.ReplaceHttpRequestBody(newBody); err != nil {
			log.Errorf("abort %s routing: failed to replace request body: %v", target, err)
			return
		}
		log.Debugf("request body model rewritten: modelKey=%s model=%s newBodyLen=%d", route.ModelKey, route.Model, len(newBody))
	} else {
		log.Debugf("skip body model rewrite: rewriteBodyModel=%v bodyLen=%d jsonValid=%v",
			route.RewriteBodyModel, len(body), len(body) > 0 && json.Valid(body))
	}

	if err := proxywasm.ReplaceHttpRequestHeader(route.ProviderHeader, route.Provider); err != nil {
		log.Errorf("abort %s routing: failed to set provider header %s: %v", target, route.ProviderHeader, err)
		return
	}
	if err := proxywasm.ReplaceHttpRequestHeader(route.ModelHeader, route.Model); err != nil {
		log.Errorf("abort %s routing: failed to set model header %s: %v", target, route.ModelHeader, err)
		return
	}
	log.Debugf("routing headers set: target=%s %s=%s %s=%s", target, route.ProviderHeader, route.Provider, route.ModelHeader, route.Model)

	ctx.SetUserAttribute("route_target", target)
	ctx.SetUserAttribute("route_reason", reason)
	ctx.SetUserAttribute("target_provider", route.Provider)
	ctx.SetUserAttribute("target_model", route.Model)
	if target == "external" {
		ctx.SetUserAttribute("external_provider", route.Provider)
		ctx.SetUserAttribute("external_model", route.Model)
	} else if target == "local" {
		ctx.SetUserAttribute("local_provider", route.Provider)
		ctx.SetUserAttribute("local_model", route.Model)
	}
	ctx.WriteUserAttributeToLog()

	log.Debugf("route to %s model, reason=%s provider=%s model=%s", target, reason, route.Provider, route.Model)
}

// ApplyExternalRouting 兼容旧调用，等价于外网路由。
func ApplyExternalRouting(ctx wrapper.HttpContext, cfg config.PluginConfig, body []byte, reason string) {
	ApplyRouting(ctx, cfg.ExternalRouting, body, reason, "external")
}

// rewriteRequestModel 改写 JSON body 中的 model 字段。
func rewriteRequestModel(body []byte, modelKey, model string) ([]byte, error) {
	if !json.Valid(body) {
		return nil, errors.New("body is not valid JSON, cannot rewrite model")
	}
	return sjson.SetBytes(body, modelKey, model)
}
