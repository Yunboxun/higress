---
title: 流量拷贝 (Traffic Shadow)
keywords: [流量拷贝, 流量镜像, traffic shadow, traffic mirror]
description: 流量拷贝插件配置参考
---

## 功能说明

`traffic-shadow` 插件用于将请求流量异步拷贝（镜像）到指定的后端服务。这在进行线上流量回放、新版本灰度测试、日志审计等场景下非常有用。

拷贝的请求是异步发起的，不会阻塞主请求的执行，也不会影响主请求的响应。拷贝请求的响应会被忽略。

## 运行属性

插件执行阶段：`默认阶段`
插件执行优先级：`400`

## 配置字段

| 名称 | 数据类型 | 填写要求 | 默认值 | 描述 |
| --- | --- | --- | --- | --- |
| `serviceSource` | string | 必填 | - | 拷贝目标服务的来源，支持 `k8s`, `nacos`, `ip`, `dns` |
| `serviceName` | string | 必填 | - | 拷贝目标服务的名称 |
| `servicePort` | number | 必填 | - | 拷贝目标服务的端口 |
| `namespace` | string | 非必填 | `default` | 拷贝目标服务所在的命名空间（适用于 `k8s` 和 `nacos`） |
| `domain` | string | 非必填 | - | 拷贝目标服务的域名（适用于 `dns`） |
| `requestPath` | string | 非必填 | - | 拷贝请求的路径。如果不填，则使用原始请求的路径 |
| `percentage` | number | 非必填 | `100` | 流量拷贝的采样率，范围 0-100。例如 50 表示拷贝 50% 的流量 |
| `headersToAdd` | map of string | 非必填 | - | 拷贝请求中需要额外添加的 Header |
| `withBody` | boolean | 非必填 | `false` | 是否拷贝请求 Body。注意：开启此选项会增加内存消耗 |
| `maxBodyBytes` | number | 非必填 | `1048576` | 允许拷贝的最大 Body 大小（字节），默认 1MB。仅在 `withBody` 为 true 时生效 |
| `modelOverride` | string | 非必填 | - | 仅在 `withBody` 为 true 且请求体为 JSON 时生效。用于强制覆盖拷贝请求体中的 `model` 字段值 |

## 配置示例

### 示例 1：拷贝 100% 流量到 K8s 服务（不带 Body）

```yaml
serviceSource: k8s
serviceName: my-shadow-service
servicePort: 8080
namespace: default
```

### 示例 2：拷贝 50% 流量到外部大模型 API (DNS 域名)，带 Body，指定模型并添加鉴权 Header

```yaml
serviceSource: dns
serviceName: ai-qihoo-net
servicePort: 80
domain: ai.qihoo.net
requestPath: "/v1/chat/completions"
percentage: 50
withBody: true
maxBodyBytes: 2097152 # 2MB
modelOverride: "Minimax-M2.7"
headersToAdd:
  Authorization: "Bearer 30ee6ffe-7944-4f07-90f9-9ef91a4566ea"
```

### 示例 3：拷贝流量到指定 IP，并重写请求路径

```yaml
serviceSource: ip
serviceName: shadow-backend
servicePort: 9090
requestPath: "/api/v1/shadow-receive"
```

## 注意事项

1. 拷贝请求会自动添加 `x-higress-shadow: true` Header，后端服务可以通过此 Header 识别镜像流量。
2. 开启 `withBody: true` 会导致网关缓存请求 Body，对于大文件上传等场景，请谨慎开启或合理设置 `maxBodyBytes`，以免造成网关 OOM。
3. 拷贝请求的超时时间固定为 5 秒。
