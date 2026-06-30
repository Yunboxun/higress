package main

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

func main() {}

func init() {
	wrapper.SetCtx(
		"ai-metrics-advanced",
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		wrapper.ProcessStreamingResponseBody(onHttpStreamingBody),
		wrapper.ProcessResponseBody(onHttpResponseBody),
	)
}

// ============================
// 常量定义
// ============================
const (
	// Context keys
	CtxRequestStartTime = "adv-request-start-time"
	CtxFirstTokenTime   = "adv-first-token-time"
	CtxRouteName        = "adv-route"
	CtxClusterName      = "adv-cluster"
	CtxModelName        = "adv-model"
	CtxConsumer         = "adv-consumer"
	CtxSkipProcessing   = "adv-skip-processing"
	CtxInQueue          = "adv-in-queue"
	CtxIsStream         = "adv-is-stream"

	// Consumer header
	ConsumerHeader = "x-mse-consumer"

	// 共享数据 key 前缀，用于跨请求维护排队计数
	SharedDataQueueCountPrefix = "adv-queue-count:"

	// 默认排队阈值（毫秒）：超过此时间未收到首字节，视为排队
	DefaultQueueThresholdMs int64 = 500
)

// ============================
// 配置结构
// ============================

// AdvancedMetricsConfig 插件配置
type AdvancedMetricsConfig struct {
	// 是否启用首字延迟 Histogram 统计（默认 true）
	EnableFirstTokenHistogram bool `json:"enable_first_token_histogram"`
	// 是否启用排队监控（默认 true）
	EnableQueueMetrics bool `json:"enable_queue_metrics"`

	// 排队阈值（毫秒）：请求超过此时间未收到首字节，视为"排队"
	QueueThresholdMs int64 `json:"queue_threshold_ms"`

	// 路径过滤
	EnablePathSuffixes []string `json:"enable_path_suffixes"`

	// Metrics storage（WASM 单线程模型无需加锁）
	counterMetrics   map[string]proxywasm.MetricCounter
	gaugeMetrics     map[string]proxywasm.MetricGauge
	histogramMetrics map[string]proxywasm.MetricHistogram
}

// ============================
// 辅助函数
// ============================

func generateMetricName(route, cluster, model, consumer, metricName string) string {
	return fmt.Sprintf("route.%s.upstream.%s.model.%s.consumer.%s.metric.%s",
		route, cluster, model, consumer, metricName)
}

func getRouteName() string {
	if raw, err := proxywasm.GetProperty([]string{"route_name"}); err == nil {
		return string(raw)
	}
	return "-"
}

func getClusterName() string {
	if raw, err := proxywasm.GetProperty([]string{"cluster_name"}); err == nil {
		return string(raw)
	}
	return "-"
}

func isPathEnabled(requestPath string, enabledSuffixes []string) bool {
	if len(enabledSuffixes) == 0 {
		return true
	}
	pathWithoutQuery := requestPath
	if queryPos := strings.Index(requestPath, "?"); queryPos != -1 {
		pathWithoutQuery = requestPath[:queryPos]
	}
	for _, suffix := range enabledSuffixes {
		if suffix == "*" {
			return true
		}
		if strings.HasSuffix(pathWithoutQuery, suffix) {
			return true
		}
	}
	return false
}

// ============================
// Metric 操作
// ============================

func (config *AdvancedMetricsConfig) incrementCounter(metricName string, inc uint64) {
	if inc == 0 {
		return
	}
	counter, ok := config.counterMetrics[metricName]
	if !ok {
		counter = proxywasm.DefineCounterMetric(metricName)
		config.counterMetrics[metricName] = counter
	}
	counter.Increment(inc)
}

func (config *AdvancedMetricsConfig) setGauge(metricName string, value int64) {
	gauge, ok := config.gaugeMetrics[metricName]
	if !ok {
		gauge = proxywasm.DefineGaugeMetric(metricName)
		config.gaugeMetrics[metricName] = gauge
	}
	current := gauge.Value()
	gauge.Add(value - current)
}

func (config *AdvancedMetricsConfig) recordHistogram(metricName string, value uint64) {
	histogram, ok := config.histogramMetrics[metricName]
	if !ok {
		histogram = proxywasm.DefineHistogramMetric(metricName)
		config.histogramMetrics[metricName] = histogram
	}
	histogram.Record(value)
}

// ============================
// 共享数据操作（跨请求计数器，用于排队数量统计）
// ============================

func incrementSharedCounter(key string, delta int64) int64 {
	for retries := 0; retries < 10; retries++ {
		data, cas, err := proxywasm.GetSharedData(key)
		var current int64
		if err == nil && len(data) == 8 {
			current = int64(binary.LittleEndian.Uint64(data))
		}
		newVal := current + delta
		if newVal < 0 {
			newVal = 0
		}
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, uint64(newVal))
		if setErr := proxywasm.SetSharedData(key, buf, cas); setErr == nil {
			return newVal
		}
	}
	return 0
}

// ============================
// 配置解析
// ============================

func parseConfig(configJson gjson.Result, config *AdvancedMetricsConfig) error {
	config.counterMetrics = make(map[string]proxywasm.MetricCounter)
	config.gaugeMetrics = make(map[string]proxywasm.MetricGauge)
	config.histogramMetrics = make(map[string]proxywasm.MetricHistogram)

	// 默认启用首字延迟 Histogram
	config.EnableFirstTokenHistogram = true
	if configJson.Get("enable_first_token_histogram").Exists() {
		config.EnableFirstTokenHistogram = configJson.Get("enable_first_token_histogram").Bool()
	}

	// 默认启用排队监控
	config.EnableQueueMetrics = true
	if configJson.Get("enable_queue_metrics").Exists() {
		config.EnableQueueMetrics = configJson.Get("enable_queue_metrics").Bool()
	}

	// 排队阈值
	config.QueueThresholdMs = DefaultQueueThresholdMs
	if configJson.Get("queue_threshold_ms").Exists() {
		config.QueueThresholdMs = configJson.Get("queue_threshold_ms").Int()
	}

	// 路径过滤
	pathSuffixes := configJson.Get("enable_path_suffixes").Array()
	if len(pathSuffixes) == 0 {
		config.EnablePathSuffixes = []string{"/completions", "/messages", "/generateContent"}
	} else {
		config.EnablePathSuffixes = make([]string, 0, len(pathSuffixes))
		for _, suffix := range pathSuffixes {
			s := suffix.String()
			if s == "*" {
				config.EnablePathSuffixes = []string{}
				break
			}
			config.EnablePathSuffixes = append(config.EnablePathSuffixes, s)
		}
	}

	log.Infof("ai-metrics-advanced config: histogram=%v, queue=%v, queue_threshold=%dms, paths=%v",
		config.EnableFirstTokenHistogram, config.EnableQueueMetrics,
		config.QueueThresholdMs, config.EnablePathSuffixes)

	return nil
}

// ============================
// 请求处理
// ============================

func onHttpRequestHeaders(ctx wrapper.HttpContext, config AdvancedMetricsConfig) types.Action {
	requestPath, _ := proxywasm.GetHttpRequestHeader(":path")
	if !isPathEnabled(requestPath, config.EnablePathSuffixes) {
		ctx.SetContext(CtxSkipProcessing, true)
		ctx.DontReadRequestBody()
		ctx.DontReadResponseBody()
		return types.ActionContinue
	}

	ctx.DisableReroute()
	route := getRouteName()
	cluster := getClusterName()
	ctx.SetContext(CtxRouteName, route)
	ctx.SetContext(CtxClusterName, cluster)
	ctx.SetContext(CtxRequestStartTime, time.Now().UnixMilli())

	if consumer, _ := proxywasm.GetHttpRequestHeader(ConsumerHeader); consumer != "" {
		ctx.SetContext(CtxConsumer, consumer)
	}

	ctx.BufferRequestBody()

	return types.ActionContinue
}

// ============================
// 请求体处理
// ============================

func onHttpRequestBody(ctx wrapper.HttpContext, config AdvancedMetricsConfig, body []byte) types.Action {
	if ctx.GetBoolContext(CtxSkipProcessing, false) {
		return types.ActionContinue
	}

	if len(body) > 0 {
		if stream := gjson.GetBytes(body, "stream"); stream.Exists() && stream.Bool() {
			ctx.SetContext(CtxIsStream, true)
		}
	}

	return types.ActionContinue
}

// ============================
// 响应头处理
// ============================

func onHttpResponseHeaders(ctx wrapper.HttpContext, config AdvancedMetricsConfig) types.Action {
	if ctx.GetBoolContext(CtxSkipProcessing, false) {
		return types.ActionContinue
	}

	contentType, _ := proxywasm.GetHttpResponseHeader("content-type")
	if !strings.Contains(contentType, "text/event-stream") {
		ctx.BufferResponseBody()
	}

	if model, _ := proxywasm.GetHttpResponseHeader("x-model-name"); model != "" {
		ctx.SetContext(CtxModelName, model)
	}

	return types.ActionContinue
}

// ============================
// 流式响应处理
// ============================

func onHttpStreamingBody(ctx wrapper.HttpContext, config AdvancedMetricsConfig, data []byte, endOfStream bool) []byte {
	if ctx.GetBoolContext(CtxSkipProcessing, false) {
		return data
	}

	requestStartTime, ok := ctx.GetContext(CtxRequestStartTime).(int64)
	if !ok {
		return data
	}

	// 提取模型名
	if ctx.GetContext(CtxModelName) == nil {
		if model := wrapper.GetValueFromBody(data, []string{"model"}); model != nil {
			ctx.SetContext(CtxModelName, model.String())
		}
	}

	now := time.Now().UnixMilli()

	// 首字延迟（第一个 chunk 到达时记录）
	if ctx.GetContext(CtxFirstTokenTime) == nil {
		ctx.SetContext(CtxFirstTokenTime, now)
		firstTokenDuration := now - requestStartTime

		if ctx.GetBoolContext(CtxIsStream, false) {
			// 使用 Envoy 原生 Histogram，自动生成 _bucket{le=...}, _sum, _count
			if config.EnableFirstTokenHistogram {
				writeFirstTokenHistogram(ctx, &config, firstTokenDuration)
			}

			// 排队判断
			if config.EnableQueueMetrics {
				handleQueueOnFirstToken(ctx, &config, firstTokenDuration)
			}

			log.Debugf("ai-metrics-advanced: first_token_latency=%dms", firstTokenDuration)
		}
	}

	if endOfStream {
		writeServiceDuration(ctx, &config, requestStartTime, now)
	}

	return data
}

// ============================
// 非流式响应处理
// ============================

func onHttpResponseBody(ctx wrapper.HttpContext, config AdvancedMetricsConfig, body []byte) types.Action {
	if ctx.GetBoolContext(CtxSkipProcessing, false) {
		return types.ActionContinue
	}

	requestStartTime, _ := ctx.GetContext(CtxRequestStartTime).(int64)
	now := time.Now().UnixMilli()
	responseDuration := now - requestStartTime

	if ctx.GetContext(CtxModelName) == nil {
		if model := gjson.GetBytes(body, "model"); model.Exists() {
			ctx.SetContext(CtxModelName, model.String())
		}
	}

	if ctx.GetBoolContext(CtxIsStream, false) {
		// 非流式请求以总响应时间作为"首字延迟"
		if config.EnableFirstTokenHistogram {
			writeFirstTokenHistogram(ctx, &config, responseDuration)
		}

		if config.EnableQueueMetrics {
			handleQueueOnFirstToken(ctx, &config, responseDuration)
		}
	}

	writeServiceDuration(ctx, &config, requestStartTime, now)

	return types.ActionContinue
}

// ============================
// 核心指标写入
// ============================

// writeFirstTokenHistogram 使用 Envoy 原生 Histogram 记录首字延迟
// Envoy 会自动生成标准 Prometheus Histogram 格式：
//
//	{metric_name}_bucket{le="..."} count
//	{metric_name}_sum total
//	{metric_name}_count total
//
// 可直接使用 histogram_quantile(0.95, ...) 计算 P95
func writeFirstTokenHistogram(ctx wrapper.HttpContext, config *AdvancedMetricsConfig, firstTokenDuration int64) {
	route := ctx.GetStringContext(CtxRouteName, "-")
	cluster := ctx.GetStringContext(CtxClusterName, "-")
	model := ctx.GetStringContext(CtxModelName, "UNKNOWN")
	consumer := ctx.GetStringContext(CtxConsumer, "none")

	metricName := generateMetricName(route, cluster, model, consumer, "llm_first_token_duration_ms")
	config.recordHistogram(metricName, uint64(firstTokenDuration))
}

// handleQueueOnFirstToken 排队判断逻辑
// 首字延迟 > queue_threshold_ms → 视为排队
// 排队时间 = 首字延迟 - 阈值
func handleQueueOnFirstToken(ctx wrapper.HttpContext, config *AdvancedMetricsConfig, firstTokenDuration int64) {
	route := ctx.GetStringContext(CtxRouteName, "-")
	cluster := ctx.GetStringContext(CtxClusterName, "-")
	model := ctx.GetStringContext(CtxModelName, "UNKNOWN")
	consumer := ctx.GetStringContext(CtxConsumer, "none")

	if firstTokenDuration > config.QueueThresholdMs {
		queueTime := firstTokenDuration - config.QueueThresholdMs
		ctx.SetContext(CtxInQueue, true)

		// 排队时间也用 Histogram 记录（可用 histogram_quantile 计算 P95 排队时间）
		queueTimeMetric := generateMetricName(route, cluster, model, consumer, "llm_queue_time_ms")
		config.recordHistogram(queueTimeMetric, uint64(queueTime))

		// 排队请求数 counter（累加）
		config.incrementCounter(
			generateMetricName(route, cluster, model, consumer, "llm_queue_requests_total"),
			1,
		)

		// 排队数 Gauge（通过 shared data 实现跨请求计数）
		queueKey := SharedDataQueueCountPrefix + generateMetricName(route, cluster, model, consumer, "pending")
		newCount := incrementSharedCounter(queueKey, 0) // 当前排队数 (此时请求已离开队列)
		config.setGauge(
			generateMetricName(route, cluster, model, consumer, "llm_queue_pending"),
			newCount,
		)

		log.Debugf("ai-metrics-advanced: queued, queue_time=%dms (threshold=%dms, first_token=%dms)",
			queueTime, config.QueueThresholdMs, firstTokenDuration)
	}
}

// writeServiceDuration 使用 Histogram 记录服务总时长
func writeServiceDuration(ctx wrapper.HttpContext, config *AdvancedMetricsConfig, requestStartTime, responseEndTime int64) {
	route := ctx.GetStringContext(CtxRouteName, "-")
	cluster := ctx.GetStringContext(CtxClusterName, "-")
	model := ctx.GetStringContext(CtxModelName, "UNKNOWN")
	consumer := ctx.GetStringContext(CtxConsumer, "none")

	serviceDuration := responseEndTime - requestStartTime

	metricName := generateMetricName(route, cluster, model, consumer, "llm_service_duration_ms")
	config.recordHistogram(metricName, uint64(serviceDuration))

	log.Debugf("ai-metrics-advanced: route=%s model=%s service_duration=%dms",
		route, model, serviceDuration)
}
