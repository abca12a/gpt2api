# Corrections

## 使用边界

- 本文件只记录纠正过的理解、踩坑记录和“不要再犯”的判断；当前实现状态看 `Documentation.md`，机器拓扑看 `AGENTS.md`。
- 不把一次性任务 ID、临时排查命令、即时统计数写成长期事实；需要回溯时查 Git、线上日志或数据库。

## 图片任务执行

- 出图快速换号：不能因为 SSE 已结束且缺少 `image_gen_task_id`、缺少图片引用就立即判定失败；正确做法是先短 Poll conversation mapping，仍无图再暂停账号并换号。
- SSE 读取超时：不能只给 `ImageConvOpts.SSETimeout` 赋值就认为生效；`parseSSE` 必须按事件读取设置 timeout，静默超时后关闭事件流并进入换号或失败流程。
- 参考图参数错误：`/v1/images/generations` 不能在 JSON 参考图解析前创建 `image_tasks`；解析失败要退款并直接 400，不留下无人执行的任务。
- 多图结果：不能认为上游返回多少张就落库多少张；最终结果必须按请求 `n` 截断，且 `N>1` 可能来自多个账号和 conversation。

## gpt-image-2 渠道

- 生产依赖：不能把当前 `gpt-image-2` 简化为只走 `gpt2api -> chatgpt.com` Web Runner；公网入口是 `https://lmage2.dimilinks.com/v1`，内部优先依赖 `codex-cli-proxy-image -> http://cli-proxy-api:8317`。
- 瞬态兜底：不能只把 HTTP 502 当作可兜底错误；`5xx`、timeout、EOF、connection reset、broken pipe 等都应按瞬态处理，同渠道重试后再回落内置 Web Runner。
- 兜底边界：内容安全、`400 invalid_value`、`image_generation_user_error`、最小像素预算等不是渠道瞬态错误，不能通过换渠道或切 Free 账号绕过。
- Free 账号：不能把本仓库 `oai_accounts.plan_type=free` 自动接入 Codex image channel；Free 只适合内置 Web Runner 图片链路，Codex auth 轮换后必须检查 plan 后缀。

## 图片参数与尺寸

- Codex 1K：不能再把非正方形 `1k` 比例图按长边 1024 映射，例如 `16:9 -> 1024x576`；非正方形 1K 要保证约 100 万像素且长边不低于 1536。
- 4K 预期：ChatGPT Web `picture_v2` 链路没有稳定可控的原生 2K/4K 字段；原生大图主要依赖 Codex image channel，Web Runner 只能作为默认档位/兜底。
- 参考图字段：线上 generations JSON 可能不走 `/v1/images/edits`，不能只认 `reference_images`；应兼容 `images / image / image_url / image_urls / input_image / input_images` 并看 `reference_count`。
- 参考图上传：Azure SAS PUT 的 EOF、timeout、5xx、408、429 属于网络瞬态，不是图片参数错误；内置 Runner 要短重试，外置容器内部失败只能通过等待窗口和 fallback 降低影响。

## 失败归因与展示

- 图片失败诊断：不能只看结构化错误；ChatGPT Web Runner 可能在 assistant 文本里解释拒绝原因但不产出图片引用，必须提取 SSE 和 conversation mapping 的最新 assistant 文本。
- 内容安全归因：只有出现安全、政策、未成年、内容审核等明确文本或上游信号时才归为 `content_moderation`；普通 `poll_timeout / poll_error / no image ref produced` 不能凭空推断违规。
- 管理员生成记录：不能只移除缩略图外层 `<a target="_blank">` 就认为不会跳转；`el-image` 内置预览仍可能触发 `about:blank#blocked`，应使用普通 `img` 和受控弹窗。
- 后台性能：不要把 `image_tasks.result_urls` 大字段重新放回生成记录列表；列表只放元数据和摘要，图片/失败详情懒加载。

## 下游 new-api

- 分组依赖：不能只看 token 的 `model_limits=gpt-image-2` 就认为授权成功；如果 token `group` 落到 `default`，`new-api` 会在请求进入 gpt2api 前报无可用渠道。
- 用量日志：不能看到 `quota=0 / use_time=0 / 操作 textGenerate` 就判断图片任务成功；这只是异步提交记录，最终要看 `tasks.status/fail_reason`、错误日志和号池 `image_tasks.error`。
- 前端提交：浏览器登录态前端应走下游同源 `/pg/images/generations?async=true`；API Key/业务后端走下游 `/v1/images/generations?async=true`；不要让浏览器直连号池或 CLIProxyAPI。

## 环境与运维

- Alpine 二进制：部署到 `gpt2api/server` Alpine 镜像前，不能用默认 CGO 动态链接二进制覆盖 `deploy/bin/gpt2api`；应使用 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o deploy/bin/gpt2api ./cmd/server`。
- 机器拓扑：当前 Codex 是号池，不是下游后端；涉及 IP、用户、项目目录、构建机职责时先看 `AGENTS.md`，不要沿用旧对话里的过期结论。
- SSH 判断：han 给出已记录 IP 并问能否访问/进去时，优先理解为 SSH 登录和项目目录可达性，不要只做 ping/curl；默认 SSH 身份失败不等于远端无可用公钥。
- 号池数据库：不能把 `deploy/docker-compose.yml` 示例口令当线上 MySQL 口令；查库优先从运行中容器环境读取真实连接信息，对外回复不暴露真实口令或 DSN。
