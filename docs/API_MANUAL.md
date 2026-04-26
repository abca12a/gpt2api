# gpt2api 图片 API 手册

更新时间：2026-04-26（Asia/Shanghai）
适用范围：`gpt2api` 对外图片接口、下游 `new-api` 后端、AI 创作前端。  
当前重点模型：`gpt-image-2`。

> 这份文档描述的是“我们自己的对外协议”。底层可能走 Codex 图片渠道、ChatGPT 号池 Runner 或其它外置 image channel；调用方不需要感知底层链路。

> 当前生产 `gpt-image-2` 优先依赖本机 `codex-cli-proxy-image` 外置 image channel：`gpt2api-server -> http://cli-proxy-api:8317 -> chatgpt.com/backend-api/codex/responses`。下游公网仍只调用 `https://lmage2.dimilinks.com/v1`，不要直接调用 `cliproxyapi.845817074.xyz`。

## 1. 接入总览

### 1.1 Base URL

公网推荐入口：

```text
https://lmage2.dimilinks.com/v1
```

注意：线上配置里的域名是 `lmage2.dimilinks.com`，首字符是小写 `l`。

### 1.2 认证

所有 `/v1/*` API 请求都必须带服务端 API Key：

```http
Authorization: Bearer <gpt2api API Key>
Content-Type: application/json
```

图片结果 URL `/p/img/...` 不需要 API Key；它依赖 URL 中的 `exp` 和 `sig` 做签名校验，默认有效期约 24 小时，服务重启后旧签名可能失效。

### 1.3 推荐调用链路

新接入建议统一使用异步链路：

```text
POST /v1/images/generations?async=true
GET  /v1/tasks/{task_id}
```

原因：

- 图片生成耗时通常几十秒到数分钟，同步请求容易被下游网关或浏览器超时影响。
- `/v1/tasks/{task_id}` 是新的兼容任务查询结构，适合前端统一轮询。
- 前端只需要处理 `queued`、`in_progress`、`succeeded`、`failed` 四类状态。

### 1.4 线上关键依赖

排查生产问题时先确认以下依赖，避免重复误判：

- `gpt2api` 服务：`gpt2api-server/mysql/redis/nginx` 正常，`/healthz` 返回 ok。
- Codex 图片渠道：`cli-proxy-api` 容器运行中，且与 `gpt2api-server` 同在 `deploy_default` 网络。
- 容器内解析：`gpt2api-server` 内 `cli-proxy-api` 能解析到容器 IP，并且 `http://cli-proxy-api:8317/health` 返回 ok。
- 数据库路由：`upstream_channels` 存在 `codex-cli-proxy-image`，`base_url=http://cli-proxy-api:8317`，`enabled=1`；`channel_model_mappings` 存在 `gpt-image-2 -> gpt-image-2 / modality=image / enabled=1`。
- 下游分组：`new-api` token 需要落在支持 `gpt-image-2` 的分组；如果错误是 `No available channel for model gpt-image-2 under group default`，说明请求通常还没进入 gpt2api。

快速核验：

```bash
docker compose -f deploy/docker-compose.yml ps
curl -fsS http://127.0.0.1:8080/healthz
docker exec gpt2api-server sh -c 'getent hosts cli-proxy-api && wget -qO- --timeout=3 http://cli-proxy-api:8317/health'
```

## 2. 路由列表

| 方法 | 路径 | 说明 | 推荐程度 |
|---|---|---|---|
| `GET` | `/v1/models` | 查询可用模型 | 推荐 |
| `POST` | `/v1/images/generations` | 文生图；也支持 JSON 参考图扩展 | 推荐 |
| `POST` | `/v1/images/edits` | multipart 图生图/编辑 | 可用 |
| `GET` | `/v1/tasks/{task_id}` | OpenAI/Sora 风格任务查询 | 推荐 |
| `GET` | `/v1/images/tasks/{task_id}` | 历史任务查询结构 | 兼容旧客户端 |
| `GET` | `/p/img/{task_id}/{idx}?exp=...&sig=...` | 图片代理访问 | 前端展示用 |

## 3. 查询模型

### 请求

```bash
curl https://lmage2.dimilinks.com/v1/models \
  -H "Authorization: Bearer $GPT2API_KEY"
```

### 响应

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-image-2",
      "object": "model",
      "created": 1777080000,
      "owned_by": "chatgpt"
    }
  ]
}
```

前端可默认选择 `gpt-image-2`，但后台配置页或诊断页建议保留 `/v1/models` 校验能力。

## 4. 文生图：JSON generations

### 4.1 同步请求

```http
POST /v1/images/generations
```

同步模式会阻塞到图片完成后返回，不建议前端直连使用。

```bash
curl https://lmage2.dimilinks.com/v1/images/generations \
  -H "Authorization: Bearer $GPT2API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "一张赛博朋克风格的猫咪海报，霓虹灯，电影感",
    "n": 1,
    "size": "16:9",
    "resolution": "2k",
    "output_format": "png"
  }'
```

成功响应：

```json
{
  "created": 1777080000,
  "task_id": "img_xxx",
  "data": [
    {
      "url": "/p/img/img_xxx/0?exp=1777166400000&sig=...",
      "file_id": "file_xxx"
    }
  ]
}
```

### 4.2 异步请求（推荐）

以下方式都会进入异步模式：

- 查询参数：`?async=true`
- 查询参数：`?wait_for_result=false`
- 请求头：`Prefer: respond-async`
- 请求体：`"wait_for_result": false`
- APIMart 兼容模式：`compat=apimart` 等

推荐请求：

```bash
curl "https://lmage2.dimilinks.com/v1/images/generations?async=true" \
  -H "Authorization: Bearer $GPT2API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "高端香水产品摄影，黑色背景，金色反光，商业广告质感",
    "n": 1,
    "size": "1:1",
    "resolution": "4k",
    "output_format": "png"
  }'
```

默认异步提交响应：

```json
{
  "created": 1777080000,
  "task_id": "img_xxx",
  "data": []
}
```

## 5. 图生图：JSON 参考图 generations

我们在 `/v1/images/generations` 上扩展了参考图字段，便于前端和下游网关不切 multipart 也能做图生图。

### 5.1 推荐字段

优先使用：

```json
"image_urls": ["https://example.com/ref.png"]
```

也兼容以下别名：

| 字段 | 类型 | 说明 |
|---|---|---|
| `reference_images` | string / array / object | 早期内部字段 |
| `images` | string / array / object | APIMart/OpenAI 风格兼容 |
| `image` | string / array / object | 单图兼容 |
| `image_url` | string / array / object | 单图 URL 兼容 |
| `image_urls` | string / array / object | 推荐字段 |
| `input_image` | string / array / object | 下游兼容 |
| `input_images` | string / array / object | 下游兼容 |

每个参考图值支持：

```json
"https://example.com/ref.png"
```

```json
"data:image/png;base64,iVBORw0KGgo..."
```

```json
{ "url": "https://example.com/ref.png" }
```

```json
[
  { "url": "https://example.com/ref-1.png" },
  { "url": "data:image/jpeg;base64,/9j/4AAQ..." }
]
```

### 5.2 请求示例

```bash
curl "https://lmage2.dimilinks.com/v1/images/generations?async=true" \
  -H "Authorization: Bearer $GPT2API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "保持人物主体一致，改成海边日落电影写真风格",
    "n": 1,
    "size": "16:9",
    "resolution": "4k",
    "image_urls": [
      "https://example.com/reference.png"
    ]
  }'
```

### 5.3 限制

- 同一次请求最多 4 张参考图。
- 单张参考图最大 20 MB。
- 支持 HTTP(S) URL、data URL、纯 base64。
- 参考图必须能被 `gpt2api` 后端访问；浏览器本地 blob URL 不能直接传。

## 6. 图像编辑：multipart edits

当前 `/v1/images/edits` 按同步接口处理；如果前端需要异步图生图，优先使用 `POST /v1/images/generations?async=true` 搭配 `image_urls`。

### 6.1 请求

```http
POST /v1/images/edits
Content-Type: multipart/form-data
```

至少上传一张 `image` 文件。

```bash
curl https://lmage2.dimilinks.com/v1/images/edits \
  -H "Authorization: Bearer $GPT2API_KEY" \
  -F "model=gpt-image-2" \
  -F "prompt=把图片改成复古胶片风格，保留主体" \
  -F "size=16:9" \
  -F "resolution=2k" \
  -F "image=@/path/to/reference.png"
```

多图写法：

```bash
curl https://lmage2.dimilinks.com/v1/images/edits \
  -H "Authorization: Bearer $GPT2API_KEY" \
  -F "model=gpt-image-2" \
  -F "prompt=融合两张参考图的风格，生成电商主图" \
  -F "size=1:1" \
  -F "resolution=4k" \
  -F "image[]=@/path/to/ref-1.png" \
  -F "image[]=@/path/to/ref-2.png"
```

兼容文件字段：

| 字段 | 说明 |
|---|---|
| `image` | 单文件，推荐 |
| `image[]` | 多文件 |
| `images` | 多文件兼容 |
| `images[]` | 多文件兼容 |
| `image_1`、`image_2` | 编号字段兼容 |
| `mask` | 可选；当前会作为参考图一并上传，上游暂不严格区分 mask |

## 7. 任务查询

### 7.1 推荐路径：`GET /v1/tasks/{task_id}`

```bash
curl https://lmage2.dimilinks.com/v1/tasks/img_xxx \
  -H "Authorization: Bearer $GPT2API_KEY"
```

进行中响应：

```json
{
  "id": "img_xxx",
  "task_id": "img_xxx",
  "object": "image.task",
  "status": "in_progress",
  "progress": 50,
  "created_at": 1777080000
}
```

成功响应：

```json
{
  "id": "img_xxx",
  "task_id": "img_xxx",
  "object": "image.task",
  "status": "succeeded",
  "progress": 100,
  "created_at": 1777080000,
  "completed_at": 1777080060,
  "result": {
    "created": 1777080000,
    "data": [
      {
        "url": "/p/img/img_xxx/0?exp=1777166400000&sig=...",
        "file_id": "file_xxx"
      }
    ]
  }
}
```

失败响应：

```json
{
  "id": "img_xxx",
  "task_id": "img_xxx",
  "object": "image.task",
  "status": "failed",
  "progress": 100,
  "created_at": 1777080000,
  "completed_at": 1777080060,
  "error": {
    "code": "upstream_error",
    "message": "上游返回错误"
  }
}
```

状态映射：

| 内部状态 | `/v1/tasks` 状态 | 前端建议 |
|---|---|---|
| `queued` / `dispatched` | `queued` | 显示排队中 |
| `running` | `in_progress` | 显示生成中 |
| `success` | `succeeded` | 展示 `result.data` |
| `failed` | `failed` | 展示错误并允许重试 |

### 7.2 历史路径：`GET /v1/images/tasks/{task_id}`

该路径保留给旧客户端，响应结构如下：

```json
{
  "task_id": "img_xxx",
  "status": "success",
  "conversation_id": "conv_xxx",
  "created": 1777080000,
  "finished_at": 1777080060,
  "error": "",
  "credit_cost": 123,
  "data": [
    {
      "url": "/p/img/img_xxx/0?exp=1777166400000&sig=...",
      "file_id": "file_xxx"
    }
  ]
}
```

新前端不要混用两个查询结构，建议只接 `/v1/tasks/{task_id}`。

## 8. 尺寸和清晰度

### 8.1 推荐传参方式

推荐前端拆成两个控件：

```json
{
  "size": "16:9",
  "resolution": "2k"
}
```

含义：

- `size`：画幅比例，支持 `1:1`、`16:9` 等。
- `resolution`：目标清晰度档位，支持 `1k`、`2k`、`4k`。

也支持直接传具体像素：

```json
{
  "size": "3840x2160"
}
```

但为了前端统一交互和渠道兼容，推荐使用“比例 + resolution”。

### 8.2 分辨率别名

以下字段都可以表达清晰度档位，但推荐只用 `resolution`：

| 字段 | 示例 | 说明 |
|---|---|---|
| `resolution` | `1k` / `2k` / `4k` | 推荐 |
| `image_size` | `2k` | 兼容别名 |
| `scale` | `4k` | 兼容别名 |
| `upscale` | `2k` / `4k` | 兼容别名 |
| `quality` | `2k` / `4k` | 兼容旧前端，不建议再用来表示清晰度 |

识别规则：

| 档位 | 识别值 |
|---|---|
| `1k` | `1k`、`1024p`、`1024` |
| `2k` | `2k`、`1440p` |
| `4k` | `4k`、`uhd`、`2160p` |

### 8.3 当前尺寸映射表

当 `size` 是比例且命中 Codex/native 图片渠道时，会映射成具体像素。宽高按 16 像素对齐；4K 档控制在约 `3840x2160` 像素预算内，所以正方形 4K 是 `2880x2880`，不是 `3840x3840`。1K 非正方形会主动提高到至少约 100 万像素且长边不低于 1536，避免 Codex 上游报 `below the current minimum pixel budget`，所以 `16:9+1k` 是 `1536x864`，不是 `1024x576`。

| `size` | 1K | 2K | 4K |
|---|---:|---:|---:|
| `auto` / `1:1` | `1024x1024` | `2048x2048` | `2880x2880` |
| `3:2` | `1536x1024` | `2016x1344` | `3504x2336` |
| `2:3` | `1024x1536` | `1344x2016` | `2336x3504` |
| `4:3` | `1536x1152` | `2048x1536` | `3264x2448` |
| `3:4` | `1152x1536` | `1536x2048` | `2448x3264` |
| `5:4` | `1600x1280` | `2000x1600` | `3200x2560` |
| `4:5` | `1280x1600` | `1600x2000` | `2560x3200` |
| `16:9` | `1536x864` | `2048x1152` | `3840x2160` |
| `9:16` | `864x1536` | `1152x2048` | `2160x3840` |
| `2:1` | `1536x768` | `2048x1024` | `3840x1920` |
| `1:2` | `768x1536` | `1024x2048` | `1920x3840` |
| `21:9` | `1568x672` | `2016x864` | `3808x1632` |
| `9:21` | `672x1568` | `864x2016` | `1632x3808` |

### 8.4 前端尺寸控件建议

推荐给用户暴露：

- 画幅：`1:1`、`3:2`、`2:3`、`4:3`、`3:4`、`16:9`、`9:16`、`21:9`、`9:21`。
- 清晰度：`1k`、`2k`、`4k`。
- 默认值：`size=1:1`、`resolution=2k`。
- 专业模式再开放具体像素 `WxH`。

## 9. 参数说明

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---:|---|
| `model` | string | `gpt-image-2` | 模型 slug；必须在 API Key 模型白名单内 |
| `prompt` | string | 必填 | 生图/编辑提示词，不能为空 |
| `n` | int | `1` | 小于等于 0 会归一为 1；大于 4 会夹到 4 |
| `size` | string | `1024x1024` | 支持 `WxH`、比例 `W:H`、`auto` |
| `resolution` | string | 空 | 推荐清晰度字段：`1k` / `2k` / `4k` |
| `image_size` | string | 空 | 清晰度兼容别名 |
| `scale` | string | 空 | 清晰度兼容别名 |
| `upscale` | string | 空 | 清晰度/超分兼容别名 |
| `quality` | string | 空 | 透传质量参数；也兼容旧的 `2k/4k` 写法 |
| `style` | string | 空 | 透传风格参数，主要给兼容渠道使用 |
| `response_format` | string | 空 | 兼容 `url` / `b64_json`；当前客户端统一按 `data[].url` 处理 |
| `output_format` | string | 空 | 透传输出格式，建议 `png` / `jpeg` / `webp` |
| `output_compression` | int | 空 | 透传压缩质量，通常配合 `jpeg` / `webp` |
| `background` | string | 空 | 透传背景参数，如 `auto`、`transparent`、`opaque` |
| `moderation` | string | 空 | 透传审核强度，如 `auto`、`low` |
| `user` | string | 空 | 调用方用户标识，便于审计 |
| `wait_for_result` | bool | `true` | `false` 表示异步提交，仅返回 `task_id` |
| `image_urls` 等 | string/list/object | 空 | JSON 参考图字段，最多 4 张 |

## 10. APIMart 兼容模式

我们的接口不是完全等于 APIMart 官方文档，但保留了常用兼容入口。

### 10.1 开启方式

任一方式命中即可：

```text
?compat=apimart
?response_schema=apimart
?schema=apimart
?format=apimart
?apimart=true
```

或请求头：

```http
X-Response-Format: apimart
X-API-Format: apimart
X-Compat-Mode: apimart
```

### 10.2 异步提交响应

```bash
curl "https://lmage2.dimilinks.com/v1/images/generations?async=true&compat=apimart" \
  -H "Authorization: Bearer $GPT2API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "一张未来城市概念图",
    "size": "16:9",
    "resolution": "2k"
  }'
```

响应：

```json
{
  "code": 200,
  "data": [
    {
      "status": "submitted",
      "task_id": "img_xxx"
    }
  ]
}
```

### 10.3 与 APIMart 文档的主要差异

| 项目 | 我们当前行为 |
|---|---|
| 异步提交 | 默认 `{created, task_id, data: []}`；APIMart 模式才是 `{code,data}` |
| 任务查询 | 推荐 `/v1/tasks/{task_id}`，状态为 `queued/in_progress/succeeded/failed` |
| 参考图数量 | 当前最多 4 张 |
| 图片 URL | 可能返回相对路径 `/p/img/...`，前端需补齐 origin |
| `n` | 接口接受 1-4；Codex/native 渠道当前建议前端按 `n=1` 设计 |
| `response_format` | GPT Image 链路统一建议消费 `data[].url`，不要依赖 `b64_json` |

## 11. 错误格式

### 11.1 默认 OpenAI 风格

```json
{
  "error": {
    "message": "prompt 不能为空",
    "type": "invalid_request_error",
    "code": "invalid_request_error"
  }
}
```

常见 HTTP 状态：

| HTTP | code | 含义 | 前端建议 |
|---:|---|---|---|
| 400 | `invalid_request_error` / `invalid_reference_image` | 参数错误、prompt 空、参考图异常 | 提示用户修改参数 |
| 400 / failed | `content_moderation` | 上游内容安全策略拒绝 | 提示用户调整 prompt / 参考图，不按系统故障重试 |
| 401 | `missing_api_key` / `invalid_api_key` | API Key 缺失或错误 | 检查后端配置 |
| 403 | `model_not_allowed` | Key 无模型权限 | 检查模型白名单 |
| 402 | `insufficient_balance` | 余额不足 | 提示充值或联系管理员 |
| 404 | `not_found` | 任务不存在或不属于当前用户 | 停止轮询 |
| 429 | `rate_limit_rpm` | RPM 限流 | 降低并发，稍后重试 |
| 502/503 | `upstream_error` / `service_unavailable` | 上游、Codex image channel 或账号池不可用；异步任务会把上游详情拼到 `error.message` | 记录 `error.message`，先查 `cli-proxy-api` 和渠道状态，再决定重试 |
| 500 | `internal_error` / `billing_error` | 服务内部异常 | 上报日志 |

### 11.2 APIMart 兼容错误

APIMart 兼容模式下仍是 `error` 包装，但 `code` 会变成 HTTP 状态码：

```json
{
  "error": {
    "message": "参数错误：size 不合法 / 4K 比例不支持 / 像素违规等",
    "type": "invalid_request_error",
    "code": 400
  }
}
```

## 12. 前端接入建议

### 12.1 请求由后端代理

浏览器不要直接持有 `gpt2api API Key`。推荐链路：

```text
AI 创作前端 -> new-api 后端 -> gpt2api -> 上游图片渠道
```

`new-api` 后端负责：

- 保存 `gpt2api API Key`。
- 转发用户 prompt、尺寸、参考图。
- 做用户鉴权、计费、内容策略和限流。
- 统一把 `/p/img/...` 相对路径补成完整 URL。

### 12.2 前端推荐请求体

文生图：

```json
{
  "model": "gpt-image-2",
  "prompt": "用户输入的提示词",
  "n": 1,
  "size": "16:9",
  "resolution": "2k",
  "output_format": "png",
  "wait_for_result": false
}
```

图生图：

```json
{
  "model": "gpt-image-2",
  "prompt": "基于参考图生成新图",
  "n": 1,
  "size": "16:9",
  "resolution": "2k",
  "image_urls": ["data:image/png;base64,..."],
  "wait_for_result": false
}
```

### 12.3 轮询策略

建议：

- 提交后每 2-3 秒轮询一次 `/v1/tasks/{task_id}`。
- 最长轮询 8-10 分钟。
- `succeeded` 后展示 `result.data[].url`。
- 如果 URL 以 `/p/img/` 开头，前端或后端补成 `https://lmage2.dimilinks.com/p/img/...`。
- `failed` 或 404 后停止轮询并允许用户重试。

示例：

```ts
const GPT2API_ORIGIN = 'https://lmage2.dimilinks.com'

function normalizeImageUrl(url: string) {
  return new URL(url, GPT2API_ORIGIN).toString()
}
```

### 12.4 不要再犯的点

- 不要把 `gpt2api API Key` 下发到浏览器。
- 不要只改前端枚举而忘记后端转发 `size/resolution/image_urls`。
- 不要把浏览器 `blob:` URL 直接传给 gpt2api；要先上传到后端或转成 data URL。
- 不要默认 `n>1`；当前 Codex/native 渠道按 `n=1` 设计最稳。
- 不要把 `/v1/images/tasks/{id}` 和 `/v1/tasks/{id}` 的响应结构混在一起。
- 不要把 `model_limits=gpt-image-2` 当成完整授权；`new-api` token 分组也必须能命中 `gpt-image-2` 渠道。
- 不要把 `reference_count=0` 的问题归因到上游生成效果；这通常说明参考图没有转发到 gpt2api。

## 13. 已验证能力快照

截至 2026-04-26，当前链路已验证：

- 文生图：`gpt-image-2` 可提交、轮询、取图。
- JSON 图生图：`image_urls` / `reference_images` 等字段可携带参考图。
- multipart 图生图：`/v1/images/edits` 可上传参考图。
- 尺寸：`auto`、`1:1`、`3:2`、`2:3`、`4:3`、`3:4`、`5:4`、`4:5`、`16:9`、`9:16`、`2:1`、`1:2`、`21:9`、`9:21` 均完成过提交、轮询、取图验证。
- 内容：中文场景、英文场景、产品图、文字海报、人像、建筑等类型均完成过测试。
- 2K/4K：Codex/native 图片渠道已验证文生图和参考图链路能返回 2K/4K 档结果。
- 1K 非正方形：`16:9+1k` 已验证映射为 `1536x864` 并成功返回，避免旧的 `1024x576` 最小像素预算错误。

仍需前端按产品策略处理：

- 4K 生成耗时更长，失败重试成本更高。
- 输出真实格式以上游返回为准，前端展示优先信图片实际字节/浏览器解码结果。
- `n=1` 是当前最稳的产品默认值。
