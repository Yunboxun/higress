# hbox-ai-request-router

Higress WASM 插件：在 AI 网关层按 **app_id + 原模型规则**、**app_id 规则**、**Header 规则** 或 **输入 token 阈值** 将请求重路由到本地模型或外网模型。

插件不直接发起 HTTP 转发，而是通过设置 `x-higress-llm-provider` / `x-higress-llm-model` 等 Header（并可改写 body 中的 `model` 字段），由 Higress 既有 **AI 路由 + ai-proxy** 链路将请求转发至目标 Provider。

## 适用场景

- 特定 `app_id` 下，按百分比将某个原始模型分流到不同本地模型
- 特定 `app_id` 的请求固定走本地其他模型
- 特定 API Key / Authorization 前缀的请求走外网模型
- 长上下文请求自动切换到外网大窗口模型

## 生效范围

这个插件本身既可以做成**全局生效**，也可以做成**局部生效**，最终取决于 `WasmPlugin` 的挂载方式：

- 使用 `defaultConfig` 且不加域名/Ingress 限制时，通常是较大范围生效
- 使用 `matchRules` 绑定某些域名、Ingress、路由时，就是局部生效

但无论全局还是局部，插件内部仍然只会处理 [`enableOnPathSuffix`](./internal/config/config.go:118) 命中的 AI 请求路径。

## 架构与工作原理

### 请求处理优先级

当前优先级（高 → 低）：

1. `appModelRules`：`app_id + 原 model` 联合命中，且满足流量百分比 → 路由到本地目标模型
2. `appIdRules`：仅 `app_id` 命中 → 路由到本地目标模型
3. `headerRules`：Header / Query / Authorization 命中 → 路由到外网模型
4. `tokenThreshold`：输入 token 超限 → 路由到外网模型
5. 都未命中 → 保持原始路由

### 生效阶段

插件工作在请求转发前，分两阶段：

| 阶段 | 作用 |
| --- | --- |
| Request Headers | 路径过滤、读取 `x-am-appid`、判断是否需要延迟到 Body 阶段 |
| Request Body | 提取原始 `model`，完成 `app_id + model` 联合匹配，并进行 `model` 改写与最终路由 |

因此：
- 仅 `app_id` 路由、Header 路由，可能在 Header 阶段直接完成
- `app_id + 原 model` 路由，通常在 Body 阶段完成，因为原始模型在 body 中

## 配置说明

### 顶层字段

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `enabled` | `boolean` | 否 | 是否启用插件 |
| `enableOnPathSuffix` | `string[]` | 否 | 仅对匹配的 AI API 路径生效 |
| `externalRouting` | `object` | 否 | 外网默认目标 |
| `localRouting` | `object` | 否 | 本地默认目标 |
| `appModelRules` | `object[]` | 否 | `app_id + 原 model` 命中后转到本地模型 |
| `appIdRules` | `object[]` | 否 | `app_id` 命中后转到本地模型 |
| `headerRules` | `object[]` | 否 | Header / Query / Authorization 命中后转到外网模型 |
| `tokenThreshold` | `object` | 否 | token 超阈值走外网 |

### RouteTarget 结构

`externalRouting`、`localRouting`、`appIdRules[].route`、`appModelRules[].route` 结构一致：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `provider` | `string` | **是** | — | 目标 Provider |
| `model` | `string` | **是** | — | 目标模型 |
| `providerHeader` | `string` | 否 | `x-higress-llm-provider` | provider 对应的 header |
| `modelHeader` | `string` | 否 | `x-higress-llm-model` | model 对应的 header |
| `rewriteBodyModel` | `boolean` | 否 | `true` | 是否改写 body 中的 `model` |
| `modelKey` | `string` | 否 | `model` | body 里模型字段路径 |

### appIdRules[]

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `header` | `string` | 否 | `x-am-appid` | app_id 所在 header |
| `values` | `string[]` | **是** | — | 命中的 app_id 列表 |
| `route` | `object` | **是** | — | 命中后路由目标 |

### appModelRules[]

用于支持**同时建立多组“appid + 原模型 → 目标模型”映射**，并支持百分比分流。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `header` | `string` | 否 | `x-am-appid` | app_id 所在 header |
| `appIds` | `string[]` | **是** | — | 命中的 app_id 列表 |
| `sourceModels` | `string[]` | **是** | — | 原始请求 body 中允许命中的模型列表 |
| `percentage` | `number` | 否 | `100.0` | 流量命中百分比，范围 0-100，不填则为 100% |
| `route` | `object` | **是** | — | 命中后路由到的本地 provider/model |

## 示例

### 同一个 app_id 下，多组原模型分流到不同目标模型

```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: hbox-ai-request-router
  namespace: higress-system
spec:
  priority: 520
  url: oci://<your_registry>/hbox-ai-request-router:1.0.0
  defaultConfig:
    enabled: true
    externalRouting:
      provider: openai
      model: gpt-4o
    appModelRules:
      - header: x-am-appid
        appIds:
          - app_001
        sourceModels:
          - deepseek-r1
          - deepseek-v3
        percentage: 50.5  # 50.5% 的流量走该规则
        route:
          provider: internal-a
          model: qwen-max
      - header: x-am-appid
        appIds:
          - app_001
        sourceModels:
          - gpt-4o
          - gpt-4o-mini
        # percentage 缺省，100% 流量命中该规则
        route:
          provider: internal-b
          model: qwen-plus
      - header: x-am-appid
        appIds:
          - app_002
        sourceModels:
          - claude-3-5-sonnet
        route:
          provider: internal-c
          model: deepseek-r1-distill
```

### 仅 app_id 命中后走本地模型

```yaml
enabled: true
externalRouting:
  provider: openai
  model: gpt-4o
appIdRules:
  - values:
      - app_177969757134590807615585
    route:
      provider: internal-gray
      model: qwen-max
```

## 路由原因（route_reason）

| 值 | 含义 |
| --- | --- |
| `app_model_rule` | 命中 `appModelRules` |
| `app_id_rule` | 命中 `appIdRules` |
| `header_rule` | 命中 `headerRules` |
| `token_threshold` | 命中 token 阈值 |

## 一句话总结

[`hbox-ai-request-router`](./) 现在已经支持**多组 `appid + 原 model -> 本地目标模型`** 的百分比分流配置，也保留了原有的 `app_id`、Header、Token 阈值路由能力。
