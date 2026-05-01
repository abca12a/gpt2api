# Documentation

## 记忆边界

- 本文件只记录当前状态、长期排查入口和最近仍有复用价值的阶段事实。
- 项目机器拓扑、SSH 角色和项目目录以根目录 `AGENTS.md` 的“项目连接信息”为唯一权威来源。
- 历史误判、回归原因和“不要再犯”的经验写入 `Corrections.md`。
- 面向外部调用方的接口细节写入 `docs/API_MANUAL.md`；下游 `new-api` / 前端协作细节写入 `docs/DOWNSTREAM_INTEGRATION.md`。

## 文档索引

- `AGENTS.md`：长期协作规则、机器拓扑、连接判断规则。
- `Prompt.md`：当前目标、业务约定、成功标准。
- `Plan.md`：当前维护计划、验收命令和文档边界。
- `Corrections.md`：纠正过的误解、踩坑记录和不要再犯的问题。
- `docs/API_MANUAL.md`：`gpt2api` 图片 API 对外手册。
- `docs/DOWNSTREAM_INTEGRATION.md`：下游 `new-api` 和前端对接文档。
- `docs/IMAGE_N4_DIAGNOSTICS.md`：`gpt-image-2 n=4` 少图问题的单账号、Runner 合并和 `/p/img` 回源三段诊断步骤。
- `deploy/README.md`：容器化部署、预编译、备份恢复。
- `scripts/README.md`：smoke、Codex auth 校验、`gpt-image-2` 真单联调工具。

## 当前事实

### 图片任务协议

- 图片生成默认建议走异步任务：`POST /v1/images/generations?async=true` 提交，`GET /v1/tasks/:id` 轮询。
- 异步提交返回 `{created, task_id, data: []}`，HTTP 状态保持 `200`，避免下游网关误判 `202`。
- `GET /v1/tasks/:id` 返回 OpenAI/Sora 风格 `image.task` 包装；`GET /v1/images/tasks/:id` 保持历史响应。
- 服务启动时会把启动前仍处于 `queued / dispatched / running` 的图片任务标记为 `interrupted`，避免部署或重启后下游长期轮询不到终态。
- 请求 `n` 最终按 `1..4` 处理；`n>1` 时服务端可能并发拆成多个单图任务，结果必须读取 `result.data[]`，不要用只保存首图的兼容字段判断是否缺图。

### gpt-image-2 路由

- 下游公网入口始终是 `https://lmage2.dimilinks.com/v1`；下游不要直接调用 `cliproxyapi` 公网域名或浏览器直连号池内部接口。
- `resolution` 规范化为 `1k / 2k / 4k`；缺省或无法识别时默认 `1k`，并在任务响应和 `provider_trace` 中回显。
- `1k` 策略：跳过外置图片渠道，走 strict `free runner`；trace 中会出现 `resolution_runner_only`。
- `2k/4k` 策略：外置渠道顺序强制为 `Codex -> APIMart`，外置渠道遇到可重试瞬态错误后允许回落 strict `free runner`。
- 生产 Codex image channel 为数据库中的 `codex-cli-proxy-image -> http://cli-proxy-api:8317`，映射 `gpt-image-2 -> gpt-image-2 / modality=image`。
- APIMart 第二跳使用官方 `gpt-image-2-official` 协议：`POST /v1/images/generations`、`resolution=1k/2k/4k`、`image_urls[]`、`mask_url`；`background=transparent` 在 APIMart 路径降级为 `auto`。
- 外置渠道返回 APIMart 异步任务格式时，号池适配器会自动轮询 `/v1/tasks/{task_id}` 并在超时边缘做一次短终态确认，减少“刚放弃上游就完成”的重复出图。
- `content_policy_violation / content_moderation / moderation_blocked / safety system` 归为内容安全，不兜底、不标记渠道 unhealthy，也不继续换渠道绕过。
- `400 invalid_value / image_generation_user_error / minimum pixel budget` 属于用户请求参数错误，返回 `invalid_request_error` 并保留详情，不切 free runner。

### 图片参数与结果

- 推荐传参是 `size=比例` + `resolution=1k/2k/4k`；也兼容 `image_size / scale / upscale / quality` 中的分辨率别名。
- 非正方形 `1k` 不能再按长边 1024 映射；示例：`16:9 -> 1536x864`、`9:16 -> 864x1536`、`2:3 -> 1024x1536`、`21:9 -> 1568x672`。
- Codex/native 路径宽高按 16 对齐，4K 档像素预算约为 `3840*2160`；正方形 `1:1/auto + 1k` 仍是 `1024x1024`。
- JSON 参考图兼容 `reference_images / images / image / image_url / image_urls / input_image / input_images`，支持字符串、字符串数组、`{"url":...}` 和对象数组；图片参数日志会记录 `reference_count`。
- 参考图唯一输入上限按当前实现控制为 4 张；解析失败要退款并直接 400，不创建残留 `image_tasks`。
- 图片任务优先返回本站 `/p/img/<task_id>/<idx>` HMAC 签名代理 URL，不直接暴露上游临时 `result_urls`；`data:image/...;base64` 大块结果也会改走代理。
- `/p/img` 签名密钥由服务端稳定 `JWT_SECRET` 派生；同一密钥下服务重启不影响新签 URL。签名过期、更换 `JWT_SECRET` 或 2026-05-01 修复前随机密钥签出的旧 URL 失效，属于预期。

### 下游集成与计费

- `docs/DOWNSTREAM_INTEGRATION.md` 是下游 `new-api` 后端和前端对接文档；当前对外是 `gpt2api` OpenAI 兼容图片接口，不是 OpenAI 官方 API。
- API Key 客户端/业务后端提交图片任务走下游 `POST /v1/images/generations?async=true`；登录态前端提交走下游同源 `POST /pg/images/generations?async=true`；两者轮询都走下游 `GET /v1/tasks/{task_id}`。
- 下游 `new-api` token 必须命中含 `gpt-image-2` 渠道的分组；错误里出现 `under group default` 时，请求通常还没进入 gpt2api。
- 下游 `new-api` 的 `Request ID` 不会原样透传成号池 request id；跨系统排查时，优先用下游公开 `task_id` 在 `tasks.private_data.upstream_task_id` 映射到号池 `img_*`。
- 用户侧价格真相源在下游后端：当前默认单价为 `1k=0`、`2k=0.06`、`4k=0.12` ⭐，按 `n` 放大并在任务创建时固化到 `billing_context`。
- 当前下游 `/api/pricing` 会返回 `gpt-image-2.resolution_options` 与 `pricing_version=gpt-image-2-resolution-v1`；后台仍可保留单个基础模型项 `gpt-image-2=0`，分档键仅作为覆盖项使用，不应当作多个真实模型。
- 号池内部 `models.image_price_per_call`、`image_tasks.estimated_credit/credit_cost` 和 `usage_logs.credit_cost` 是号池自身成本/余额语义，不是下游用户实际扣费依据。

### 账号与基础服务

- 账号池支持 `JSON / OAuth / AT / RT / ST` 五种导入方式；账号入库、AES 加密、自动刷新、额度探测、代理绑定共用 `internal/account` 逻辑。
- OAuth 导入只是获取账号凭据的新入口，不替换旧导入链路；默认回调仍优先使用 OpenAI/Codex 常用的 `http://localhost:1455/auth/callback`。
- OAuth 会话状态保存在服务端内存，TTL 为 30 分钟；服务重启或超时后需要重新生成授权链接。
- OAuth 的 `proxy_id` 同时用于服务端换 token 和新建账号默认代理绑定；更新已有账号时不会自动改绑原代理。
- 外置 Codex image channel 使用 `/home/ubuntu/CLIProxyAPI/auths/codex-*.json` 文件池，由 `cli-proxy-api` 独立调度；`gpt2api-server` 不直接把数据库 `oai_accounts` 当作外置 Codex 调度池。
- 外置 Codex auth 文件可以与数据库账号邮箱重合，但两边不是同一个队列：外置池消耗 Codex usage/credits，内置 ChatGPT Web Runner 消耗 Web 图片额度。
- 图片 runner 在 ChatRequirements、参考图上传、prepare、SSE、poll、download URL 阶段遇到上游 `401/403` 会标记账号 `dead`；`429` 仍走 throttled/cooldown。

### 可观测性与后台

- `image_tasks.provider_trace` 记录请求档位、首跳 provider、每一步尝试顺序、fallback 原因、最终 provider、上游 task/request id、错误层级和 timing。
- 管理端“生成记录”列表只返回任务元数据、结果数量和失败摘要；图片/失败详情懒加载，避免列表带回 `result_urls` 大字段。
- 管理端“Codex 今日”来自外置 CLIProxyAPI 日志，只统计请求数、成功数、失败数和 429 次数；不是官方 Codex 剩余 credits，也不等同 Web 图剩余。
- 后台生成记录不要使用 `el-image` 内置预览承载结果图；使用普通 `img` 与受控 `el-dialog`，避免 base64/data URL 误跳 `about:blank#blocked`。

### 部署边界

- 本项目 Dockerfile 是“宿主预编译 + 镜像复制产物”，镜像构建不会自动 `go build`。
- 部署前必须先更新 `deploy/bin/gpt2api`，推荐执行 `bash deploy/build-local.sh`；不能只跑 `docker compose build/up`。
- Alpine 镜像需要静态 linux/amd64 二进制；手工构建命令为 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o deploy/bin/gpt2api ./cmd/server`。
- `gpt2api-server` 与 `cli-proxy-api` 必须同在 Docker 网络 `deploy_default`；容器内 `cli-proxy-api` DNS 和 `/healthz` 要可用。
- `JWT_SECRET` 不能为空，否则服务启动失败；该密钥同时影响登录 JWT 和图片代理签名派生。

## 排查入口

- 慢单先分层：`new-api` 分组/任务状态、gpt2api `image_tasks`、外置 `codex-cli-proxy-image` 健康、APIMart、内置 runner、`/p/img` 回源/超分/保存链路分别看。
- 参考图不生效先看 gpt2api 图片参数日志的 `reference_count`：`0` 表示前端或 `new-api` 没传到号池，`>0` 后再查上传和上游生成效果。
- 图片裂图先看任务是否有 `file_ids` 和单图代理元信息；浏览器直接访问上游临时图链失败，通常不是任务生成失败。
- 多图缺失先查下游任务详情 `result.data[]` 和号池 `file_ids/result_urls` 数量；不要只看下游 `private_data.result_url`。
- `gpt-image-2 n=4` 少图优先按 `docs/IMAGE_N4_DIAGNOSTICS.md` 跑探针：默认 `single_run_once` 判断单账号单会话是否出满 4 个 `file_id`，`GPT2API_LIVE_SINGLE_ACCOUNT_N4_MODE=parallel` 判断正式 Runner 各 part 和最终 merge。
- 手机保存 `/p/img` 失败先看 HTTP 状态：`403` 多半是签名过期、密钥变更或旧随机签名；`502` 多半是代理回源或上游临时图下载问题。
- 账号 401/403 后仍被调度，先查账号状态是否已自动变 `dead`；如果没有，补充对应阶段日志再修状态回写。
- 修改 `deploy/nginx.conf` 后若容器内仍读取旧配置，优先重建 `gpt2api-nginx`，不要只依赖 `nginx -s reload`。

## 最近变更

- 2026-05-01：检查当日日志时发现 `chat-requirements raw body` 会记录上游 requirements token 原文，且 access log 会记录 `/p/img` 的 `sig` / `api_key` 等敏感 query；已改为 requirements 摘要日志和敏感 query 值脱敏，并部署到当前号池。
- 2026-05-01：已修复图片 runner 遇到上游 `401/403` 不标记账号 `dead` 的问题，并部署到当前号池；部署后已观察到 poll 阶段 `403` 会自动回写账号状态。
- 2026-05-01：新增 `gpt-image-2 n=4` 结构化诊断输出和 `scripts/gpt-image-2-n4-diagnose.sh`：Runner 并发 part 会记录账号、会话、file_id 数、首次失败和最终 merge 摘要；live 探针输出 `GPT2API_IMAGE_N4_DIAGNOSTIC_JSON`，用于区分单账号执行、结果合并和代理回源阶段。
- 2026-05-01：已修复 `/p/img` 签名随进程随机密钥重启失效的问题，签名密钥改为从稳定 `JWT_SECRET` 派生；新增重启后签名仍有效、换密钥后旧签名失效的回归测试。
- 2026-05-01：已确认 free runner 多图生产依赖并发拆单；单个 free 账号单次会话不应承诺稳定一次性出满 4 张。
- 2026-05-01：下游后端已把 `gpt-image-2` 分辨率计费改为按 `n` 放大；当前单价为 `1k=0`、`2k=0.06`、`4k=0.12` ⭐。
- 2026-05-01：当前仓库新增 `scripts/gpt-image-2-single-e2e.mjs`，用于新发或复核 `1k/2k/4k` 真单，支持区分当前展示价漂移和历史订单下单价不一致。
- 2026-05-01：号池已完成 `gpt-image-2` resolution 路由最小切片：`1k -> free runner`，`2k/4k -> Codex -> APIMart -> free runner`，并在响应和 `provider_trace` 回显 resolution。
- 2026-04-30：已修复 Codex 图片渠道在 CLIProxyAPI 中用工具模型选路的问题，选路改为 Responses 顶层主模型并禁用 free-tier auth 选择。
- 2026-04-30：已对齐 APIMart 最新 `gpt-image-2-official` 图片协议，并修复 APIMart 4K 比例请求继续发送不支持比例的问题。
- 2026-04-29：`image_tasks.provider_trace`、fallback 可配置策略、慢阶段 timing、账号级实时统计已部署，用于减少靠日志人工反推 provider 链路。

## 已清理内容

- 已删除旧文档中大量单次任务 ID、临时 token、即时数据库计数、重复 smoke 结果和已被后续结论覆盖的阶段流水。
- 早期 `0.12` 单价、`0.06/0.10/0.20` 过渡价、临时“只保留单模型不展开分档”的中间结论已折叠为当前事实。
- 机器拓扑与连接方式已收敛到 `AGENTS.md`；后续不要在本文件复制 SSH 详情，避免形成两套来源。
