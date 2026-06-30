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
	"net/http"
	"strings"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/tidwall/gjson"

	"github.com/higress-group/wasm-go/pkg/wrapper"
)

func main() {}

func init() {
	wrapper.SetCtx(
		"traffic-replay",
		wrapper.ParseConfigBy(parseConfig),
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
		wrapper.ProcessRequestBodyBy(onHttpRequestBody),
	)
}

type RecordedRequest struct {
	Method       string
	Path         string
	Headers      [][2]string
	Body         []byte
	RelativeTime time.Duration
}

type ReplayState struct {
	startTime       time.Time
	requests        []RecordedRequest
	isRecording     bool
	lastTickTime    time.Duration
	replayLoopStart time.Time
}

type TrafficReplayConfig struct {
	client         wrapper.HttpClient
	recordDuration time.Duration
	headersToAdd   map[string]string
	withBody       bool
	maxBodyBytes   uint32
	requestPath    string

	state *ReplayState
}

func parseConfig(json gjson.Result, config *TrafficReplayConfig, log log.Log) error {
	serviceSource := json.Get("serviceSource").String()
	serviceName := json.Get("serviceName").String()
	servicePort := json.Get("servicePort").Int()

	if serviceName == "" || servicePort == 0 {
		return errors.New("invalid service config: serviceName and servicePort are required")
	}

	config.requestPath = json.Get("requestPath").String()

	recordDurationInt := json.Get("recordDuration").Int()
	if recordDurationInt == 0 {
		recordDurationInt = 60
	}
	config.recordDuration = time.Duration(recordDurationInt) * time.Second

	config.headersToAdd = make(map[string]string)
	for k, v := range json.Get("headersToAdd").Map() {
		config.headersToAdd[strings.ToLower(k)] = v.String()
	}

	config.withBody = json.Get("withBody").Bool()

	maxBodyBytes := json.Get("maxBodyBytes").Uint()
	if maxBodyBytes == 0 {
		config.maxBodyBytes = 1024 * 1024
	} else {
		config.maxBodyBytes = uint32(maxBodyBytes)
	}

	config.state = &ReplayState{
		startTime:   time.Now(),
		isRecording: true,
		requests:    make([]RecordedRequest, 0),
	}

	wrapper.RegisterTickFunc(100, func() {
		state := config.state
		now := time.Now()

		if state.isRecording {
			if now.Sub(state.startTime) >= config.recordDuration {
				state.isRecording = false
				state.replayLoopStart = now
				state.lastTickTime = 0
				log.Infof("Finished recording, recorded %d requests. Starting replay.", len(state.requests))
			}
		} else {
			if len(state.requests) == 0 {
				return
			}

			elapsedInLoop := now.Sub(state.replayLoopStart)
			if elapsedInLoop >= config.recordDuration {
				state.replayLoopStart = now
				state.lastTickTime = 0
				elapsedInLoop = 0
			}

			for _, req := range state.requests {
				if req.RelativeTime >= state.lastTickTime && req.RelativeTime < elapsedInLoop {
					sendRequest(config, req, log)
				}
			}
			state.lastTickTime = elapsedInLoop
		}
	})

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

func onHttpRequestHeaders(ctx wrapper.HttpContext, config TrafficReplayConfig, log log.Log) types.Action {
	if !config.state.isRecording {
		return types.ActionContinue
	}

	if config.withBody && wrapper.HasRequestBody() {
		ctx.SetRequestBodyBufferLimit(config.maxBodyBytes)
		return types.ActionContinue
	}

	recordRequest(ctx, config, nil, log)
	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config TrafficReplayConfig, body []byte, log log.Log) types.Action {
	if !config.state.isRecording {
		return types.ActionContinue
	}

	if config.withBody {
		recordRequest(ctx, config, body, log)
	}

	return types.ActionContinue
}

func recordRequest(ctx wrapper.HttpContext, config TrafficReplayConfig, body []byte, log log.Log) {
	state := config.state
	if !state.isRecording {
		return
	}

	now := time.Now()
	relativeTime := now.Sub(state.startTime)
	if relativeTime >= config.recordDuration {
		state.isRecording = false
		return
	}

	headers, err := proxywasm.GetHttpRequestHeaders()
	if err != nil {
		log.Errorf("failed to get request headers: %v", err)
		return
	}

	overrideKeys := make(map[string]string)
	for k, v := range config.headersToAdd {
		overrideKeys[strings.ToLower(k)] = v
	}

	forceRemoveKeys := make(map[string]bool)
	for k := range overrideKeys {
		forceRemoveKeys[k] = true
	}
	forceRemoveKeys["authorization"] = true
	forceRemoveKeys["x-hi-original-auth"] = true

	allowedHeaders := map[string]bool{
		"content-type": true,
		"accept":       true,
		"user-agent":   true,
	}

	replayHeaders := [][2]string{}
	for _, h := range headers {
		if len(h[0]) > 0 && h[0][0] == ':' {
			continue
		}
		if forceRemoveKeys[strings.ToLower(h[0])] {
			continue
		}
		if !allowedHeaders[strings.ToLower(h[0])] {
			continue
		}
		replayHeaders = append(replayHeaders, [2]string{h[0], h[1]})
	}

	for k, v := range overrideKeys {
		replayHeaders = append(replayHeaders, [2]string{k, v})
	}

	replayHeaders = append(replayHeaders, [2]string{"x-higress-replay", "true"})

	var bodyCopy []byte
	if body != nil {
		bodyCopy = make([]byte, len(body))
		copy(bodyCopy, body)
	}

	req := RecordedRequest{
		Method:       ctx.Method(),
		Path:         ctx.Path(),
		Headers:      replayHeaders,
		Body:         bodyCopy,
		RelativeTime: relativeTime,
	}

	state.requests = append(state.requests, req)
	log.Debugf("Recorded request: %s %s at %v", req.Method, req.Path, req.RelativeTime)
}

func sendRequest(config *TrafficReplayConfig, req RecordedRequest, log log.Log) {
	requestPath := config.requestPath
	if requestPath == "" {
		requestPath = req.Path
	}

	callErr := config.client.Call(
		req.Method,
		requestPath,
		req.Headers,
		req.Body,
		func(statusCode int, responseHeaders http.Header, responseBody []byte) {
			log.Debugf("replay call completed: status=%d", statusCode)
		},
		5000,
	)

	if callErr != nil {
		log.Errorf("failed to make replay call: %v (method: %s, path: %s)", callErr, req.Method, requestPath)
	} else {
		log.Debugf("replay call dispatched successfully: method=%s, path=%s", req.Method, requestPath)
	}
}
