## v1.0.0

**发布日期**：2026-06-16

首个正式版本。Higress WASM 插件，在 AI 网关层按 **Header 规则** 或 **输入 token 阈值** 将请求重路由到外网模型；不直接发起 HTTP 转发，而是通过设置 `x-higress-llm-provider` / `x-higress-llm-model` 等 Header（并可改写 body 中的 `model` 字段），由 Higress 既有 **AI 路由 + ai-proxy** 链路完成外网调用。

### 功能特性

- **路径过滤**：仅对 AI 相关路径生效，默认后缀包括 `/completions`、`/messages`、`/embeddings`、`/responses` 等；支持 `*` 匹配全部路径
- **Header 规则路由**：按 `headerRules` 匹配请求 Header、Query 参数或 `Authorization` Bearer token，支持 `exact` / `prefix` / `regexp` 三种匹配方式
- **Token 阈值路由**：当输入 token 超过阈值时自动切换外网模型
  - `heuristic` 模式：本地从 body 提取 `messages` / `input` / `prompt` / `system` / `tools` 等文本，按字符数估算 token
  - `tokenize` 模式：异步调用集群内 tokenize 服务，根据响应 token 数决策
- **外网路由 Header 注入**：设置 `x-higress-llm-provider`、`x-higress-llm-model`（名称可配置）
- **Body model 改写**：可选将 JSON body 中的 `model` 字段改写为外网模型名；改写失败时放弃外网路由，避免 Header 与 body 不一致
- **审计日志**：命中外网路由时写入 `route_target`、`route_reason`、`external_provider`、`external_model` 等自定义属性
- **差异化配置**：支持 `WasmPlugin` 的 `defaultConfig` 与 `matchRules`（按 Ingress / 域名）

### 路由决策优先级

1. **Header 规则命中** → 外网路由（`route_reason=header_rule`）
2. **Token 阈值超限**（`tokenThreshold.enabled=true` 且未命中 header 规则）→ 外网路由（`route_reason=token_threshold`）
3. **均未命中** → 放行，保持网关原有内网路由

### 网关前置条件

部署本插件前，需在 Higress 侧完成：

1. 在 **McpBridge** 中注册外网 DNS 服务（如 `openai-external.dns`）
2. 创建 **Ingress / AI 路由**，通过 `higress.io/match-header-x-higress-llm-provider` 注解匹配外网 provider
3. 该路由挂载 **ai-proxy** WasmPlugin，由 ai-proxy 完成对外网 API 的实际调用

### 配置要点

| 配置项                               | 说明                                       |
| ------------------------------------ | ------------------------------------------ |
| `externalRouting.provider` / `model` | **必填**，外网 Provider 与模型名           |
| `externalRouting.rewriteBodyModel`   | 默认 `true`，是否改写 body 中的 model 字段 |
| `tokenThreshold.enabled`             | 默认 `false`，启用后 `threshold` 须 > 0    |
| `tokenThreshold.mode`                | `heuristic`（默认）或 `tokenize`           |
| `headerRules[]`                      | 规则按数组顺序评估，任一命中即触发外网路由 |

### 构建与部署

- 要求 **Go 1.24+**（`go.mod` 为 Go 1.25）
- 构建 WASM：`make build`
- 构建并推送多架构 OCI 镜像：`make build-image-multiarch IMAGE_TAG=v1.0.0`
- 建议 `WasmPlugin` 优先级：`520`

```bash
# 示例
make build-image-multiarch IMAGE_TAG=v1.0.0
```

### 已知限制

- 插件不直接转发请求，依赖网关侧 McpBridge + Ingress + ai-proxy 链路
- `heuristic` 模式为字符数估算，精度取决于 `charsPerToken` 配置
- `tokenize` 服务调用失败时保持原路由，并记录 `tokenize_status=failed`
- body 改写要求请求体为合法 JSON；非 JSON body 且 `rewriteBodyModel=true` 时会放弃外网路由
- 请求 body 缓冲上限为 100 MiB
