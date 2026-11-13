# flow2api 与 new-api 直连协议调研

查阅日期：2026-11-13。仅使用两个项目的官方 GitHub 仓库源码与 README；未使用二手文章，未做运行时联调，也未检查本地 `image-sidecar` 实现，因此本文不包含本地转换差异结论。

## 结论先行

**当前源码下，new-api 可以直接中转 flow2api，不需要额外的 sidecar/协议适配器。** flow2api 原生同时公开 OpenAI Chat Completions 和 Gemini `generateContent` 两套接口，并非只接受 Gemini 格式。

最省事的选择是 **new-api 的 OpenAI 渠道 → `POST {base_url}/v1/chat/completions`**：模型、消息、流式标志和图像 URL 均在同一协议内传递，new-api 会设置 `Authorization: Bearer <渠道密钥>`，flow2api 原生接受该认证。[N1][N2][F1][F3]

如果调用方使用 Gemini 协议，则选 **new-api 的 Gemini 渠道 → `/v1beta/models/{model}:generateContent` 或 `:streamGenerateContent?alt=sse`**，也无需 sidecar。[N4][N5][F1]

需要区分三种边界：

1. **仅配置**：上述两条同协议链路只需配置渠道类型、Base URL、密钥和模型名/模型映射，不需路径重写或请求转换。
2. **new-api 自己转换**：OpenAI 入站 → Gemini 渠道、Gemini 入站 → OpenAI 渠道时，new-api 会转换请求及响应；这不是直连不兼容，也不需要外部 sidecar。[N2][N4][N8][N9]
3. **需要适配或改路由**：调用方坚持使用 flow2api 未提供的接口（例如 OpenAI Images `/v1/images/generations`），或必须保留会被 new-api 默认 DTO 序列化丢弃的自定义字段时，才需换用 new-api 的可转换路由/透传能力，或增加适配器。flow2api 的公开路由中没有 `/v1/images/generations`。[F1][N6][N7]

另一个已核实边界：OpenAI 渠道的默认类型化转发不能当作任意字段透传；顶层 `generationConfig`/`quality` 不在 new-api 的 `GeneralOpenAIRequest` 中。应优先使用完整 flow2api 模型名，或使用已被双方支持的 `size` / `extra_body.generationConfig` 形式。[N2][N6][N7][F6]

远程参考图也不需要 sidecar 预先下载或把 URL 转成 data URL：flow2api 的 `_load_image_bytes_from_uri` 原生支持 HTTP/HTTPS 下载和 data URL。直连时远程下载及其实现层面的安全控制由 flow2api 承担；本文未进一步审计其 SSRF 防护。[F7]

## 固定版本

| 项目 | 本次检查的提交 | 版本说明 |
|---|---|---|
| `TheSmallHanCat/flow2api` | [`2cf700d1c6f36348b37405dbc0c9a2d5397338e4`](https://github.com/TheSmallHanCat/flow2api/tree/2cf700d1c6f36348b37405dbc0c9a2d5397338e4) | 提交时间 2026-11-08；`git describe` 仅得到短提交号，未声称对应某个发布版 |
| `QuantumNous/new-api` | [`0bda086fb4c2f83ac0397e4a60d90686578d67ad`](https://github.com/QuantumNous/new-api/tree/0bda086fb4c2f83ac0397e4a60d90686578d67ad) | `v1.0.0-rc.50-1-g0bda086f`，提交时间 2026-11-12；不是正式发布标签本身 |

以下链接均固定到上述提交。

## Compose 部署契约

| 项目 | 必须采用的契约 | 一手依据 |
|---|---|---|
| 官方镜像 | 标准模式使用 **`ghcr.io/thesmallhancat/flow2api:latest`**；这是官方 `docker-compose.yml` 明确给出的镜像名，不是推测。发布 workflow 也从该仓库构建并推送 GHCR 标准镜像。[F11][F15] | [F11][F15] |
| 官方构建替代 | 不使用镜像时，在固定提交的仓库根目录执行构建：Compose `build.context` 为仓库根目录、`dockerfile` 为 `Dockerfile`。官方 `docker-compose.local.yml` 即如此；`Dockerfile` 基于 `python:3.11-slim`，以 `python main.py` 启动。[F11] | [F11] |
| 容器端口 | **8000/TCP**；`Dockerfile` `EXPOSE 8000`，默认 `[server] host="0.0.0.0"`、`port=8000`，入口按此配置启动。同一 Compose 网络内 new-api/sidecar 根 URL 使用 **`http://flow2api:8000`**，无需发布宿主机端口；官方示例的 `38000:8000` 只是宿主机映射。[F11][F12] | [F11][F12] |
| 卷 | 按官方 Compose 保留 **`/app/data`**（SQLite `flow.db`）、**`/app/tmp`**（缓存/生成文件）和 **`/app/config/setting.toml`**（配置文件）。前两者需要持久化；配置挂载应是预先创建的普通文件；运行配置存入 SQLite，不要求回写 TOML，避免 Docker 将缺失路径创建成目录。[F11][F12][F13] | [F11][F12][F13] |
| 环境变量 | 官方标准 Compose 仅设置 **`PYTHONUNBUFFERED=1`**。API key、管理员凭据和监听地址不是由环境变量注入，而是在 TOML/数据库中配置。[F11][F12][F13] | [F11][F12][F13] |
| API key/基本配置 | `setting.toml` 至少设置 **`[global].api_key`**（同时改掉默认 `admin/admin`）、`[flow]` 与 **`[server]`**；new-api 渠道密钥必须等于该 `api_key`。官方示例默认值 `han1234` 不应作为生产密钥。首次启动由 TOML 初始化 SQLite，随后从数据库恢复运行配置，因此已有 `/app/data` 时仅改 TOML 不一定覆盖数据库中的 API key/管理员设置。[F12][F13][F3] | [F12][F13][F3] |
| 健康检查 | 使用公开的 **`GET http://flow2api:8000/health`**，不要求 API key；官方 Dockerfile 和 Compose **未定义 healthcheck**，需在调用方 Compose 自行添加。基础镜像不保证有 `curl`，可直接用 Python 标准库发起检查。健康只证明后端进程存活，不证明存在可用 Flow Token。[F11][F14] | [F11][F14] |

**实际生成的前置条件**：容器可以在没有 Flow Token 时启动并通过 `/health`，但真正出图还需在管理后台配置有效 Flow Token；默认第三方打码方式还需配置对应的打码服务 API key，或改用项目支持的浏览器/其他打码方式。缺少这些不会阻止 Compose 启动，却会阻止实际生成。[F13][F14]

**版本边界**：`latest` 是可变标签，并不因本文固定了源码 commit 就变成不可变镜像。若部署必须严格对应该 commit，应检出该 commit 后按上表官方 `Dockerfile` 构建；本文未获得或猜测该 commit 对应的镜像 digest。[F11][F15]

**运行验证**：`docker compose config --quiet` 通过；Dockerfile builder 中 `go test ./...` 通过；使用只含 `global/flow/server` 的最小 `setting.toml` 启动上述官方 `latest` 镜像，`/health` 返回 `backend_running=true`；健康门控验证中，Compose 等待 flow2api 变为 `Healthy` 后才启动 `image-sidecar-flow`；随后经 sidecar 请求 `http://flow2api:8000/v1/models` 返回 200 和模型列表。未配置真实 Flow Token，未执行实际出图验证。

## 1. flow2api 公开协议契约

### 1.1 端点、认证与响应

| 用途 | 公开路径 | 请求契约 | 响应契约 |
|---|---|---|---|
| OpenAI 模型列表 | `GET /v1/models`；另有 `GET /v1/models/aliases` | 需 API key | OpenAI 风格 `{object:"list", data:[...]}`；模型项含 `id/object/owned_by/description`。[F1] |
| Gemini 模型列表/详情 | `GET /v1beta/models`、`GET /models`；`GET /v1beta/models/{model}`、`GET /models/{model}` | 需 API key | Gemini 风格 `models[]` 或单个模型资源；声明支持 `generateContent`、`streamGenerateContent`。[F1] |
| OpenAI 统一生成 | `POST /v1/chat/completions` | JSON body 的 `model` 必填；`messages` 与 `contents` 至少一种能形成非空提示词；`stream` 可选，默认 `false`。[F1][F2] | `stream:false` 为 `chat.completion` JSON；`stream:true` 为 `text/event-stream`，事件为 `chat.completion.chunk`，结束发 `data: [DONE]`。[F1][F4] |
| Gemini 非流式生成 | `POST /v1beta/models/{model}:generateContent` 或 `POST /models/{model}:generateContent` | 路径模型名 + JSON body；`contents` 必填，`generationConfig`、`systemInstruction` 可选。[F1][F2] | Gemini 风格 `candidates[].content.parts[]`、`finishReason`、`modelVersion`；图片输出转为 `inlineData`，无法读取为内联数据时可回退为 `fileData` + 文本 URL。[F1][F5] |
| Gemini 流式生成 | `POST /v1beta/models/{model}:streamGenerateContent` 或 `POST /models/{model}:streamGenerateContent` | 同上；`alt` 查询参数被路由接收，但实现不检查其值，始终返回 SSE。[F1] | `text/event-stream`，每个 `data:` 是 Gemini 风格响应；不追加 OpenAI 的 `[DONE]`。[F1] |

**认证**：这些公开 API 都使用同一个灵活认证依赖，接受 `Authorization: Bearer <api_key>`、`x-goog-api-key: <api_key>` 或 `?key=<api_key>`；密钥必须等于 flow2api 配置的 API key，否则 401。[F1][F3] 这里的 key 是 flow2api 的访问密钥，不是其内部 Flow 账号 token。

**重要范围限制**：公开生成入口是 Chat Completions 与 Gemini GenerateContent；没有 OpenAI Images、Responses 或 Claude Messages 路由。不要把“OpenAI 兼容”理解成支持所有 OpenAI API 产品。[F1]

README 的 API 示例标题写“需要使用流式”，但固定提交的 Pydantic 模型把 `stream` 默认设为 `false`，路由也有完整非流式分支；因此本文按源码认定非流式受支持。此差异未做运行时验证。[F1][F2]

### 1.2 OpenAI Chat Completions 请求字段

`ChatCompletionRequest` 的显式字段如下；`extra="allow"` 表示其他 JSON 字段可被 Pydantic 接收，但不代表都会参与生成。[F2]

| 字段 | 必填/默认 | 实际用途与限制 |
|---|---|---|
| `model: string` | 必填 | 可以是完整公开模型名或基础别名；未知模型最终在生成处理中报 400。[F2][F6] |
| `messages: [{role, content}]` | 通常必填 | `content` 可为字符串或多模态数组。实现只取**最后一条消息**作为当前提示词/图片输入；图片模型还可能从历史 assistant 字符串中提取 Markdown 图片作为参考图。[F2][F7] |
| `stream: boolean` | 可选，`false` | 选择 JSON 或 OpenAI SSE。[F1][F2] |
| `temperature`、`max_tokens` | 可选 | schema 接受；当前公开路由的归一化与生成调用未使用它们，不能据此假定会控制 Flow 生成。[F1][F2] |
| `generationConfig` | 可选 | Gemini 扩展；用于解析图片/视频模型别名及比例、分辨率。[F2][F6] |
| `contents` | 可选替代入口 | Chat Completions 路径也接受 Gemini 风格 `contents`；但 new-api 自身验证 OpenAI Chat 请求时要求 `messages`，所以经 new-api 调用不应依赖这个替代入口。[F1][F2][N6] |
| `image`、`video` | 可选、标记 deprecated | `image` 是旧 Base64/URI 输入；新代码应使用 `messages[].content`。[F2] |

多模态 `messages[].content[]` 支持 `type:"text"` 和 `type:"image_url"`；`image_url.url` 可为 HTTP(S) URL、`data:image/...;base64,...` 或本地缓存 URI。视频续写另有 `extend://MEDIA_ID` 约定。[F7]

### 1.3 Gemini GenerateContent 请求字段

- `contents` 必填，每项含 `role`（`user`/`model`，可省略）和 `parts`；实现从最后一个 user 内容提取文本与图片。[F2][F7]
- part 支持 `text`、`inlineData:{mimeType,data}`、`fileData:{fileUri,mimeType?}`；图片之外的 `inlineData`/`fileData` 在该媒体归一化路径会被拒绝。[F2][F7]
- `systemInstruction` 可选，会拼入提示词；媒体模型会清理部分 agent/tool 模板文本。[F1]
- `generationConfig.responseModalities` 与 `generationConfig.imageConfig` 被 schema 接受。图片模型选择实际由 `model` 与 `imageConfig.aspectRatio/imageSize` 解析决定；没有看到 `responseModalities` 改变生成类型的逻辑。[F2][F6]

### 1.4 模型名和图像生成参数

当前图片基础别名为 `gemini-3.1-flash-image`、`gemini-3.1-flash-lite-image`，旧 `gemini-3.0-pro-image` 别名仍可解析。可直接使用带后缀的完整模型名，例如 `gemini-3.1-flash-image-landscape`、`gemini-3.1-flash-image-square-2k`；也可用基础别名配合 `generationConfig.imageConfig`。[F6][F10]

支持的比例映射包括 `16:9 → landscape`、`9:16 → portrait`、`1:1 → square`、`4:3 → four-three`、`3:4 → three-four`；`imageSize` 支持 `1K/2K/4K` 等归一化，具体模型不支持的分辨率会被忽略。2K/4K 是 flow2api 生成后的放大档位，不应等同于 OpenAI 像素尺寸。[F6][F10]

Chat Completions 还兼容：

- 顶层额外字段 `size: "宽x高"`：推断比例；
- 顶层额外字段 `quality`：映射放大档位，如 `medium → 2k`、`high/hd → 4k`；
- `extra_body.generationConfig.imageConfig.{aspectRatio,imageSize}` 等嵌套形式。[F6]

**这对 new-api 很关键**：new-api 默认 OpenAI DTO 有 `size`、`extra_body`、`imageConfig`，但没有顶层 `generationConfig` 或 `quality` 字段，并会重新序列化请求。因此默认非透传模式下，应使用完整模型名、顶层 `size`，或 `extra_body.generationConfig...`；不要假定顶层 `generationConfig`/`quality` 会穿过 new-api 的 OpenAI 渠道。[N7][N6][F6]

### 1.5 图像响应不是 OpenAI Images 响应

OpenAI 非流式图片结果固定为单个 `choices[0].message.content`，内容是 `![Generated Image](URL或data URL)`；流式最终图片也放在 `choices[0].delta.content`，过程消息则常放在 `delta.reasoning_content`。它不是 `/v1/images/generations` 的 `{data:[{url}]}` 或 `{data:[{b64_json}]}` 格式。[F4]

Gemini 原生端点则把 Markdown 图片结果转换为 `candidates[].content.parts[].inlineData`（可读取图片时），或 `fileData` 加文本 URL（无法内联时）。[F5]

## 2. new-api 的出站行为

### 2.1 标准 OpenAI 渠道

- 普通 OpenAI Chat 入站时，`ConvertOpenAIRequest` 返回同一 `GeneralOpenAIRequest` 对象，不做 OpenAI→Gemini 转换；此前 `ModelMappedHelper` 会把 body 的 `model` 改成上游模型名。[N2][N8]
- 默认 URL 是 `ChannelBaseUrl + RequestURLPath`，所以 Chat Completions 到 `{base_url}/v1/chat/completions`；拼接函数只是字符串连接，不会自动删除 `/v1` 或规范双斜杠。[N2][N3]
- 非 Realtime 请求设置 `Authorization: Bearer <渠道 ApiKey>`。[N2]
- 默认处理链路会解析为 DTO、转换后重新 JSON 序列化；启用全局或渠道 `pass_through_body_enabled` 时才直接复用原始 body。因此“OpenAI 渠道不改协议”不等于“任何未知字段都原样透传”。[N6][N7]
- OpenAI 入站/OpenAI 上游的响应保持 OpenAI 格式；实现仍可能补充 usage 或按渠道设置重排格式，不应称为字节级透传。[N9]

**配置边界**：Base URL 应填 `http(s)://<flow2api-host>:<port>`，不要带 `/v1`，也不要带尾部 `/`。正常部署无需路径重写；若反向代理把服务挂在子路径，只需代理解除该前缀，仍不需协议 sidecar。[N2][N3]

### 2.2 标准 Gemini 渠道

- 默认 API 版本为 `v1beta`；普通生成出站 URL 是 `{base_url}/v1beta/models/{upstream_model}:generateContent`，流式是 `...:streamGenerateContent?alt=sse`。[N4][N5]
- 认证头是 `x-goog-api-key: <渠道 ApiKey>`。[N4]
- Gemini 原生入站时，请求仍是 Gemini DTO，实现只补默认 role、思考配置等，不改成 OpenAI；原生响应直接按 Gemini body/SSE 写回。[N4][N10]
- OpenAI Chat 入站时，`ConvertOpenAIRequest` 明确调用转换器生成 Gemini 请求；响应再把 Gemini 转为 OpenAI Chat。图片 `inlineData` 会被转为 OpenAI message/delta 中的 Markdown data URL。[N4][N11]

### 2.3 跨协议组合的图像语义差异

| 客户端协议 | new-api 渠道 | new-api → flow2api 实际出站 | 是否需要 sidecar | 图像响应注意事项 |
|---|---|---|---|---|
| OpenAI Chat | OpenAI | OpenAI `/v1/chat/completions` | **不需要** | flow2api 返回 Markdown 图片 URL/data URL；最贴合其原生 Chat 输出。[F4][N2] |
| Gemini | Gemini | Gemini `/v1beta/models/{model}:generateContent` 或流式路径 | **不需要** | 保留 Gemini `candidates/parts`，可得到原生 `inlineData`。[F5][N4][N10] |
| OpenAI Chat | Gemini | new-api 转成 Gemini GenerateContent | **不需要，new-api 已转换** | Gemini 图片 `inlineData` 再被 new-api 转成 Markdown data URL。[N4][N11] |
| Gemini | OpenAI | new-api 转成 OpenAI `/v1/chat/completions` | **协议上不需要** | new-api 把 OpenAI 文本内容放进 Gemini `parts[].text`，不会把 flow2api 的 Markdown 图片再解析成 `inlineData`；若调用方要求原生 Gemini 图片 part，应改用 Gemini 渠道。[N2][N12][F4] |

最后一种组合说明“协议兼容”与“图像语义完全等价”不是一回事：它能形成合法的 Gemini 响应，但图片可能只是 Markdown 文本。

## 3. 何时需要转换、响应处理或仅重写

| 场景 | 所需动作 |
|---|---|
| OpenAI Chat 客户端 → OpenAI 渠道 → flow2api | **仅配置**；无需请求或响应协议转换。注意默认 DTO 的自定义字段边界。[N2][N6][N7] |
| Gemini 客户端 → Gemini 渠道 → flow2api | **仅配置**；路径、认证和流式格式双方直接匹配。[F1][F3][N4] |
| OpenAI ↔ Gemini 的跨协议组合 | **new-api 内部同时做请求和响应转换**；无需外部 sidecar，但注意上表的图片表示差异。[N2][N4][N9][N11][N12] |
| 需要使用 flow2api 没有注册的 `/v1/images/generations` | 标准直连不够；改用 Chat/Gemini 端点，或在 new-api 自定义路由/外部适配器中转换请求和 Images 响应。[F1] |
| 客户端必须收到 OpenAI Images `data[].url/b64_json`，而非 Chat Markdown | 需要响应语义转换，或修改客户端；OpenAI Chat 渠道不会自动变成 Images API。[F4][N9] |
| 必须发送 new-api DTO 未覆盖的自定义字段 | 优先使用 flow2api 已支持的 `extra_body.generationConfig` 形式；仍不够时才评估 new-api body 透传或适配器。[F6][N6][N7] |
| flow2api 位于反向代理子路径 | **仅路径重写/去前缀**；不是协议转换。Base URL 的字符串拼接规则要求避免重复 `/v1` 和双斜杠。[N3][N4] |

## 4. 建议的最小配置方向

1. **首选 OpenAI 渠道**：Base URL 填 flow2api 根地址；密钥填 flow2api API key；渠道模型列表填写 flow2api 的公开模型名，必要时用模型映射把客户端名称映射到这些名称。模型映射会写入出站 body/路径使用的上游模型名。[N8][N4]
2. 图像调用可直接使用完整模型名（例如 `gemini-3.1-flash-image-landscape`）以避免任何扩展字段传递问题。[F10]
3. 若使用基础别名和 Gemini 图片参数，在 OpenAI Chat 请求中采用：
   ```json
   {
     "model": "gemini-3.1-flash-image",
     "messages": [{"role":"user","content":"一只猫"}],
     "stream": true,
     "extra_body": {
       "generationConfig": {
         "responseModalities": ["IMAGE"],
         "imageConfig": {"aspectRatio":"16:9","imageSize":"2K"}
       }
     }
   }
   ```
   new-api 的 DTO 保留 `extra_body`，flow2api 明确从其读取 `generationConfig`；`responseModalities` 可省略，因为模型本身已决定生成类型。[N7][F6]
4. 若客户端原生使用 Gemini，则直接选 Gemini 渠道，避免 OpenAI 中间表示带来的 Markdown 图片语义差异。[N4][N10][N11]

## 一手来源

### flow2api（提交 `2cf700d1c6f36348b37405dbc0c9a2d5397338e4`）

- **[F1]** 公开路由、OpenAI/Gemini 流式与非流式分支、Gemini 响应包装：[`src/api/routes.py` L821-L1005](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/api/routes.py#L821-L1005)；流式迭代器 [L750-L816](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/api/routes.py#L750-L816)。
- **[F2]** OpenAI/Gemini 请求模型及必填、默认、额外字段：[`src/core/models.py` L247-L332](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/core/models.py#L247-L332)。
- **[F3]** Bearer、`x-goog-api-key`、查询 `key` 认证：[`src/core/auth.py` L40-L62](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/core/auth.py#L40-L62)。
- **[F4]** OpenAI JSON/SSE 的具体字段、Markdown 图片和 `[DONE]`：[`src/services/generation_handler.py` L3444-L3516](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/services/generation_handler.py#L3444-L3516)；图片输出调用 [L2594-L2618](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/services/generation_handler.py#L2594-L2618)；路由终止标记见 [F1]。
- **[F5]** Gemini 图片 `inlineData`/`fileData` 与响应 payload：[`src/api/routes.py` L622-L704](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/api/routes.py#L622-L704)。
- **[F6]** 图像参数优先级、`extra_body`、顶层 `size/quality` 与模型解析：[`src/core/model_resolver.py` L415-L638](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/core/model_resolver.py#L415-L638) 及 [L642-L727](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/core/model_resolver.py#L642-L727)。
- **[F7]** OpenAI/Gemini 文本、图片、历史参考图提取：[`src/api/routes.py` L305-L400](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/api/routes.py#L305-L400)；URI 加载范围 [L239-L248](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/api/routes.py#L239-L248)。
- **[F10]** 当前图片模型别名、比例、分辨率映射：[`src/core/model_resolver.py` L20-L110](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/core/model_resolver.py#L20-L110)；README 模型表和 Gemini 示例 [`README.md` L189-L215](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/README.md#L189-L215)、[L405-L478](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/README.md#L405-L478)。
- **[F11]** 官方镜像、构建上下文、端口和三个挂载：[`docker-compose.yml`](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/docker-compose.yml)、[`docker-compose.local.yml`](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/docker-compose.local.yml)、[`Dockerfile`](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/Dockerfile)。
- **[F12]** 配置项、默认 API key/管理员凭据、监听地址及配置文件加载/回退：[`config/setting_example.toml`](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/config/setting_example.toml)、[`src/core/config.py` L50-L80](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/core/config.py#L50-L80)、[`main.py`](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/main.py)。
- **[F13]** SQLite 路径、TOML 首次初始化及数据库运行配置：[`src/core/database.py` L26-L140](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/core/database.py#L26-L140)、[`src/main.py` L75-L122](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/src/main.py#L75-L122)。
- **[F14]** 官方部署前提、打码方式与公开 `/health`：[`README.md` L60-L154](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/README.md#L60-L154)。
- **[F15]** GHCR 官方镜像构建与发布流程：[`docker-publish.yml`](https://github.com/TheSmallHanCat/flow2api/blob/2cf700d1c6f36348b37405dbc0c9a2d5397338e4/.github/workflows/docker-publish.yml)。

### new-api（提交 `0bda086fb4c2f83ac0397e4a60d90686578d67ad`）

- **[N1]** new-api 公开 Chat/Gemini 路由：[`router/relay-router.go` L71-L107](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/router/relay-router.go#L71-L107)、[L191-L220](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/router/relay-router.go#L191-L220)。
- **[N2]** OpenAI 渠道 URL、Bearer 认证、同协议请求返回及 Gemini 入站转 OpenAI：[`relay/channel/openai/adaptor.go` L45-L59](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/channel/openai/adaptor.go#L45-L59)、[L178-L237](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/channel/openai/adaptor.go#L178-L237)、[L237-L316](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/channel/openai/adaptor.go#L237-L316)。
- **[N3]** Base URL 与请求路径直接拼接：[`relay/common/relay_utils.go` L26-L40](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/common/relay_utils.go#L26-L40)。
- **[N4]** Gemini 出站 URL、`x-goog-api-key`、OpenAI→Gemini 请求转换与原生 Gemini 请求处理：[`relay/channel/gemini/adaptor.go` L28-L49](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/channel/gemini/adaptor.go#L28-L49)、[L159-L202](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/channel/gemini/adaptor.go#L159-L202)。
- **[N5]** Gemini 默认 `v1beta`：[`setting/model_setting/gemini.go` L33-L48](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/setting/model_setting/gemini.go#L33-L48)、[L103-L110](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/setting/model_setting/gemini.go#L103-L110)。
- **[N6]** OpenAI/Gemini 请求解析、必填验证、DTO 重新序列化及 body 透传分支：[`relay/helper/valid_request.go` L331-L413](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/helper/valid_request.go#L331-L413)；[`relay/compatible_handler.go` L72-L153](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/compatible_handler.go#L72-L153)。
- **[N7]** OpenAI DTO 的 `size`、`extra_body`、`imageConfig` 等实际字段（无顶层 `generationConfig`/`quality`）：[`relaykit/dto/openai_request.go` L29-L132](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relaykit/dto/openai_request.go#L29-L132)。
- **[N8]** 模型映射写入 `UpstreamModelName` 和请求模型：[`relay/helper/model_mapped.go` L15-L68](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/helper/model_mapped.go#L15-L68)。
- **[N9]** OpenAI 上游响应在 OpenAI/Gemini 客户端格式之间的转换，以及 OpenAI 同协议响应处理：[`relay/channel/openai/relay-openai.go` L253-L373](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/channel/openai/relay-openai.go#L253-L373)。
- **[N10]** Gemini 原生 JSON/SSE 响应写回：[`relay/channel/gemini/relay-gemini-native.go` L21-L54](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/channel/gemini/relay-gemini-native.go#L21-L54)、[L97-L110](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relay/channel/gemini/relay-gemini-native.go#L97-L110)。
- **[N11]** Gemini 图片 `inlineData` 转 OpenAI Markdown data URL（含流式）：[`relaykit/relayconvert/internal/gemini_chat/to_oai_chat_resp.go` L81-L158](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relaykit/relayconvert/internal/gemini_chat/to_oai_chat_resp.go#L81-L158)、[L188-L264](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relaykit/relayconvert/internal/gemini_chat/to_oai_chat_resp.go#L188-L264)。
- **[N12]** OpenAI→Gemini 响应只把消息文本放入 `parts[].text`，未解析 Markdown 图片：[`relaykit/relayconvert/internal/oai_chat/to_gemini_chat_resp.go` L51-L121](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relaykit/relayconvert/internal/oai_chat/to_gemini_chat_resp.go#L51-L121)、流式 [L126-L229](https://github.com/QuantumNous/new-api/blob/0bda086fb4c2f83ac0397e4a60d90686578d67ad/relaykit/relayconvert/internal/oai_chat/to_gemini_chat_resp.go#L126-L229)。
