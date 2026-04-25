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
