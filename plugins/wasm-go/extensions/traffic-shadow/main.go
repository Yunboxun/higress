// Copyright (c) 2024 Alibaba Group Holding Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/higress-group/wasm-go/pkg/wrapper"
)

func main() {}

func init() {
	wrapper.SetCtx(
		"traffic-shadow",
		wrapper.ParseConfigBy(parseConfig),
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
		wrapper.ProcessRequestBodyBy(onHttpRequestBody),
	)
}

type TrafficShadowConfig struct {
	client        wrapper.HttpClient
	percentage    float64
	headersToAdd  map[string]string
	withBody      bool
	maxBodyBytes  uint32
	requestPath   string
	modelOverride string
	shadowTimeout uint32
}

func parseConfig(json gjson.Result, config *TrafficShadowConfig, log log.Log) error {
	serviceSource := json.Get("serviceSource").String()
	serviceName := json.Get("serviceName").String()
	servicePort := json.Get("servicePort").Int()

	if serviceName == "" || servicePort == 0 {
		return errors.New("invalid service config: serviceName and servicePort are required")
	}

	config.requestPath = json.Get("requestPath").String()

	percentage := json.Get("percentage").Float()
	if percentage < 0 || percentage > 100 {
		return errors.New("invalid percentage, should be between 0 and 100")
	}
	if percentage == 0 && !json.Get("percentage").Exists() {
		config.percentage = 100
	} else {
		config.percentage = percentage
	}

	config.headersToAdd = make(map[string]string)
	for k, v := range json.Get("headersToAdd").Map() {
		config.headersToAdd[strings.ToLower(k)] = v.String()
	}

	config.withBody = json.Get("withBody").Bool()

	config.modelOverride = json.Get("modelOverride").String()

	maxBodyBytes := json.Get("maxBodyBytes").Uint()
	if maxBodyBytes == 0 {
		config.maxBodyBytes = 1024 * 1024
	} else {
		config.maxBodyBytes = uint32(maxBodyBytes)
	}

	shadowTimeout := json.Get("shadowTimeout").Uint()
	if shadowTimeout == 0 {
		config.shadowTimeout = 600000 // 默认 600 秒，模型推理可能需要较长时间
	} else {
		config.shadowTimeout = uint32(shadowTimeout)
	}

	switch serviceSource {
	case "k8s":
		namespace := json.Get("namespace").String()
		if namespace == "" {
			namespace = "default"
		}
		config.client = wrapper.NewClusterClient(wrapper.K8sCluster{
			ServiceName: serviceName,
			Namespace:   namespace,
			Port:        servicePort,
		})
		return nil
	case "nacos":
		namespace := json.Get("namespace").String()
		config.client = wrapper.NewClusterClient(wrapper.NacosCluster{
			ServiceName: serviceName,
			NamespaceID: namespace,
			Port:        servicePort,
		})
		return nil
	case "ip":
		config.client = wrapper.NewClusterClient(wrapper.StaticIpCluster{
			ServiceName: serviceName,
			Port:        servicePort,
		})
		return nil
	case "dns":
		domain := json.Get("domain").String()
		if domain == "" {
			return errors.New("domain is required for dns serviceSource")
		}
		config.client = wrapper.NewClusterClient(wrapper.DnsCluster{
			ServiceName: serviceName,
			Port:        servicePort,
			Domain:      domain,
		})
		return nil
	default:
		return errors.New("unknown service source: " + serviceSource)
	}
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config TrafficShadowConfig, log log.Log) types.Action {
	if config.percentage < 100 {
		if float64(rand.Intn(10000))/100.0 >= config.percentage {
			ctx.SetContext("shadow_skip", true)
			return types.ActionContinue
		}
	}

	if config.withBody && wrapper.HasRequestBody() {
		ctx.SetRequestBodyBufferLimit(config.maxBodyBytes)
		return types.ActionContinue
	}

	doShadowCall(ctx, config, nil, log)
	// 标记已在 header 阶段完成 shadow call，防止 body 阶段重复发送
	ctx.SetContext("shadow_done", true)
	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config TrafficShadowConfig, body []byte, log log.Log) types.Action {
	skip, _ := ctx.GetContext("shadow_skip").(bool)
	if skip {
		return types.ActionContinue
	}

	// 如果已在 header 阶段完成 shadow call，不再重复发送
	done, _ := ctx.GetContext("shadow_done").(bool)
	if done {
		return types.ActionContinue
	}

	if config.withBody {
		if config.modelOverride != "" {
			if gjson.ValidBytes(body) && gjson.GetBytes(body, "model").Exists() {
				if newBody, err := sjson.SetBytes(body, "model", config.modelOverride); err == nil {
					body = newBody
				} else {
					log.Errorf("failed to override model in body: %v", err)
				}
			}
		}

		doShadowCall(ctx, config, body, log)
	}

	return types.ActionContinue
}

func doShadowCall(ctx wrapper.HttpContext, config TrafficShadowConfig, body []byte, log log.Log) {
	headers, err := proxywasm.GetHttpRequestHeaders()
	if err != nil {
		log.Errorf("failed to get request headers: %v", err)
		return
	}

	// 构建 headersToAdd 的小写 key 集合，用于去重
	// 同时记录原始请求中哪些 header 需要被覆盖
	overrideKeys := make(map[string]string) // lowercase key -> actual key from headersToAdd
	for k, v := range config.headersToAdd {
		overrideKeys[strings.ToLower(k)] = v
	}

	// 需要强制删除的 header key（不区分大小写）
	forceRemoveKeys := make(map[string]bool)
	for k := range overrideKeys {
		forceRemoveKeys[k] = true
	}
	// 始终强制删除 authorization 相关的 header，确保不会透传原始鉴权
	forceRemoveKeys["authorization"] = true
	forceRemoveKeys["x-hi-original-auth"] = true

	// 白名单模式：只保留目标服务需要的业务 header
	// 除了白名单中的 header，其他一律不传
	allowedHeaders := map[string]bool{
		"content-type": true,
		"accept":       true,
		"user-agent":   true,
	}

	shadowHeaders := [][2]string{}
	for _, h := range headers {
		// 过滤所有伪 header（以 : 开头的）
		if len(h[0]) > 0 && h[0][0] == ':' {
			continue
		}
		// 强制删除需要覆盖的 header
		if forceRemoveKeys[strings.ToLower(h[0])] {
			continue
		}
		// 白名单模式：只保留允许的 header
		if !allowedHeaders[strings.ToLower(h[0])] {
			continue
		}
		shadowHeaders = append(shadowHeaders, [2]string{h[0], h[1]})
	}

	// 添加 headersToAdd 中的 header
	for k, v := range overrideKeys {
		shadowHeaders = append(shadowHeaders, [2]string{k, v})
	}

	// 标记为 shadow 流量
	shadowHeaders = append(shadowHeaders, [2]string{"x-higress-shadow", "true"})

	requestPath := config.requestPath
	if requestPath == "" {
		requestPath = ctx.Path()
	}

	method := ctx.Method()

	callErr := config.client.Call(
		method,
		requestPath,
		shadowHeaders,
		body,
		func(statusCode int, responseHeaders http.Header, responseBody []byte) {
			if statusCode == 0 {
				log.Warnf("shadow call timeout or cancelled: method=%s, path=%s, timeout=%dms",
					method, requestPath, config.shadowTimeout)
			} else {
				log.Infof("shadow call completed: status=%d, method=%s, path=%s", statusCode, method, requestPath)
			}
		},
		config.shadowTimeout,
	)

	if callErr != nil {
		log.Errorf("failed to dispatch shadow call: %v (method: %s, path: %s, headersLen: %d, bodyLen: %d)",
			callErr, method, requestPath, len(shadowHeaders), len(body))
	} else {
		log.Infof("shadow call dispatched: method=%s, path=%s, timeout=%dms", method, requestPath, config.shadowTimeout)
	}
}

func init() {
	rand.Seed(time.Now().UnixNano())
}
