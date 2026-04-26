# Corrections

## 出图快速换号

- 2026-04-25 修正：不能因为 SSE 已结束且缺少 `image_gen_task_id`、缺少图片引用就立即判定失败；生产任务 `img_5cf852f2b9724e1daeb9dabd` 因此 22 秒内三次换号后失败。
- 正确做法：这种情况只能说明上游可能未真正受理生图任务，应先做短 Poll（当前 20 秒）给 conversation mapping 一个补出 tool/image 消息的机会；短 Poll 仍无图时再暂停该账号并换号。
- 边界：已有 `image_gen_task_id` 或已有任意 file/sediment 引用时，继续使用常规 Poll 窗口，不走短 Poll。
## SSE 读取超时

- 2026-04-25 修正：不能只给 `ImageConvOpts.SSETimeout` 赋值就认为图片 SSE 有读超时；此前 `parseSSE` 忽略 timeout 参数，连接静默时任务仍可能长期停留 `running`。
- 正确做法：`parseSSE` 必须按单次事件读取设置 timeout，超时发出 `sse read timeout` 错误并关闭事件流，让 Runner 进入换号或失败流程。
- 边界：这个 timeout 是“事件间隔静默超时”，不是整次图片任务总耗时；总耗时仍由 `PerAttemptTimeout / PollMaxWait / MaxAttempts` 控制。

## 参考图参数错误残留任务

- 2026-04-25 修正：`/v1/images/generations` 不能在 JSON 参考图解析前创建 `image_tasks`；5 张唯一参考图会返回 400，但旧代码已先落 `dispatched`，导致无人执行的残留任务。
- 正确做法：鉴权、模型、限流、计费预扣后，先 `decodeReferenceInputs`，只有解析成功才创建任务；解析失败要退款并直接 400，不写任务表。
- 边界：`referenceInputs()` 会去重，测试“超过 4 张”时必须使用 5 个不同输入，5 个完全相同的 data URL 会被折叠成 1 张。

## Alpine 容器二进制

- 2026-04-25 修正：部署到 `gpt2api/server` Alpine 镜像前，不能用默认 CGO 动态链接二进制覆盖 `deploy/bin/gpt2api`；容器会报 `/app/gpt2api: cannot execute: required file not found`。
- 正确做法：使用 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o deploy/bin/gpt2api ./cmd/server` 生成静态二进制，再 `docker compose -f deploy/docker-compose.yml up -d --build server`。

## gpt-image-2 生产依赖

- 2026-04-26 修正：不能再把当前 `gpt-image-2` 生产路径简单说成“只走 `gpt2api -> chatgpt.com` Web Runner”，也不能因为公网不让下游直连 `cliproxyapi` 就忽略本机 `cli-proxy-api` 依赖。
- 正确做法：公网入口始终是 `https://lmage2.dimilinks.com/v1`；但 gpt2api 内部优先走数据库里的 `codex-cli-proxy-image` 外置 image channel，容器内 base URL 是 `http://cli-proxy-api:8317`，映射必须是 `gpt-image-2 -> gpt-image-2 / modality=image`。
- 边界：只有没有启用 image route 时才回退内置 ChatGPT Web Runner；一旦命中 route，`502 / stream disconnected / EOF / timeout` 属于渠道链路问题，先查 `cli-proxy-api` 容器、Docker 网络和渠道健康状态。

## 外置图片渠道瞬态兜底

- 2026-04-26 修正：不能只把 HTTP 502 当成外置图片渠道可兜底错误；生产 24 小时失败样本还包含 HTTP 500 `INTERNAL_ERROR`、`context deadline exceeded`、EOF 等同类瞬态断流。
- 正确做法：外置 Codex/OpenAI image channel 同渠道重试后，如果最后错误是 `502/5xx`、超时、EOF、connection reset/broken pipe 等瞬态错误，应转入内置 ChatGPT Web Runner，并强制 `plan_type=free`；异步兜底必须新建 fallback ctx，不能复用已被外置渠道耗尽的 7 分钟 ctx。
- 边界：`content_policy_violation / safety system` 仍归为 `content_moderation`，不兜底；`400 invalid_value / image_generation_user_error / minimum pixel budget` 属于用户请求参数错误，返回 `invalid_request_error` 并保留详情，不标记渠道 unhealthy，也不切 Free 账号。

## 图片失败对话诊断

- 2026-04-26 修正：不能只看图片接口结构化错误；ChatGPT Web Runner 兜底时，上游可能在 assistant 文本里解释拒绝原因但不产出图片 tool/file 引用，旧后台只会看到 `poll_error / poll_timeout / upstream_error`。
- 正确做法：图片 SSE 和 conversation mapping 都要提取最新 assistant 文本，失败时写入 `image_tasks.error` detail；后台必须直接展示并支持复制完整错误。
- 边界：只有出现安全/政策/未成年/内容审核等明确文本信号时才归为 `content_moderation`；普通 `poll_timeout / poll_error` 仍保留为上游未产出图片引用，不能凭空推断违规。

## new-api 分组依赖

- 2026-04-26 修正：不能只看 token 的 `model_limits=gpt-image-2` 就认为下游已授权成功；用户 `1540/HMJ` 曾因 token `group` 为空落到 `default` 分组，`new-api` 直接报 `No available channel for model gpt-image-2 under group default`。
- 正确做法：排查下游 503 时先确认 `new-api` token 的 `group` 能命中 `gpt-image-2` 渠道；如果错误里出现 `under group default`，请求通常还没进入 gpt2api。
- 边界：gpt2api 侧日志、`image_tasks` 和渠道错误只能解释已经进入 gpt2api 的请求；没进 gpt2api 的分组错误要在 `new-api` 数据库或后台修。

## Codex 1K 尺寸预算

- 2026-04-26 修正：不能再把非正方形 `1k` 比例图按长边 1024 直接映射，例如 `16:9 -> 1024x576`；Codex 上游会报低于最小像素预算。
- 正确做法：非正方形 1K 要保证约 100 万像素且长边不低于 1536，当前映射示例为 `16:9 -> 1536x864`、`9:16 -> 864x1536`、`2:3 -> 1024x1536`、`21:9 -> 1568x672`。
- 边界：正方形 `1:1/auto + 1k` 仍是 `1024x1024`；2K/4K 继续按 16 对齐和 4K 像素预算映射。

## 2026-04-26 管理员生成记录图片跳转

- 错误理解：只移除缩略图外层 `<a target="_blank">` 就能阻止跳转。
- 事实：`el-image` 的内置预览仍可能围绕 base64/data URL 触发浏览器导航/阻拦，表现为 `about:blank#blocked`。
- 纠正：后台生成记录不要用 `el-image` 内置预览承载结果图；改用普通 `img` 和受控 `el-dialog` 大图弹窗，点击事件必须 `stop/prevent`，且不暴露任何可导航链接。

## 下游机器拓扑

- 2026-04-26 修正：不能因为当前 Codex 所在环境没有本地 new-api 目录，就说“本机没有下游 new-api 服务/源码”；下游后端源码与运行服务在 `212.50.232.214:/root/new-api`，SSH 使用 `root@212.50.232.214 -p 22222`。
- 正确做法：排查下游后端是否认可任务失败原因时，直接登录 `212.50.232.214` 查 `/root/new-api`、`new-api-postgres-local.tasks.fail_reason` 和 `service/task_polling.go` / `relay/channel/task/sora/adaptor.go`。
- 边界：`43.161.219.135` 是前端服务器，当前本机还没有 root SSH 权限；只能先通过公网 HTTP 静态包或 `212.50.232.214` 上的前端源码/构建目录交叉判断，不能假装已登录 43。
