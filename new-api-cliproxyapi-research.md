# new-api → CLIProxyAPI 中转与远程 `image_url` 转换调研

> 调研日期：2026-11-08。仅做静态源码与官方文档审阅，没有运行两个上游项目，也没有对实际部署发请求。下文把“源码语法上接受”和“最终模型上游实际接受”严格区分。

## 1. 结论摘要

1. **直接中转配置**：new-api 使用 **OpenAI 渠道（类型 1）**，渠道 Base URL 填 `http://<cliproxyapi-host>:8317`，不要带 `/v1`。new-api 会把入站路径 `/v1/chat/completions` 直接接到 Base URL 后，并用 `Authorization: Bearer <渠道 Key>` 请求 CLIProxyAPI。渠道 Key 应是 CLIProxyAPI 的 `access.api-keys` 中的一项，不是 new-api 发给客户端的 Key。[new-api 渠道常量](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/constant/channel.go#L4-L6) · [URL 拼接](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/common/relay_utils.go#L25-L38) · [OpenAI 请求头](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/channel/openai/adaptor.go#L193-L260) · [CLIProxyAPI Key 配置](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/config.example.yaml#L132-L138)
2. **模型映射不是必需项**：客户端模型名与 CLIProxyAPI 注册的模型 ID 相同则留空；需要对外别名时才配置映射。CLIProxyAPI 按模型注册表查 provider，不是仅按 `gemini-*` 等字符串前缀猜路由。[new-api 映射实现](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/helper/model_mapped.go#L13-L63) · [CLIProxyAPI 模型查找](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/util/provider.go#L40-L80)
3. **远程 URL 的结果取决于 CPA 的目标转换路径，不能一概而论**：在当前的 OpenAI chat.completions → Gemini 转换中，CPA **不下载**远程 URL，而是丢弃该图片 part；同一 user turn 还有文本时会继续，若该 turn 因丢图而无内容则返回 400。Claude 路径转成 URL source，Codex 路径转成 `input_image` 并保留 URL，二者都不是由 CPA 本地抓取。[Gemini 路径](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/gemini/openai/chat-completions/gemini_openai_request.go#L157-L180) · [Claude 路径](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/claude/openai/chat-completions/claude_openai_request.go#L542-L570) · [Codex 路径](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/codex/openai/chat-completions/codex_openai_request.go#L237-L246)
4. **对 Gemini 路径，最小有效转换就是把远程 URL 的原始图片字节编成 `data:<MIME>;base64,<payload>`，仍放在原 `image_url.url` 中**。CPA 会把合格的 data URL 转为 Gemini `inlineData`。CPA 的公共解析器要求非空 MIME、显式 `base64` 标记和非空 payload，但不在本地校验 Base64 内容，也没有 MIME 白名单。[公共解析器](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/common/file_data.go#L10-L42) · [Gemini inlineData 构造](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/gemini/openai/chat-completions/gemini_openai_request.go#L546-L557)
5. **new-api 已有远程下载/Base64 能力，但类型 1 的 OpenAI→OpenAI 直通不会自动调用它**。已有能力在 OpenAI→Claude/Gemini 等协议转换中生效；当前已明确选择全链 OpenAI，因此不为复用这些代码而改协议范围。对选定的类型 1 直通链路，**独立 sidecar 是维护成本较低且不绑定任一 fork 的方案**。[new-api 媒体解析器注入](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/service/request_converter.go#L14-L25) · [远程加载](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/service/file_service.go#L157-L190) · [高级自定义转换入口](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/channel/advancedcustom/adaptor.go#L47-L67)
6. **现有配置覆盖和官方 JS 插件都不能独立完成这项动态转换**：new-api 参数覆盖、CPA `requests.payload` 都能改 JSON，但没有“下载 URL 并把响应字节编码为 Base64”的操作；`cpa-plugin-jshandler` 能改请求体，却只向 Goja 注入 `console`，没有 `fetch`/HTTP API，且 JS 出错默认保留原数据。[new-api 参数覆盖文档](https://docs.newapi.ai/zh/docs/guide/feature-guide/admin/channel#参数覆盖系统) · [CPA payload 配置](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/config.example.yaml#L280-L340) · [JS 引擎初始化](https://github.com/router-for-me/cpa-plugin-jshandler/blob/3e2252710a7585f7fb9c0f0d87fd86134d72876a/engine.go#L30-L63) · [JS 错误处理](https://github.com/router-for-me/cpa-plugin-jshandler/blob/3e2252710a7585f7fb9c0f0d87fd86134d72876a/interceptor.go#L64-L111)

## 2. 调研版本

| 仓库 | 默认分支 | 本次固定的 HEAD commit |
|---|---|---|
| `QuantumNous/new-api` | `main` | [`8faf1d45e8c101974dc7483324d3d91c8a462ffe`](https://github.com/QuantumNous/new-api/commit/8faf1d45e8c101974dc7483324d3d91c8a462ffe) |
| `router-for-me/CLIProxyAPI` | `main` | [`91e0dee827abf476f9f86246f68c500a112f7155`](https://github.com/router-for-me/CLIProxyAPI/commit/91e0dee827abf476f9f86246f68c500a112f7155) |
| `router-for-me/cpa-plugin-jshandler` | `main` | [`3e2252710a7585f7fb9c0f0d87fd86134d72876a`](https://github.com/router-for-me/cpa-plugin-jshandler/commit/3e2252710a7585f7fb9c0f0d87fd86134d72876a) |

默认分支由 GitHub 仓库元数据确认：[new-api](https://api.github.com/repos/QuantumNous/new-api) · [CLIProxyAPI](https://api.github.com/repos/router-for-me/CLIProxyAPI) · [jshandler](https://api.github.com/repos/router-for-me/cpa-plugin-jshandler)。除官方在线文档外，下文项目源码链接均固定到上述 SHA。

## 3. 准确调用链、Base URL、鉴权和模型名

### 3.1 直接 OpenAI 中转链路

```text
客户端
  POST http://<new-api>:3000/v1/chat/completions
  Authorization: Bearer <new-api 签发的客户端 Key>
        │
        ▼
new-api 选择渠道并应用模型映射
  渠道类型：OpenAI（1）
  Base URL：http://<cliproxyapi>:8317
  渠道 Key：<CLIProxyAPI access.api-keys 中的一项>
        │
        ▼
POST http://<cliproxyapi>:8317/v1/chat/completions
Authorization: Bearer <渠道 Key>
        │
        ▼
CLIProxyAPI AuthMiddleware
  → ChatCompletions handler
  → 模型注册表确定 provider/executor
  → 对应协议转换与上游调用
```

源码依据：

- new-api 的 `TextHelper` 依次初始化渠道元数据、执行 `ModelMappedHelper`、调用 adaptor 转换和 `DoRequest`；非 body 直通时还会执行参数覆盖。[`relay/compatible_handler.go`](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/compatible_handler.go#L25-L150)
- OpenAI 格式请求进入 OpenAI 渠道时，`GetRequestURL` 调用 `GetFullRequestURL(ChannelBaseUrl, RequestURLPath, ChannelType)`；后者通常只是 `baseURL + requestURL`。Cloudflare AI Gateway 的特例与本链路无关。[OpenAI `GetRequestURL`](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/channel/openai/adaptor.go#L175-L190) · [`GetFullRequestURL`](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/common/relay_utils.go#L25-L38)
- 因而 Base URL 若为 `http://host:8317/v1`，会得到 `/v1/v1/chat/completions`；末尾多一个 `/` 也没有通用清理逻辑。建议填纯源站 `http://host:8317`。
- CPA 的 `/v1` 路由组挂 `AuthMiddleware`，其中明确注册 `POST /chat/completions`。[`server_routes.go`](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/api/server_routes.go#L63-L81) · [`ChatCompletions`](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/sdk/api/handlers/openai/openai_handlers.go#L125-L156)

### 3.2 默认监听与 Key

CPA 当前 v8 示例配置为：

```yaml
server:
  host: ""       # 默认绑定全部 IPv4/IPv6 接口
  port: 8317
access:
  api-keys:
    - "替换为随机且私有的客户端 Key"
management:
  secret-key: "独立的管理 Key"
```

[`config.example.yaml` L30-L36、L113-L138](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/config.example.yaml#L30-L36) 给出了监听、代理访问 Key 和管理 Key；实际 HTTP 地址由 `host:port` 组成。[`server.go`](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/api/server.go#L248-L252) 代理访问 Key 不是上游 OAuth/厂商 Key，也不是管理 Key。示例模板 Key 应替换；当前版本另有示例 Key 安全模式。[安全模式](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/api/server_middleware.go#L385-L407)

配置 API key provider 后，CPA 可从 `Authorization: Bearer`、`X-Goog-Api-Key`、`X-Api-Key`、query `key` 或 `auth_token` 取凭据；本链路只需要第一种。[`provider.go`](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/access/config_access/provider.go#L60-L110) 注意：`AuthMiddleware` 在没有 access manager/provider 时保留 legacy 放行分支，所以不能把“未配置 Key 必然 401”当作安全边界；生产部署应显式配置 Key 并限制监听/网络访问。[`AuthMiddleware`](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/api/server_middleware.go#L480-L548)

new-api 不会把客户端原始 `Authorization` 原样透传到 OpenAI 渠道；adaptor 用所选渠道的 `ApiKey` 重新设置 Bearer。渠道 Header Override 可以另行覆盖它，但本场景不需要。[OpenAI `SetupRequestHeader`](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/channel/openai/adaptor.go#L193-L260) · [通用请求头处理](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/channel/api_request.go#L38-L90)

### 3.3 模型映射与 CPA 路由

- new-api 的模型映射是 JSON `map[string]string`，支持链式映射和环路检测；最终值写入 `UpstreamModelName` 并更新请求体模型。[`model_mapped.go`](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/helper/model_mapped.go#L13-L63) 同名直转时留空；渠道本身仍需启用相应模型。
- CPA 先解析 `auto`/thinking suffix，再通过模型注册表查 provider；查不到返回 400 `model_not_found`。[路由处理](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/sdk/api/handlers/handlers_routing.go#L160-L230)
- 注册表返回该模型 ID 对应的 provider 集合；凭证的 `prefix` 可注册带前缀模型名，`routing.force-model-prefix` 控制无前缀请求的凭证范围。这不是简单按模型名前缀字符串硬编码路由。[`GetModelProviders`](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/registry/model_registry.go#L1799-L1841) · [prefix 配置说明](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/config.example.yaml#L202-L213)

因此最省配置是客户端、new-api 渠道模型和 CPA 模型 ID 三者同名；只有需要别名或改名时才加模型映射。

### 3.4 当前选定方案的直接配置

按“全链 OpenAI + sidecar”配置如下；主机名和端口是部署示例，按实际网络替换：

| 配置位置 | 字段 | 值 |
|---|---|---|
| new-api 渠道 | 类型 | `OpenAI`（类型 1） |
| new-api 渠道 | Base URL | `http://image-sidecar:8080`（不带 `/v1`，不带末尾 `/`） |
| new-api 渠道 | API Key | CPA `access.api-keys` 中的一项 |
| new-api 渠道 | 模型 | 本机 CPA 已注册且允许对外暴露的模型 ID |
| new-api 渠道 | 模型映射 | 同名直转留空/`{}`；别名时填 `{"对外名":"CPA模型ID"}` |
| sidecar | CPA 上游根地址 | `http://cliproxyapi:8317` |
| CPA | `access.api-keys` | 与 new-api 渠道 Key 相同的私有随机值 |

实际请求链为：

```text
POST new-api:3000/v1/chat/completions
  → new-api 以 Bearer <CPA access key> 请求 image-sidecar:8080/v1/chat/completions
  → sidecar 仅转换 messages[].content[] 中远程 image_url
  → POST cliproxyapi:8317/v1/chat/completions
```

sidecar 只对**精确方法和路径** `POST /v1/chat/completions` 解析并改写 JSON；其他路径和其他方法原样反向代理，包括 `/v1/models` 等管理/发现请求。转发给 CPA 时保留原 path、query、`Authorization: Bearer <CPA access key>` 和 `Content-Type`；下载图片时另建无凭据请求。响应无论是否为 SSE 都不做协议转换。

## 4. 图片输入的官方契约与 CPA 实际转换证据

### 4.1 OpenAI/Gemini 官方契约

- OpenAI 官方视觉指南允许提供完整图片 URL，也允许 Base64 编码的 data URL；chat content part 形状是 `type: "image_url"` 与 `image_url.url`。[OpenAI Images and vision](https://developers.openai.com/api/docs/guides/images-vision) · [OpenAI 官方 SDK 的 `ImageURL` 类型](https://github.com/openai/openai-python/blob/main/src/openai/types/chat/chat_completion_content_part_image_param.py)
- Gemini 原生 generateContent 文档提供 inline Base64 图片和 Files API 路径；REST `Part` 将 `inlineData` 与 `fileData` 列为互斥数据字段，`Blob` 含 MIME 和 Base64 数据。[Gemini 图片理解](https://ai.google.dev/gemini-api/docs/generate-content/image-understanding) · [Gemini generateContent REST 参考](https://ai.google.dev/api/generate-content#v1beta.Part)

这些官方契约只说明 OpenAI/Gemini 原生 API 的输入形式，**不能据此推断 CPA 的每个兼容转换器都会下载远程 URL**。

### 4.2 CPA 公共 data URL 解析规则

`NormalizeOpenAIFileData` 在 `image_url` 路径中以空 filename/fallback MIME 调用，因此：

| 输入 | 本地结果 |
|---|---|
| `https://…` / `http://…` | 非 `data:` 且没有 fallback MIME，`ok=false`；没有 HTTP 下载 |
| `data:image/png;base64,<非空payload>` | 返回 `mimeType="image/png"` 和原样 payload |
| `data:image/png,<payload>` | 缺少 `base64` 元数据，`ok=false` |
| `data:;base64,<payload>` | MIME 为空，`ok=false` |
| `data:image/png;base64,` | payload 为空，`ok=false` |
| Base64 内容本身损坏 | 此函数不解码验证，仍可能 `ok=true`，错误留到后续上游 |
| MIME 不是图片或不在某上游白名单 | 此函数只要求非空，不做白名单；后续上游仍可能拒绝 |

来源：[`internal/translator/common/file_data.go` L10-L42](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/common/file_data.go#L10-L42)。所以“支持 `data:image/...;base64`”在 CPA Gemini 路径中指**语法解析和内联映射**，不等于任意声明的 MIME 都被 Gemini 接受。

### 4.3 按目标路径区分远程 URL、字段和错误行为

| CPA 目标路径 | 远程 `http(s)` URL | data URL / 输出字段 | 本地错误与限制 |
|---|---|---|---|
| OpenAI chat → **Gemini** | **不抓取，丢弃该 part**。同一 user turn 若还有文本，文本继续；若该 turn 没有可发送内容，400 `unsupported content part: image_url` | 经公共解析器后输出 `inlineData: {mime_type, data}`；只读取 `image_url.url`，这里不保留 `detail` | 无效 data URL 同样走 drop 逻辑；assistant 历史 turn 有另一段手工 data 解析，远程 URL 也跳过。[user 转换](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/gemini/openai/chat-completions/gemini_openai_request.go#L157-L180) · [drop/400 策略](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/common/parts.go#L15-L50) · [assistant 分支](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/gemini/openai/chat-completions/gemini_openai_request.go#L215-L235) |
| OpenAI chat → **Claude** | 转为 `image.source: {type:"url", url:<原值>}`，CPA 不抓取；最终接受性由 Anthropic 上游决定 | `data:` 转为 `source: {type:"base64", media_type, data}`；空 MIME 默认 `application/octet-stream`，不要求显式 `;base64`，也不在此校验 Base64 | 非空远程 URL 本身不产生本地转换错误。[源码](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/claude/openai/chat-completions/claude_openai_request.go#L542-L570) |
| OpenAI chat → **Codex/Responses** | user 图片转为 `input_image.image_url`，URL 字符串原样保留；CPA 不抓取 | 远程和 data URL 都按字符串保留；另有 `detail`、`file_id` 映射 | 仅 user role 的此分支；最终接受性由 Codex 后端决定。[主映射](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/codex/openai/chat-completions/codex_openai_request.go#L237-L246) · [detail/file_id](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/translator/codex/openai/chat-completions/codex_openai_request.go#L565-L587) |
| **openai-compatibility** 自定义上游 | 同格式路径没有在此下载并内联远程图的步骤；交给所配置的上游 | 仍可能经过模型别名和 payload 配置，不应理解为整个 HTTP body 字节级不变 | 最终行为属于所配上游，而非 CPA 统一保证。[executor](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/internal/runtime/executor/openai_compat_executor.go#L150-L185) |

**范围提醒**：这里核实的是当前 CPA 的 OpenAI chat.completions 入站转换。不能把 Gemini 的“丢图”结论套到 Claude/Codex，也不能把 Claude/Codex 的“URL 透传”称为“CPA 已支持远程抓取”。

### 4.4 最小转换前后 JSON

以下示例针对需要转换的 **CPA → Gemini** 路径。假设下载 `https://example.com/pixel.png` 得到 PNG 字节，HTTP Content-Type 为 `image/png`。

转换前：

```json
{
  "model": "gemini-2.5-pro",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "text", "text": "描述这张图片"},
        {
          "type": "image_url",
          "image_url": {
            "url": "https://example.com/pixel.png",
            "detail": "auto"
          }
        }
      ]
    }
  ],
  "stream": true
}
```

转换后只替换该 `url`；模型、文本、`detail` 和 `stream` 不动：

```json
{
  "model": "gemini-2.5-pro",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "text", "text": "描述这张图片"},
        {
          "type": "image_url",
          "image_url": {
            "url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
            "detail": "auto"
          }
        }
      ]
    }
  ],
  "stream": true
}
```

CPA 随后把图片 part 映射为 `{"inlineData":{"mime_type":"image/png","data":"<原Base64 payload>"}}`；`detail` 在该 Gemini 转换分支没有输出映射。模型名只是示例，实际必须使用本机 CPA 已注册的模型 ID。

### 4.5 示例图片 URL 响应实测

调研时对示例中的两个公开图片地址执行了 HEAD（未使用请求中的 API Key）：图一动态 `.php?u=...` 地址返回 `200`、`Content-Type: image/jpeg`，但没有 `Content-Length`；图二返回 `200`、`Content-Type: image/png`、`Content-Length: 35031`。这证明转换器不能依赖 URL 扩展名，也不能只检查 Content-Length；必须以实际响应和读取硬限制为准。

## 5. 已有能力与“不要重复造轮子”检查

### 5.1 new-api 已有下载转换，但不在类型 1 直通路径

new-api 已具备完整媒体加载链：

- `http://`/`https://` 被识别为 `URLSource`。[`file_source.go`](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relaykit/types/file_source.go#L123-L138)
- 协议转换通过 `ResolveBase64Data` 取图片；OpenAI→Gemini 输出 `inlineData`，并检查 Gemini MIME 白名单。[OpenAI→Gemini](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relaykit/relayconvert/internal/oai_chat/to_gemini_chat_req.go#L330-L358) · [OpenAI→Claude](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relaykit/relayconvert/internal/oai_chat/to_claude_messages_req.go#L275-L289)
- `LoadFileSource`/`loadFromURL` 使用 `DoDownloadRequest`，限制文件大小并 Base64 编码原始字节；`DoDownloadRequest` 走 SSRF 校验/受保护 HTTP client 或 Worker。[文件加载](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/service/file_service.go#L157-L190) · [下载保护](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/service/download.go#L36-L55)

但 OpenAI 类型 1 的 `ConvertOpenAIRequest` 对图片 URL 没有上述 `ResolveBase64Data` 调用，最终仍返回同一个请求对象；因此远程 URL 会留到 CPA。[OpenAI `ConvertOpenAIRequest`](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/channel/openai/adaptor.go#L253-L443) 未发现类型 1 渠道上的“远程 `image_url` 自动转 Base64”开关；现有行为由转换路径决定。

**与当前决策的关系**：高级自定义渠道确实提供 `openai_chat_completions_to_gemini_generate_content` 和 `openai_chat_completions_to_anthropic_messages`。[转换器常量与路由字段](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relaykit/dto/channel_settings.go#L166-L198) 但当前已明确为全链 OpenAI，本报告不展开也不建议改用这些原生协议路径；记录它们只为定位已有下载代码，避免误以为类型 1 直通也会调用。

### 5.2 配置覆盖能改请求，但不能动态下载

- **new-api 参数覆盖**是真实的出站请求改写：支持 set/delete/move/copy/replace/regex_replace 等操作，并可定位 `messages` 路径。[官方文档](https://docs.newapi.ai/zh/docs/guide/feature-guide/admin/channel#参数覆盖系统) · [执行位置](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/relay/compatible_handler.go#L132-L138) 它没有 HTTP 下载/读响应字节/Base64 编码操作，所以只能写死值或改字符串，不能完成本任务。
- **CPA `requests.payload`**同样是真实改写：有 default/override/override-raw/filter 和 gjson/sjson 路径，但没有网络计算能力。[配置说明](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/config.example.yaml#L280-L340)
- **new-api Task Plugin（类型 61）不是 chat.completions 中间件**。它面向任务/图片/视频/Responses 等声明协议和异步任务生命周期，不能据此拦截普通 OpenAI chat 渠道的出站请求。[new-api README 的插件边界](https://github.com/QuantumNous/new-api/blob/8faf1d45e8c101974dc7483324d3d91c8a462ffe/README.md#L160-L181) · [Task Plugin API](https://docs.newapi.ai/zh/docs/plugins/api-reference)
- **自定义 Provider/Executor 不是同义的请求中间件**。CPA 的 Executor/RequestTranslator 用于接入新上游；真正改内置链路请求体的是 `RequestInterceptor` 或 `RequestBodyPatcher`。[CPA Capabilities](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/sdk/pluginapi/types.go#L63-L137) · [请求拦截文档](https://help.router-for.me/cn/plugin/request-interceptor)

### 5.3 为什么排除现成的 `cpa-plugin-jshandler`

该插件确实能在翻译前修改 `ctx.body`，不是“仅任务插件”。[README 钩子说明](https://github.com/router-for-me/cpa-plugin-jshandler/blob/3e2252710a7585f7fb9c0f0d87fd86134d72876a/README.md#js-script-api) 但当前固定版本中：

- 注册能力只有 Request/Response/StreamChunk Interceptor，没有 Executor。[`main.go`](https://github.com/router-for-me/cpa-plugin-jshandler/blob/3e2252710a7585f7fb9c0f0d87fd86134d72876a/main.go#L19-L51)
- 每个 Goja VM 只额外注入 `console.log`；JS API 和引擎初始化没有 `fetch`、HTTP client 或文件网络读取绑定。[`engine.go`](https://github.com/router-for-me/cpa-plugin-jshandler/blob/3e2252710a7585f7fb9c0f0d87fd86134d72876a/engine.go#L30-L63)
- 脚本执行错误会记录 warning 并继续，初始 body 保持原值；这意味着即使脚本试图调用不存在的网络 API，默认也不是可靠失败，而可能把未转换请求继续发往 CPA。[`interceptor.go`](https://github.com/router-for-me/cpa-plugin-jshandler/blob/3e2252710a7585f7fb9c0f0d87fd86134d72876a/interceptor.go#L64-L111)

所以它只能改已有字符串，**不能独立完成远程下载并内联**。这不是说 CPA 插件机制整体不行：另写 Go `RequestInterceptor` 可以自行执行 HTTP 下载并替换 body，且不必 fork CPA；但那仍是需要维护的自定义组件。

## 6. 四种转换位置的维护成本与流式影响

| 位置 | 维护成本 | 对流式响应的影响 | 适用判断 |
|---|---|---|---|
| **独立反向代理 sidecar：new-api → sidecar → CPA** | 新增一个小服务和一次内网跳点；需要自行维护下载安全、监控和版本升级，但不跟随两个上游 rebase，也不绑定 CPA 插件 ABI | 只应在请求阶段缓存 JSON/下载图片；响应必须逐块转发并及时 Flush，不能等完整 SSE 结束。这样做不会改变 CPA 的响应协议，但图片下载会推迟上游请求和首 token | **题目给定的类型 1 直通链路上首选**；new-api Base URL 改为 `http://<sidecar>:<port>`，sidecar 上游为 `http://<cliproxyapi>:8317`，路径仍不带 `/v1` 前缀配置 |
| **修改 new-api** | 可在 OpenAI 出站前集中处理，并复用 `LoadFileSource`、SSRF/大小限制；但需要维护 fork、数据库/渠道语义与上游合并，误加在公共转换器还可能波及其他渠道 | 只改请求时，响应流式路径无需变化；主要增加请求前延迟 | 已长期维护 new-api fork、需要统一渠道审计/限流时合理；单次部署不是最小维护 |
| **修改 CLIProxyAPI** | 修改内置 translator 可让所有客户端受益，但多个目标路径语义不同，fork 合并成本高；自写 Go RequestInterceptor 可免 fork，却需跟踪插件 API/ABI 和部署插件 | 请求拦截发生在执行前，本身无需碰 SSE；不要让响应拦截器缓存流 | 希望所有 CPA 客户端统一受益，或已经运维 CPA 插件时合理；现成 jshandler 不够 |
| **客户端预转换** | 服务端零改动；但每个客户端/调用方都要实现下载、安全和错误策略，闭源或多方客户端难统一 | 不影响服务端响应流；客户端承担下载延迟和流量 | 只有一个可控客户端且愿意改它时最省；不适合作为多用户 new-api 网关的统一修复 |

四种位置都应只改请求图片 part；**没有任何一种需要为了此功能解析或重写响应 SSE**。sidecar 是唯一会额外经过一层响应代理的选项，因此必须明确其流式透传要求。

## 7. 转换器的必要边界

sidecar 实现遵循以下边界；公开 HTTP 测试见 `proxy_test.go`，运行方式见 `README.md`。

1. **只改图片 part**：遍历所有消息（包括 assistant 历史）的数组形式 `content`，仅处理 `type="image_url"` 且 `image_url.url` 为字符串的 part；文本、工具调用、模型名、`detail`、`stream` 和其他字段保持不变。字符串形式的纯文本 content 不需拆开。
2. **原字节、不重编码**：下载成功后直接 Base64 编码响应原始字节；不解码图片、不缩放、不转 JPEG/PNG。MIME 取可信且规范化后的 Content-Type，必要时 sniff；不要用 URL 后缀单独证明内容类型。
3. **data URL 幂等**：已有 `data:` URL直接保留，不能再下载、解码后重编码或重复加前缀。重复经过转换器应得到相同请求。非 `http(s)` 的其他 scheme 不得交给通用客户端读取。
4. **响应流式透传**：保留上游状态码和必要响应头；SSE 按到达 chunk 写出并 Flush，传递取消和背压；不得为了转换请求图片而缓存完整响应。请求侧则必须等所有选中的图片转换完成后才发上游，避免半转换请求。
5. **资源限制**：至少设置入站 JSON body、单图片字节数、单请求图片数量、所有图片总字节数、单图和整请求下载 deadline、并发数及重定向次数；Base64 后约膨胀也应计入出站 body/内存预算。Content-Length 只能作预检，读取时仍需硬性限流。CPA 当前 `server.max-request-body-bytes` 示例默认 0（不限制），不能替转换器承担这些限制。[CPA body limit 配置](https://github.com/router-for-me/CLIProxyAPI/blob/91e0dee827abf476f9f86246f68c500a112f7155/config.example.yaml#L30-L44)
6. **SSRF——域名白名单、重定向与 DNS 都要管**：实现采用精确域名白名单，但白名单不能作为唯一防线。初始 URL 和每次重定向都重新校验 scheme、userinfo 和主机名；实际连接时阻断 loopback、private、link-local、multicast、unspecified、保留地址，并覆盖 IPv4-mapped IPv6。DNS 校验与实际连接地址绑定，避免“检查时公网、连接时内网”的 rebinding；保留原 Host/SNI/证书校验。OWASP 特别指出重定向绕过和二次 DNS 查询风险。[OWASP SSRF 防护](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)
7. **下载认证隔离**：不要把 new-api/CPA 的 `Authorization`、cookie 或上游凭据转发给图片主机；跨域重定向更不能继承它们。只允许无需自定义认证头或 Cookie 的公网图片 URL；签名 URL 的 query 可用于下载，但日志必须脱敏。
8. **失败语义要可靠且完整**：任一选中的图片下载、超时、超限或类型不支持时，在调用 CPA 前失败整个请求，不静默删图，也不发送部分转换结果。建议无效 scheme/字段/MIME 用 400，数量/大小超限用 413，远端非成功响应用 502，下载 deadline 用 504；这些是建议映射，需与 new-api 错误格式适配。错误日志不要包含完整 Base64、签名 URL query 或任何 API Key。

## 8. 已确认决策、实施默认值与版本前提

已确认的决策：**全链 OpenAI；独立 sidecar；精确图片域名白名单（初始占位 `images.example.com`）；任一图片失败则整单失败；sidecar 只转换 `POST /v1/chat/completions`，其他路径透传；与 new-api、CLIProxyAPI 在同一 Docker Compose 内网部署，不将 sidecar 暴露到公网；两个上游均使用最新版镜像；模型使用已确认可用的 `gemini-3.1-flash-image`。** 实施默认策略：

1. HTTP/HTTPS 均按用户选择允许；签名 URL 的 query 可用于下载但日志必须脱敏；不支持自定义认证头或 Cookie 的图片地址。
2. 资源阈值先采用保守常量：每请求最多 8 张远程图片、单图 10 MiB、原始图片总计 20 MiB、最多 5 次重定向、单图下载 15 秒、整请求图片处理 45 秒、下载并发 4。它们是 sidecar 的防护阈值，不代表最终模型一定接受该大小；Base64 和 JSON 开销另计。
3. 客户端、new-api 渠道和 CPA 的模型 ID 同名时模型映射留空；只有需要别名时才增加映射。

实现以最新版镜像为目标；源码行为引用仍固定到上述 commit，避免后续默认分支变化使证据失效。

## 9. 无法确认与验证边界

- 已运行真实 sidecar 容器，以本地模拟 CLIProxyAPI 验证示例两张公网图片的下载、MIME、原始字节数、模型名、文本、鉴权及 `/v1/models` 透传；公开 HTTP 测试还验证了 SSE 首块提前到达。另以最新版 CLIProxyAPI 镜像启动真实 Compose 服务，并经 sidecar 请求 `/v1/models` 得到 200 空列表（未挂载模型认证）。未运行真实 new-api 或最终模型上游，因此真实端到端生成、模型图片接受性和生产 SSE 延迟仍需在实际部署验证。
- Claude URL source、Codex `input_image` 远程 URL 以及 openai-compatibility 上游是否最终抓取/接受远程 URL，取决于各自上游；CPA 源码只能证明它如何转发。
- CPA 公共解析器不做 MIME 白名单和 Base64 内容验证；具体模型支持的 MIME、尺寸和总请求限制仍须按实际上游与模型文档核对。
- 官方 OpenAI/Gemini 在线文档是动态页面，以上链接未固定 commit；项目行为结论均以固定 SHA 源码为准。
