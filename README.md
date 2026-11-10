# new-api → CLIProxyAPI 图片转换 sidecar

在 OpenAI `/v1/chat/completions` 请求进入 CLIProxyAPI 前，将白名单域名的远程 `image_url` 下载为原始图片字节，并替换为 `data:<MIME>;base64,...`。只处理请求图片，不改写模型响应。

```text
客户端 → new-api → image-sidecar → CLIProxyAPI → 模型上游
```

## 部署

### 已有两个服务

保留现有 new-api、CLIProxyAPI 配置和认证目录，只把 `docker-compose.yml` 中的 `image-sidecar` 服务合并到同一 Compose。两个现有服务必须与 sidecar 使用同一容器网络。

在 `.env` 中设置：

```dotenv
IMAGE_HOST_ALLOWLIST=images.example.com
CLIPROXYAPI_URL=http://cli-proxy-api:8317
```

`CLIPROXYAPI_URL` 也可以使用 Compose 环境变量直接设置。白名单只接受逗号分隔的**精确主机名**，不要填写 scheme、路径、端口或通配符；每个重定向目标域名也必须单独列入。

示例请求需要：

```dotenv
IMAGE_HOST_ALLOWLIST=pgw.udn.com.tw,oss-cdn.i2tools.com
```

然后构建并启动：

```sh
docker compose up -d --build image-sidecar
```

### 使用完整三服务示例

1. `cp .env.example .env`。
2. 在 `.env` 中设置两个独立随机密钥和图片域名白名单。可用 `openssl rand -hex 32` 生成随机值。
3. 将现有 CLIProxyAPI 认证文件放入 `./auths`，或把 Compose 中该卷改为已有认证目录。全新空目录不代表已经完成模型授权。
4. 执行 `docker compose up -d --build`。

Compose 只把 new-api 的 `${NEW_API_PORT:-3000}` 发布到宿主机；CLIProxyAPI 和 sidecar 只在容器内网暴露。

## new-api 渠道

| 字段 | 值 |
|---|---|
| 类型 | `OpenAI`（类型 1） |
| Base URL | `http://image-sidecar:8080`，不带 `/v1` 或末尾 `/` |
| API Key | 与 `CLIPROXYAPI_API_KEY` 相同 |
| 模型 | `gemini-3.1-flash-image` |
| 模型映射 | 同名直转，留空 |

客户端仍请求 new-api 的 `/v1/chat/completions`，使用 new-api 签发的客户端 Key；原请求结构无需修改。

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | sidecar 监听地址 |
| `CLIPROXYAPI_URL` | `http://cli-proxy-api:8317` | CLIProxyAPI 根地址，不带路径或查询 |
| `IMAGE_HOST_ALLOWLIST` | 无；Compose 占位为 `images.example.com` | 必填，逗号分隔的精确图片域名 |

## 转换与失败语义

- 只遍历 `messages[].content[]` 中的 `image_url`；文本、图片顺序、`detail`、模型和其他字段保持不变。
- 已有 `data:` URL 原样保留。HTTP/HTTPS 图片以原始字节 Base64 编码，不解码、不缩放、不压缩。
- 支持 JPEG、PNG、WebP、HEIC、HEIF；响应声明与实际 sniff 结果不一致时拒绝。
- 只允许白名单域名和公网 IP；阻止私网、回环、链路本地、保留地址及 DNS 重绑定。
- 图片请求不继承模型 API Key、Authorization 或 Cookie；签名 URL query 可以下载，但不写入日志。
- 其他 HTTP 路径直接反向代理；SSE 响应逐块透传。
- 任一图片失败都在调用 CLIProxyAPI 前终止整个请求，不删图、不降级。

默认限制定义在 `sidecar.go`：入站 JSON 32 MiB、8 张远程图片、单图 10 MiB、图片原始字节合计 20 MiB、4 路下载并发、单图 15 秒、整批 45 秒、最多 5 次重定向。Base64 后的出站体积会更大。

常见错误：

| HTTP | 错误码 | 原因 |
|---|---|---|
| 400 | `invalid_image_url` | scheme、URL 或用户信息不合法 |
| 400 | `image_host_not_allowed` | 初始或重定向域名不在白名单 |
| 400 | `image_target_not_allowed` | 域名解析到非公网地址 |
| 400 | `unsupported_image_type` | 类型不支持或内容与声明不符 |
| 413 | `image_too_large` / `images_too_large` | 单图或总字节超限 |
| 502 | `image_download_failed` | 图片服务器非成功状态或网络失败 |
| 504 | `image_download_timeout` | 图片下载或整批处理超时 |

## 验证

```sh
go test ./...
# 没有本机 Go 时，镜像构建会在 builder 阶段运行同一测试集：
docker compose build image-sidecar
```

CLIProxyAPI 的 Gemini 转换路径对 data URL 的源码依据与调研记录见 `new-api-cliproxyapi-research.md`。
