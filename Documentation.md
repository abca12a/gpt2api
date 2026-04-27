# Documentation

## 记忆边界

- 本文件只记录当前状态、长期排查入口和最近仍有复用价值的阶段事实；一次性任务 ID、临时文件路径、即时数据库计数和 smoke 细节不再长期保存。
- 项目机器拓扑、SSH 角色和项目目录以根目录 `AGENTS.md` 的“项目连接信息”为唯一权威来源；本文件不重复保存连接详情。
- 历史误判、回归原因和“不要再犯”的经验写入 `Corrections.md`；实现细节和接口状态写在本文件。

## 当前事实

### 账号与基础服务

- 账号池支持 `JSON / OAuth / AT / RT / ST` 五种导入方式；账号入库、AES 加密、自动刷新、额度探测、代理绑定共用 `internal/account` 逻辑。
- OAuth 导入只是获取账号凭据的新入口，不替换旧导入链路；默认回调仍优先使用 OpenAI/Codex 常用的 `http://localhost:1455/auth/callback`。
- OAuth 会话状态保存在服务端内存，TTL 为 30 分钟；服务重启或超时后需要重新生成授权链接。
- OAuth 的 `proxy_id` 同时用于服务端换 token 和新建账号默认代理绑定；更新已有账号时不会自动改绑原代理。

### 图片任务协议

- 图片生成默认走异步任务：提交后保存 `task_id` 并轮询任务接口；`dispatched` 表示等待账号调度，拿到账号 lease 后才进入 `running`。
- `POST /v1/images/generations?async=true` 与 `Prefer: respond-async` 会按异步任务 body 返回，但 HTTP 状态保持 `200`，避免下游网关把 `202` 当上游错误。
- 默认异步提交返回 `{created, task_id, data: []}`；显式传 `compat=apimart`、`response_schema=apimart` 或 `X-Response-Format: apimart` 时，响应改为 APIMart 风格 `{code:200,data:[{status:"submitted",task_id}]}`。
- `GET /v1/tasks/:id` 返回 OpenAI/Sora 风格 `image.task` 包装；`GET /v1/images/tasks/:id` 保持历史响应。
- 服务启动时会把启动前仍处于 `queued / dispatched / running` 的图片任务标记为 `interrupted`，避免部署或重启后下游长期轮询不到终态。

### gpt-image-2 路由

- 下游公网入口始终是 `https://lmage2.dimilinks.com/v1`；下游不要直接调用 `cliproxyapi` 公网域名或浏览器直连号池内部接口。
- 生产 `gpt-image-2` 优先走数据库中的外置 image channel：`codex-cli-proxy-image -> http://cli-proxy-api:8317`，映射为 `gpt-image-2 -> gpt-image-2 / modality=image`。
- `gpt2api-server` 与 `cli-proxy-api` 必须同在 Docker 网络 `deploy_default`；容器内 `cli-proxy-api` DNS 和 `/health` 要可用。
- 外置 Codex/OpenAI image channel 在同渠道重试后若仍遇到 `502/5xx`、超时、EOF、connection reset、broken pipe 等瞬态错误，会回落内置 ChatGPT Web Runner，并强制调度 `plan_type=free` 账号。
- `content_policy_violation / content_moderation / moderation_blocked / safety system` 归为 `content_moderation`，不兜底、不标记渠道 unhealthy，也不继续换渠道绕过安全策略。
- `400 invalid_value / image_generation_user_error / minimum pixel budget` 属于用户请求参数错误，返回 `invalid_request_error` 并保留详情，不切 Free 账号。
- Free 账号可走内置 Web Runner 图片链路，但当前不具备/未暴露 Codex `image_generation` 工具，不能作为 Codex image channel 主力；轮换 Codex auth 后运行 `scripts/check-codex-auth-plans.sh`，避免 `*-free.json` 或未知后缀混入。

### 图片参数与结果

- APIMart 风格 `size=比例 + resolution=1k/2k/4k` 会转换为 Codex 可接受像素；非正方形 `1k` 不能按长边 1024 直接映射，当前示例为 `16:9 -> 1536x864`、`9:16 -> 864x1536`、`2:3 -> 1024x1536`、`21:9 -> 1568x672`。
- Codex 路径约束：最长边不超过 3840，宽高需 16 对齐，4K 档像素预算约等于 `3840*2160`；正方形 `1:1/auto + 1k` 仍是 `1024x1024`。
- JSON 参考图兼容 `reference_images / images / image / image_url / image_urls / input_image / input_images`，支持字符串、字符串数组、`{"url":...}` 和对象数组；图片参数日志会记录 `reference_count`。
- 参考图唯一输入上限仍按当前实现控制为 4 张；解析失败要退款并直接 400，不写入 `image_tasks` 残留任务。
- 内置 ChatGPT Web Runner 的 Azure 参考图 PUT 使用独立标准 HTTP/TLS transport，并对 EOF、timeout、5xx、408、429 做短重试；外置 `cli-proxy-api` 容器内部上传逻辑仍是黑盒。
- 请求 `n` 是最终落库和结算上限；如果上游 SSE 一次返回超出 `n` 的 sediment/file 引用，服务端会在落库前裁剪到请求数量。
- `N>1` 时每张图可能来自不同账号和 conversation；不要假设一个任务只有一个可下载全部图片的账号上下文。
- 对外 `ImageGenerations / ImageTask / ImageTaskCompat / ImageEdits / chat->image` 返回的 `/p/img` 代理图会按当前请求 `Host/X-Forwarded-Proto` 补成绝对 URL；`internal/image` 内部仍保留相对 path。
- 图片任务对前端展示优先返回本站 `/p/img/<task_id>/<idx>` 签名代理 URL，不直接暴露上游临时 `result_urls`；缺少 `file_ids` 的极老任务才兜底旧直链。

### 失败归因与后台展示

- OpenAI 兼容网关错误按 APIMart 常见类型归类：401=`authentication_error`、402=`payment_required`、429=`rate_limit_error`、5xx=`server_error/service_unavailable`；APIMart 兼容模式下 HTTP 错误的 `error.code` 改为数字状态码。
- ChatGPT Web Runner 会同时提取图片 SSE 和 conversation mapping 中最新 assistant 文本；如果上游没有给出图片引用但返回自然语言拒绝/说明，会写入 `image_tasks.error` detail。
- `GET /v1/tasks/:id` 失败时除 `error{code,message,detail}` 外，还返回顶层 `error_code / error_message / error_msg / message / error_detail / failure_reason / failed_reason / fail_reason`，方便下游不同读取路径展示原因。
- 管理员“生成记录”列表不再查询或返回 `image_tasks.result_urls` 大字段，只返回任务元数据、结果数量和失败摘要；点击“查看结果 / 查看失败”时再调用 `GET /api/admin/image-tasks/:id/images` 懒加载。
- 后台生成记录不要使用 `el-image` 内置预览承载结果图；使用普通 `img` 与受控 `el-dialog`，避免 base64/data URL 误跳 `about:blank#blocked`。

### 下游集成

- `docs/DOWNSTREAM_INTEGRATION.md` 是下游 `new-api` 后端和前端对接文档；当前对外是 `gpt2api -> chatgpt.com` Web 反代与本机 Codex image channel 组合路线，不是 OpenAI 官方 API 路线。
- API Key 客户端/业务后端提交图片任务走下游 `POST /v1/images/generations?async=true`；登录态前端提交走下游同源 `POST /pg/images/generations?async=true`；两者轮询都走下游 `GET /v1/tasks/{task_id}`。
- `/v1/tasks/batch` 当前不建议用于 `gpt-image-2`，因为号池未提供批量任务查询接口。
- 下游 `new-api` token 必须命中含 `gpt-image-2` 渠道的分组；错误里出现 `under group default` 时，请求通常还没进入 gpt2api。
- 下游用量日志中的 `LogTypeConsume / quota=0 / use_time=0 / 操作 textGenerate` 只是异步提交记录，不代表任务成功；最终状态看 `tasks.status/fail_reason`、错误日志和号池 `image_tasks.error`。
- 下游前端 `/console/logs` 已新增状态说明：图片失败日志显示“图像生成失败”和后端错误原因，异步提交日志显示“图像生成已提交”。

## 排查入口

- 慢单先分层：`new-api` 分组/任务状态、gpt2api `image_tasks`、外置 `codex-cli-proxy-image` 健康、内置 Runner、`/p/img` 回源/超分/保存链路分别看；不要只补账号。
- 成功任务 `created_at -> started_at` 等待时间正常但总体慢时，优先查外置渠道连续超时；无参考图外置最多等待 90 秒，有参考图最多等待 2 分钟，任务总超时随重试延长并封顶 15 分钟。
- `/p/img/<task>` 首次代理取图或超分可能额外消耗数秒到二十余秒，不计入 `image_tasks.finished_at`；区分“生成耗时”和“代理下载/超分/保存耗时”。
- 参考图不生效先看 gpt2api 图片参数日志的 `reference_count`：`0` 表示前端或 `new-api` 没传到号池，`>0` 后再查上传和上游生成效果。
- 图片裂图先看任务是否有 `file_ids` 和单图代理元信息；浏览器直接访问上游临时图链失败，通常不是任务生成失败。
- 修改 `deploy/nginx.conf` 后若容器内仍读取旧配置，优先重建 `gpt2api-nginx`，不要只依赖 `nginx -s reload`。
- CLIProxyAPI 管理界面当前经公网域名可访问，安全性依赖强管理密钥；若要改回仅本机可用，需要重新加 Nginx 层拦截。

## 最近变更

- 2026-04-27：外置图片渠道等待窗口收敛为无参考图 90 秒、有参考图 2 分钟；超时后尽快走内置 Runner 兜底，下游前端轮询窗口需覆盖到 15 分钟。
- 2026-04-27：对外 `/p/img` 代理图统一补绝对 URL，避免下游把相对路径补到错误域名后下载到 HTML。
- 2026-04-27：管理员“生成记录”改为轻量列表与懒加载图片/失败详情；不要把 base64/data URL 或 `result_urls` 大字段重新放回列表接口。
- 2026-04-27：内置 Runner 与外置图片渠道成功结果落库/结算前统一按请求 `n` 截断，防止上游多产出导致下游展示多图。
- 2026-04-27：新增 `scripts/check-codex-auth-plans.sh`，用于 Codex auth 导入/轮换后拦截 free 或未知 plan 文件。
- 2026-04-27：参考图上传的 Azure PUT/确认链路增加短重试；上传失败归类为 `network_transient`，不再直接当图片参数错误。
- 2026-04-27：下游前端 AI 画布已在构建期修正 `gpt-image-2` payload，把比例尺寸转像素尺寸、`1k/2k/4k` 改为 `resolution`，参考图发往 `/pg/images/generations` 前转为 `data:image`。

## 已清理内容

- 已删除 2026-04-25 至 2026-04-27 的大部分单次任务 ID、smoke 任务耗时、账号导入文件路径、临时容器/临时 token 细节和一次性数据库快照。
- 账号导入、SSE 超时、参考图解析、外置渠道接入、下游日志展示等历史流水已折叠到“当前事实”“排查入口”和 `Corrections.md`。
- 机器拓扑与连接方式已收敛到 `AGENTS.md`；后续不要在本文件复制 SSH 详情，避免形成两套来源。
