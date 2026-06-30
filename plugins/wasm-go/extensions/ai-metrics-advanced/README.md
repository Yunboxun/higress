---
title: AI 高级指标监控
keywords: [higress, AI, metrics, P95, histogram, queue]
description: AI 高级指标监控插件，提供首字延迟 Histogram（支持 histogram_quantile 计算 P95）和基于阈值的模型排队监控
---

## 介绍

`ai-metrics-advanced` 插件在网关侧自动计算以下高级 AI 推理指标，**无需后端推理引擎做任何改造**：

| 指标类型 | 说明 |
|---------|------|
| **首字延迟 Histogram** | 使用 Envoy 原生 Histogram，可直接用 `histogram_quantile` 计算 P95/P99 |
| **排队时间 Histogram** | 超出阈值部分的等待时间分布 |
| **排队请求计数** | 被判定为排队的请求总数 |
| **排队请求数 Gauge** | 当前排队中的请求数 |
| **服务总时长 Histogram** | 完整请求生命周期的时长分布 |

### 排队判断逻辑

通过**首字延迟阈值**在网关侧判断请求是否处于排队状态：

```
首字延迟 > queue_threshold_ms  →  视为排队
排队时间 = 首字延迟 - queue_threshold_ms
```

## 配置说明

| 名称 | 数据类型 | 默认值 | 描述 |
|------|---------|--------|------|
| `enable_first_token_histogram` | bool | true | 是否启用首字延迟 Histogram |
| `enable_queue_metrics` | bool | true | 是否启用排队监控 |
| `queue_threshold_ms` | int | 500 | 排队判断阈值（毫秒） |
| `enable_path_suffixes` | []string | ["/completions","/messages","/generateContent"] | 路径过滤 |

## 指标说明

所有 Histogram 指标使用 Envoy 原生 Histogram 类型，Prometheus 中呈现为标准格式：

```
# 首字延迟
route_upstream_model_consumer_metric_llm_first_token_duration_ms_bucket{..., le="..."} count
route_upstream_model_consumer_metric_llm_first_token_duration_ms_sum{...} total
route_upstream_model_consumer_metric_llm_first_token_duration_ms_count{...} total

# 排队时间
route_upstream_model_consumer_metric_llm_queue_time_ms_bucket{..., le="..."} count
route_upstream_model_consumer_metric_llm_queue_time_ms_sum{...} total
route_upstream_model_consumer_metric_llm_queue_time_ms_count{...} total

# 排队请求总数 (Counter)
route_upstream_model_consumer_metric_llm_queue_requests_total{...} count

# 当前排队数 (Gauge)
route_upstream_model_consumer_metric_llm_queue_pending{...} count

# 服务总时长
route_upstream_model_consumer_metric_llm_service_duration_ms_bucket{..., le="..."} count
route_upstream_model_consumer_metric_llm_service_duration_ms_sum{...} total
route_upstream_model_consumer_metric_llm_service_duration_ms_count{...} total
```

## Prometheus 查询示例

### 首字延迟 P95

```promql
histogram_quantile(0.95,
  rate(route_upstream_model_consumer_metric_llm_first_token_duration_ms_bucket[5m])
)
```

### 首字延迟 P99

```promql
histogram_quantile(0.99,
  rate(route_upstream_model_consumer_metric_llm_first_token_duration_ms_bucket[5m])
)
```

### 首字延迟平均值

```promql
rate(route_upstream_model_consumer_metric_llm_first_token_duration_ms_sum[5m])
/
rate(route_upstream_model_consumer_metric_llm_first_token_duration_ms_count[5m])
```

### 按模型查询首字延迟 P95

```promql
histogram_quantile(0.95,
  rate(route_upstream_model_consumer_metric_llm_first_token_duration_ms_bucket{ai_model=~"Qwen3-235B.*"}[5m])
)
```

### 排队请求占比

```promql
rate(route_upstream_model_consumer_metric_llm_queue_requests_total[5m])
/
rate(route_upstream_model_consumer_metric_llm_first_token_duration_ms_count[5m])
```

### 排队时间 P95

```promql
histogram_quantile(0.95,
  rate(route_upstream_model_consumer_metric_llm_queue_time_ms_bucket[5m])
)
```

### 平均排队时间

```promql
rate(route_upstream_model_consumer_metric_llm_queue_time_ms_sum[5m])
/
rate(route_upstream_model_consumer_metric_llm_queue_time_ms_count[5m])
```

### 当前排队请求数

```promql
route_upstream_model_consumer_metric_llm_queue_pending
```

### 服务总时长 P95

```promql
histogram_quantile(0.95,
  rate(route_upstream_model_consumer_metric_llm_service_duration_ms_bucket[5m])
)
```

### 平均服务总时长

```promql
rate(route_upstream_model_consumer_metric_llm_service_duration_ms_sum[5m])
/
rate(route_upstream_model_consumer_metric_llm_service_duration_ms_count[5m])
```

## 配置示例

### 默认配置

```yaml
# 空配置即可，使用所有默认值
```

### 自定义排队阈值

```yaml
enable_queue_metrics: true
queue_threshold_ms: 1000
```

### 完整配置

```yaml
enable_first_token_histogram: true
enable_queue_metrics: true
queue_threshold_ms: 5000
enable_path_suffixes:
  - "/v1/chat/completions"
  - "/v1/completions"
```
