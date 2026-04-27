# Documentation

## 当前事实

- 账号池支持 `JSON / OAuth / AT / RT / ST` 五种导入方式；账号入库、AES 加密、自动刷新、额度探测、代理绑定共用 `internal/account` 现有逻辑。
- OAuth 导入只是新增获取账号凭据的入口，不替换旧导入链路；默认回调仍优先使用 OpenAI/Codex 常用的 `http://localhost:1455/auth/callback`。
- 图片生成已改为异步任务：前端提交后保存 `task_id` 并轮询任务接口；`dispatched` 表示等待账号调度，拿到账号 lease 后才进入 `running`。
- 图片生成兼容下游任务协议：`POST /v1/images/generations?async=true` 与 `Prefer: respond-async` 会按异步任务 body 返回但 HTTP 状态保持 `200`，避免下游网关把 `202` 当上游错误；默认异步提交仍返回 `{created, task_id, data: []}`，但显式传 `compat=apimart` / `response_schema=apimart` 或 `X-Response-Format: apimart` 等头时，会自动按异步提交处理，响应改为 APIMart 风格 `{code:200,data:[{status:"submitted",task_id}]}`；`GET /v1/tasks/:id` 返回 OpenAI/Sora 风格 `image.task` 包装，`GET /v1/images/tasks/:id` 保持原历史响应。
- OpenAI 兼容网关错误已按 APIMart 常见错误类型归类：401=`authentication_error`、402=`payment_required`、429=`rate_limit_error`、5xx=`server_error/service_unavailable`；默认 `error.code` 仍是内部稳定字符串，APIMart 兼容模式下 HTTP 错误的 `error.code` 改为数字状态码。
- Codex/OpenAI 兼容图片渠道已做内容安全归因：明确命中 `content_policy_violation / content_moderation / moderation_blocked / safety system` 等上游信号时，任务失败码写为 `content_moderation`，同步响应返回 400 并退款；这类错误不标记渠道 unhealthy，也不继续换渠道绕过上游安全策略。没有明确安全信号的 `poll_timeout / upstream_error / no image ref produced` 仍只能视为上游未明确原因，不能推断为违规。
- ChatGPT Web Runner 的图片失败诊断会同时提取 SSE 和 conversation mapping 中最新 assistant 文本；如果上游没有给出图片 file/sediment 引用但返回了自然语言拒绝/说明，会写进 `image_tasks.error` 的 detail，并在命中安全/未成年/政策等明确文本信号时归类为 `content_moderation`。管理员“生成记录”已新增“失败原因”列，展示中文原因、上游详情摘要和复制按钮。
- 图片异步任务运行在当前进程 goroutine 内；服务启动时会把启动前仍处于 `queued / dispatched / running` 的任务标记为 `interrupted`，避免部署或重启后下游长期轮询永不完成的旧任务。
- 图片任务调度默认扫描最多 500 个候选账号，避免账号池规模较大时只看前 30 个候选导致误报 `no_available_account`；`poll_timeout` 属于可换号重试错误，异步任务总超时会随重试次数延长并封顶 15 分钟。
- 无参考图的异步生图已改为更快换号：默认最多 4 次尝试、单次 2 分钟、轮询 90 秒；某账号 `poll_timeout` 后会临时降级暂停调度，避免坏账号连续拖慢任务。
- 图片任务对前端展示时优先返回本站 `/p/img/<task_id>/<idx>` 签名代理 URL，不直接暴露上游临时 `result_urls`；缺少 `file_ids` 的极老任务才兜底旧直链。
- 2026-04-27 排查下游 AI 创作任务 `task_gOIdvPLUbrHpy8EdzNcmPzDo4T0WrL1J`：号池任务 `img_871fdec3952f4aa88fb3988a` 实际成功，但外置 `codex-cli-proxy-image` 参考图请求先卡到 7 分钟超时，随后才回落内置 ChatGPT Web Runner，导致总耗时约 8 分钟。为避免同类卡顿，异步图片外置渠道现在无参考图最多等待 90 秒、有参考图最多等待 2 分钟，超时后尽快走内置 Runner 兜底；下游前端轮询窗口需覆盖到 15 分钟。
- 2026-04-27 修复下游图生图保存回归：对外 `ImageGenerations / ImageTask / ImageTaskCompat / ImageEdits / chat->image` 返回的本站 `/p/img` 代理图，现在会按当前请求 `Host/X-Forwarded-Proto` 输出绝对 URL（生产为 `https://lmage2.dimilinks.com/p/img/...`）。原因是下游前端/保存器曾把相对 `/p/img` 补成 `https://dimilinks.com/p/img/...`，下载到 HTML 后触发“图片未能保存到本机”或上游 `unsupported MIME type text/html`；`internal/image` 包内部仍保留相对 path，只有网关对外响应层补 origin。
- 2026-04-27 管理员后台“生成记录”列表已改为轻量加载：列表不再查询/返回 `image_tasks.result_urls` 大字段（生产表约 1877 行但该列累计约 2.5GB，单页曾需秒级加载），只返回任务元数据、结果数量和失败摘要；点击“查看结果 / 查看失败”时再调用 `GET /api/admin/image-tasks/:id/images` 懒加载图片 URL 或失败详情。后续不要把 base64/data URL 重新放回列表接口。
- 2026-04-27 排查生成记录 `image_tasks.id=1963 / img_bd53c1f501c942b7ad7f9031`：请求入库 `n=1`，但 ChatGPT Web SSE 一次返回了 2 个 sediment 图片引用，旧 Runner 只判断“已满足 ≥N”后跳过轮询，却没有把超出的引用截断，导致任务落库和下游展示 2 张。已在内置 Runner 与外置图片渠道成功结果落库/结算前统一按请求 `n` 截断；这不是用户绕过 `n`，而是上游多产出未被服务端裁剪。
- `file_ids` 的单图元素可携带 `account_id / conversation_id / file_ref`；图片代理优先使用单图元信息回源，兼容旧任务的任务级账号信息。
- 2026-04-25 已将 `upscale=2k/4k` 从本地 Catmull-Rom 插值切换为阿里云生成式图像超分：`/p/img` 首次访问拉取 ChatGPT 原图后调用 `GenerateSuperResolutionImage`，轮询 `GetAsyncJobResult` 并立即下载结果；失败回落原图且不再回退本地插值，成功结果仍只缓存在当前进程 LRU。阿里 AK/SK 只写本机忽略文件 `deploy/.env` 和环境变量，不写入 Git。
- 本地已合并上游多渠道能力，并保留 OAuth 导入、额度汇总、个人图片代理、nginx/端口等本地定制。
- `deploy/nginx.conf` 当前由同一个 `gpt2api-nginx` 处理公网入口：`lmage2.dimilinks.com` 进入 gpt2api，`cliproxyapi.845817074.xyz` 进入 CLIProxyAPI。
- 2026-04-25 已新增 `docs/DOWNSTREAM_INTEGRATION.md`，作为下游 `new-api` 后端和前端对接文档；当时确认对外是 `gpt2api -> chatgpt.com` Web 反代路线，不是 OpenAI 官方 API，也不是 `cliproxyapi` 路线。2026-04-25 21:24 CST 起，纯文生图的 `gpt-image-2` 已额外接入本机 CLIProxyAPI Codex 图片渠道作为外置 image channel；2026-04-25 21:42 CST 起，JSON 参考图与 multipart `/v1/images/edits` 也已接入同一 Codex image channel，只有外置渠道不可用时才回退原 ChatGPT Web Runner。
- 2026-04-26 复核关键依赖：当前生产 `gpt-image-2` 优先依赖 `codex-cli-proxy-image` 外置 image channel，数据库配置为 `upstream_channels.name=codex-cli-proxy-image / base_url=http://cli-proxy-api:8317 / enabled=1 / status=healthy`，映射为 `local_model=gpt-image-2 -> upstream_model=gpt-image-2 / modality=image`；`gpt2api-server` 与 `cli-proxy-api` 必须同在 Docker 网络 `deploy_default`，容器内 `cli-proxy-api` DNS 和 `/health` 必须可用。下游公网仍只打 `lmage2.dimilinks.com`，不要直接打 `cliproxyapi` 公网域名。
- 2026-04-26 已按 han 要求增加图片渠道瞬态错误 Free 兜底：外置 Codex/OpenAI 兼容 image channel 在重试后仍返回 `502/5xx`、`stream disconnected before completion`、超时、EOF、connection reset 等瞬态错误时，会转入内置 ChatGPT Web Runner，并强制调度 `plan_type=free` 账号；异步兜底会使用新的 fallback 超时上下文，避免复用已经被外置渠道耗尽的 7 分钟 ctx。内容安全、`400 invalid_value`、最小像素预算等用户请求错误不兜底，也不标记渠道 unhealthy，避免绕过安全策略或掩盖真实参数问题。
- 2026-04-26 已上线 new-api 顾客身份透传：仅当号池 API Key ID 命中 `security.trusted_downstream_api_key_ids`（当前生产为 `2`）时，`image_tasks` 会写入 `downstream_user_id / downstream_username / downstream_user_email / downstream_user_label`；优先读取 `X-NewAPI-User-ID/Username/User-Email`，请求 `user` 字段只作为同一可信 key 的兜底。管理员“生成记录”已显示“顾客 / 号池用户”，关键词可搜顾客 ID、用户名、邮箱和 label。历史回填按 new-api `tasks.private_data.upstream_task_id == image_tasks.task_id` 精确匹配，2026-04-26 导出 1396 条、命中并补写 870 条，不猜测无法匹配的数据。
- 2026-04-26 管理员“生成记录”的结果弹窗已取消图片外层跳转链路：不再使用 `el-image` 的内置预览和 `target=_blank` 外链，缩略图改为普通 `img` + 受控大图弹窗，避免 base64/data URL 在点击缩略图、大图或空白区域时误跳 `about:blank#blocked`。
- 2026-04-27 起，项目机器连接拓扑迁入根目录 `AGENTS.md` 的“项目连接信息”；`Documentation.md` 不再保存连接详情，避免与长期 agent 记忆出现两套来源。

## 长期注意事项

- 不要用 `file_id` 是否以 `sed:` 开头判断 IMG1/preview；`sediment / estuary` 也可能是正常 IMG2 最终结果，只有内部持久化的 `preview:` 前缀才表示真正兜底。
- 修图片裂图时先看任务是否有 `file_ids` 和单图代理元信息；浏览器直接访问上游临时图链失败，通常不是任务生成失败。
- `N>1` 并发生图可能每张图来自不同账号和 conversation；不要再假设一个任务只有一个可用于下载全部图片的账号上下文。
- OAuth 会话状态保存在服务端内存，TTL 为 30 分钟；服务重启或超时后需要重新生成授权链接。
- OAuth 的 `proxy_id` 同时用于服务端换 token 和新建账号默认代理绑定；更新已有账号时不会自动改绑原代理。
- 修改 `deploy/nginx.conf` 后，如容器内仍读取旧配置，优先重建 `gpt2api-nginx`，不要只依赖 `nginx -s reload`。
- CLIProxyAPI 管理界面当前经公网域名可访问，安全性依赖强管理密钥；若要改回仅本机可用，需要重新加 Nginx 层拦截。
- 排查下游 `Failed to update video task / parseTaskResult` 时，先区分三类现象：上游提交 HTTP 状态必须是 `200`、下游返回客户端 `202` 是正常异步响应、任务失败原因若是 `no_available_account / poll_timeout / interrupted` 则根因在号池任务执行或部署中断，不是前端问题。
- 排查 `gpt-image-2` 不可用时先分层：`new-api` 报 `under group default` 先修 token/group；gpt2api 日志有 `channel async image fail`、`upstream 502` 或 `stream disconnected before completion` 先查 `cli-proxy-api` 容器、Docker 网络和 `upstream_channels/channel_model_mappings`；只有无启用 image route 或内置 Runner 日志出现时，才优先查 ChatGPT 账号池。
- 排查参考图不生效时先看 gpt2api 图片参数日志的 `reference_count`：`0` 表示前端或 `new-api` 没把参考图字段传到 gpt2api，`>0` 后再查上游上传/生成效果。
- 不要再把 `16:9+1k` 记录为 `1024x576`；当前 Codex/native 映射会为非正方形 1K 提高像素预算，例如 `16:9+1k -> 1536x864`、`9:16+1k -> 864x1536`、`2:3+1k -> 1024x1536`。
- 2026-04-25 针对 `img_0af0fe5de388490597197ee8` 的 `poll_timeout` 已完成热修复部署；部署后本机 smoke 任务 `img_3fa25b0cbe914af58b11c27d` 约 26 秒成功返回 1 张图。
- 2026-04-25 13:12-13:15 CST 生产号池监控检查：`gpt2api-server/mysql/redis/nginx` 均 healthy，`account.refresh_enabled=true`、`account.quota_probe_enabled=true`，刷新/探测无待办欠账；账号池 200 个活跃账号，检查末快照约 185 healthy / 15 warned，0 dead/suspicious/throttled，图片剩余额度合计约 3837。
- 同次检查发现两个需后续关注点：所有账号 `image_quota_total=-1`，导致 `/api/admin/accounts/quota-summary` 的 `total_capacity` 原始汇总会显示负数；代理池为空且 200 个账号均未绑定代理，出图日志仍有 `turnstile required` 与 `poll_timeout`，会把相关账号临时降为 warned 并冷却 24 小时。
- 2026-04-25 14:08 CST 按用户提供的临时 zip 向当前本机生产号池导入 200 个新账号；导入前与现有 200 个账号邮箱无重叠，导入结果 `created=200 / failed=0`，新增账号 ID 为 216-415，均已写入 RT 和过期时间；导入后活跃账号 400 个，快照约 298 healthy / 102 warned，0 dead/suspicious/throttled。临时 zip/导入 payload 已从 `/tmp` 清理，凭据未写入仓库。
- 2026-04-25 20:36 CST 按 CPA JSON 导出包继续导入账号时，只导入 `available-cpa-json` 中 source 未禁用、usage 探测 HTTP 200 且未触及 rate limit 的 9 个 `codex/plus` 账号；`valid-but-source-disabled-cpa-json` 中 2 个 live-valid 但源池禁用的账号未导入，除非后续明确要重新启用。
- 同次导入通过 `/api/admin/accounts/import` 走现有 AES 加密、去重、刷新/额度探测链路；导入后 9 个账号均为 `healthy / codex / plus`，额度定向探测均可拿到剩余额度，`image_quota_total` 仍保持当前系统既有的 `-1` 表示未知上限。
- 2026-04-25 已针对“用户出图慢/长时间等待后失败”做快速换号优化：无参考图生图默认最多 5 次尝试、单次 90 秒、总等待封顶约 5 分钟、常规 Poll 60 秒、调度等待 10 秒；如果 SSE 已结束但没有 `image_gen_task_id` 且没有任何图片引用，只缩短为 20 秒 Poll 后再换号，避免直接失败也避免长时间空等。
- 同次追加多图优化：`n>1` 并发子图不再每张只试一个账号，单张子图失败时也按快速换号预算重试，提高多图任务至少出图/出满图概率。
- 2026-04-25 继续修复图片任务长期 `running`：`parseSSE` 已真正启用按事件读取超时；无参考图 SSE 单次静默 30 秒超时，参考图 60 秒超时，超时后返回 `sse read timeout` 并进入既有换号/失败链路，避免 goroutine 长期卡在上游 SSE 读取。
- 2026-04-25 14:02 CST 部署 SSE timeout 修复时，旧进程中 7 个非终态图片任务被启动清理标记为 `interrupted`；部署后服务健康检查正常，队列中无残留 `queued / dispatched / running`。
- 同次运维修正：发现代理表有 1 个健康启用代理但账号绑定数为 0，已将 200 个活跃账号统一绑定到 `proxy_id=2`；同时清理有剩余额度的 `warned` 账号冷却时间但保留 `warned` 低优先级状态，可调度候选恢复到约 195 个。
- 修复部署后的首轮观察：`img_1434f47cf8a24c4aa916bd52` 单图 54 秒成功，`img_4deb7b3814c249578a1db985` 四图 37 秒成功；日志仍可见 `turnstile required`，但已能继续产出，后续重点观察代理质量和超时率。
- 2026-04-25 14:12 CST 发现新导入的 200 个账号未绑定代理并触发连续 `poll_timeout`，已再次批量绑定全部 400 个活跃账号到 `proxy_id=2`；代码层新增未绑定账号的代理兜底，调度、额度探测、刷新和图片代理在无显式绑定时会使用第一个启用且健康分大于 0 的代理。
- 2026-04-25 14:14 CST 部署代理兜底代码时，`img_a84ce97f0ddc4d9fbb6d7644` 刚进入运行并被本次重启标记 `interrupted`；部署后服务健康，队列无残留运行任务，400 个活跃账号均已绑定代理。
- 2026-04-25 14:20 CST 最终观察：代理兜底部署后 `img_1a72127054f04401994bf6ad / img_f2f50dc04258465d8ef95240 / img_c2472ba5a58c46ab8d5b8ba9 / img_5d58ca4eb2a44d1fbce339d0` 连续成功，耗时约 50-105 秒；当前无 `queued / dispatched / running` 残留，账号快照约 285 healthy / 115 warned，可调度候选约 390，未绑定代理账号 0。
- 2026-04-25 14:24 CST 追加观察：`img_57600ab66ab14951bf6116a0` 四图 177 秒成功，`img_d99abc6a395a496b923cc847` 143 秒成功，`img_a8fdcb388c604d1f8fda88e9` 117 秒成功；当前无任务长时间卡住。发现一次 `/p/img` 代理取图 60 秒超时但同任务其他取图成功，后续若用户反馈“图已成功但加载慢/502”，优先考虑为原图代理增加短期缓存或优化回源超时策略。
- 2026-04-25 针对 `gpt-image-2` 4K/尺寸参数兼容性确认：官方原生控参应走 Image API `/v1/images/generations` 或 Responses API 的 `image_generation` tool；本项目的 ChatGPT 号池反代只能通过网页 `f/conversation` 间接出图，不能保证原生遵守 `size / quality / output_format`。当前代码已让外置 OpenAI 图片渠道透传这些参数；Chat 入口转图片也会保留 `n / size / quality / output_format / output_compression / background / moderation`，并在号池路径把 2K/4K 尺寸降级映射为本地代理放大兜底，但这不是真正的上游原生 4K/精确构图。
- 2026-04-25 4K 放大补充修复：显式传 `upscale=2k/4k` 时不再走外置图片渠道直返，避免绕过 `/p/img` 本地放大代理；`upscale` 现在会 trim 并忽略大小写，兼容下游传 `4K`。
- 2026-04-25 部署后排查发现下游 `new-api` 请求入库为 `size=16:9/2:3/...` 且 `upscale` 为空，说明 4K 选择未按 `upscale` 传入；已追加兼容 `resolution / image_size / scale / quality` 中的 `4k/UHD/2160p/2k/1440p` 别名，并记录不含 prompt 的图片参数日志用于核验下游真实传参。
- 2026-04-25 19:52 CST 参考 APIMart `gpt-image-2` 文档做本机 smoke：临时 API Key 调 `POST /v1/images/generations?async=true`，请求 `model=gpt-image-2 / n=1 / size=1:1 / resolution=1k`，提交 HTTP 200 返回 `task_id=img_e85a592c1d1f4acc916f2de6`；`GET /v1/tasks/{id}` 约 24 秒变为 `succeeded`，数据库任务为 `success`，代理图首次回源出现一次 `files/download 403` 导致 502，随后连续 3 次重试均 200 且返回 PNG。当前默认实现与 APIMart 文档仍有协议差异：查询响应不是 `code/data` 包装，任务状态是 `queued/in_progress/succeeded/failed` 而非 `submitted/processing/completed/failed`，结果图是相对 `/p/img/...`，JSON 参考图上限仍是 4 张而非 16 张；异步提交和 HTTP 错误可通过 APIMart 兼容模式对齐。
- 2026-04-25 20:01-20:07 CST 完成 `gpt-image-2` 接口矩阵测试：临时 API Key 实跑 26 个生成任务，14 个 `size`（`auto/1:1/3:2/2:3/4:3/3:4/5:4/4:5/16:9/9:16/2:1/1:2/21:9/9:21`）均提交、轮询、取图成功；6 类内容（中文场景、英文场景、产品图、文字海报、人像、建筑）均成功；JSON `image_urls` data URL 图生图与 multipart `/v1/images/edits` 均成功；`n=5` 被夹到 4 并返回 4 张图。`resolution=2k` 触发阿里云超分并返回 `X-Upscale=2k;provider=aliyun;cache=miss`，输出 3072×2048；两次 `resolution=4k` 均提交/生成成功但超分回落原图，日志分别为 OSS `Policy expired/AccessDenied` 与提交超时。注意：本轮尺寸测试 prompt 内显式带了 size 字符串，实际网页号池路径仍不能证明纯 API `size` 字段会原生传给上游。
- 同轮边界测试：缺 API Key 返回 401，空 prompt 返回 400；5 张唯一参考图返回 400 `最多支持 4 张参考图`。测试发现该错误原本会先落 `dispatched` 残留任务，已修复为参考图解析成功后才创建任务并完成部署复测，修复后同样 400 且不再新增 `image_tasks` 记录。
- 2026-04-25 20:21-20:36 CST 按用户要求只用现有 ChatGPT Web 号池、不走官方渠道、不用阿里，测试原生 2K/4K 可行性：纯 prompt 要求 `2048×1152 / 3840×2160 / 1152×2048 / 2160×3840 / 4096×4096`，实际原图仍为 `1672×941 / 941×1672 / 1254×1254`；临时加入 `image_size/target_image_size/requested_image_size/image_generation_options/image_generation_params` 到 `/backend-api/f/conversation/prepare`、消息 metadata、顶层 payload，实际输出退回 `1254×1254`；再把 `client_contextual_info` 的 `page_width/page_height/screen_width/screen_height/pixel_ratio` 改为目标尺寸，仍为 `1254×1254`；两步法（低分参考图重新喂给 Web，prompt 要求重绘到 2K/4K）仍只输出 `1672×941`。结论：当前可用的 ChatGPT Web `picture_v2` 链路没有找到可控原生像素尺寸字段，不能稳定拿到 OpenAI 原生 2K/4K；不使用外部超分时只能拿网页默认档位原图。
- 2026-04-25 21:24 CST 继续按“不要官方渠道、不要阿里、用现有 Codex 链路”测试并接入：本机 `CLIProxyAPI -> chatgpt.com/backend-api/codex/responses -> image_generation` 可直接生成原生像素图。直连 CLIProxyAPI 实测成功尺寸包括 `1024x1024 / 1024x1536 / 1536x1024 / 2048x1152 / 1152x2048 / 2048x2048 / 3840x2160 / 2160x3840 / 3072x2304 / 2880x2880 / 2336x3504`；失败边界包括 `4096x4096`（最长边必须 ≤3840）、`3840x3840 / 2560x3840 / 3328x2496`（超过当前像素预算）、`2352x3528`（宽高必须同时可被 16 整除）。由此推断 Codex 当前约束为最长边 ≤3840、宽高 16 对齐、4K 档像素预算约等于 `3840*2160`。
- 同次已在 gpt2api 生产库增加 `codex-cli-proxy-image` 外置 image channel（`openai` 兼容类型，base URL 为容器内 `http://cli-proxy-api:8317`，映射 `gpt-image-2 -> gpt-image-2`）。代码会在无参考图时优先走该 Codex 渠道，并把 APIMart 风格 `size=比例 + resolution=1k/2k/4k` 转为 Codex 可接受像素：例如 `16:9+2k -> 2048x1152`、`16:9+4k -> 3840x2160`、`1:1+4k -> 2880x2880`、`2:3+4k -> 2336x3504`；若 `quality` 被下游误当作 `4k/2k` 别名，会用于解析分辨率但不会继续透传给上游 quality。
- 同次完成 gpt2api 端到端验证：同步调用 `size=16:9,resolution=2k/4k` 分别返回 `2048x1152` 与 `3840x2160` PNG；异步调用 `POST /v1/images/generations?async=true` 提交耗时约 `0.02-0.04s`，任务 `img_4cfd7c7c65964b6d83cbed6a` 约 78 秒成功返回 `2048x1152`，任务 `img_bb6028cdae2d485fa3941325` 约 48 秒成功返回 `3840x2160`。这条路径不走 OpenAI 官方 API，不用阿里超分；当时限制是当前 Codex/CLIProxy 单次实际只返回 1 张图（`n=2` 仍只有 1 张），参考图尚未接入外置渠道，`output_format=webp` 曾出现响应元数据为 webp 但实际字节仍是 PNG 的上游/代理不一致。
- 2026-04-25 21:42 CST 已把参考图/图生图接入 Codex 图片渠道：`/v1/images/generations` 中的 `reference_images / images / image_url / image_urls / input_image / input_images` 会先解码为 data URL 后转发到 CLIProxyAPI `/v1/images/edits`；multipart `/v1/images/edits` 也会优先走同一渠道。已实测直连 CLIProxyAPI 参考图生成 `1024x1024 / 2048x1152 / 3840x2160 / 2336x3504` 成功；gpt2api 端到端实测 JSON 参考图 `16:9+2k` 返回 `2048x1152`，multipart edit `16:9+4k` 返回 `3840x2160`，异步 JSON 参考图任务 `img_540d23c743d74d3e800929ed` 约 78 秒成功返回 `3840x2160`。
- 2026-04-26 00:30 CST 排查 22:45-00:30 CST 用户报错：不是 nginx 或 gpt2api 服务整体不可用。下游 `new-api` 在 22:53-23:11 对用户 `1540/HMJ` 多次返回 503，原因是请求落在 `default` 分组，而 `gpt-image-2` 可用渠道只在 `gpt-image-2` 等专用分组；同窗还看到用户 `1312/haru` 访问 `gpt-image-2 2号/3号` 分组被拒。已进入 gpt2api 的异步图片任务中，失败主要来自外置 `codex-cli-proxy-image` 返回 `upstream 502: stream disconnected before completion`；另有两次 `size=16:9,resolution=1k` 被转成 `1024x576` 后触发 Codex 上游 `Invalid size ... below the current minimum pixel budget`。
- 2026-04-26 同次修复：异步图片失败现在以 `错误码: 上游详情` 写入 `image_tasks.error`，`GET /v1/tasks/:id` 会拆出稳定 `error.code` 并把上游详情拼进 `error.message`，避免后台/前端只能看到 `upstream_error`；Codex image channel 的 `1k` 非 1:1 比例不再映射到低于最小像素预算的 `1024x576`，例如 `16:9+1k -> 1536x864`、`9:16+1k -> 864x1536`、`2:3+1k -> 1024x1536`。
- 2026-04-26 00:54 CST 已在下游 `new-api` 生产库修正用户 `1540/HMJ` 的活动 token `380`：原本 `model_limits=gpt-image-2` 但 `group` 为空，导致请求落入 `default` 分组并报 `No available channel for model gpt-image-2 under group default`；现已把该 token 的 `group` 改为 `gpt-image-2`，token `381` 原本已正确。
- 2026-04-26 00:57-01:02 CST 修复后继续观察：`gpt2api-server` 服务与 `/healthz` 持续正常，`img_5c4ba56e6b8649e1a5bcab70`、`img_96b9c3d495c84d23b0d81c4b` 仍出现外置 `codex-cli-proxy-image` 的 `upstream 502: internal_server_error: server_error: stream disconnected before completion`，但错误详情已按 `upstream_error: ...` 落入 `image_tasks.error`；同时 `img_3ef9e9225ef0460cbcbd01c6` 已成功返回 `1536x864`，说明 `16:9 + 1k` 的最小像素预算映射修复生效。当前剩余问题主要是上游图片渠道偶发断流，不再是“看不到具体原因”。
- 2026-04-26 继续修复图片渠道偶发断流：外置 image channel（含同步、异步、chat->image）现在遇到 `502/5xx`、`stream disconnected before completion`、超时、EOF、connection reset 等瞬态错误时，会先在同一渠道上自动重试 1 次；若所有外置渠道仍失败，再转入强制 Free 账号的内置 ChatGPT Web Runner。`400 invalid_value`、内容安全拦截等非瞬态错误不会误重试/兜底；参数类错误会以 `invalid_request_error` 返回并保留上游详情。这样可以压掉一部分单次断流造成的误失败，同时保留原有错误详情落库。
- 2026-04-26 22:55 CST 起，内置 ChatGPT Web Runner 会在图片 SSE 与后续 conversation poll 中保存 assistant 文本诊断；如果上游实际返回“我不能生成/违反安全策略”等对话文本而不是图片引用，任务失败详情会包含 `assistant: ...` 和必要的 `last_error: ...`，后台“生成记录”可直接查看和复制，不再只显示“失败”。
- 2026-04-26 23:20 CST 起，图片异步任务失败原因会同时传给 API 用户：`GET /v1/tasks/:id` 的兼容响应在 `error.message` 中返回中文可读原因并在 `error.detail` 保留原始诊断；历史入口 `GET /v1/images/tasks/:id` 保留原 `error` 字符串，并新增 `error_code`、`error_message`、`error_detail`。若上游返回 assistant 自然语言拒绝，用户可在 `error_message` 里看到“上游说明:...”。
- 2026-04-26 23:45 CST 起，考虑到下游后端可能不只读取 `error.message`，图片异步失败响应增加冗余兼容字段：`GET /v1/tasks/:id` 失败时除 `error{code,message,detail}` 外，还会在顶层返回 `error_code`、`error_message`、`error_msg`、`message`、`error_detail`、`failure_reason`、`failed_reason`、`fail_reason`；`/api/me/images/tasks` 与 `/api/admin/image-tasks` 也返回同一套 `error_code/error_message/error_detail`，前端个人图片历史和后台生成记录优先展示 `error_message`，复制时同时带上原始诊断。
- 2026-04-25 参考图排查：线上最近只看到下游请求 `POST /v1/images/generations?async=true`，没有 `/v1/images/edits`；gpt2api 原本只在 generations JSON 中认非标准 `reference_images` 字段。已追加兼容 `images / image / image_url / image_urls / input_image / input_images`，支持字符串、字符串数组、`{"url":...}` 和对象数组，并在图片参数日志中记录 `reference_count` 以判断下游是否真的把参考图传到 gpt2api。
- 2026-04-25 15:27 CST 线上用户测试参考图不生效时，`POST /v1/images/generations?async=true` 的图片参数日志显示 `reference_count=0`，且无参考图上传记录；gpt2api 解析兼容测试通过。当前证据说明该请求没有把参考图带到 gpt2api，问题优先在前端到 `new-api` 或 `new-api`/插件转发字段，而不是 gpt2api Runner 上传阶段。若后续日志 `reference_count>0` 仍不生效，再排查上游上传/账号池执行。
- 2026-04-27 15:40-15:55 CST 排查号池出图耗时：账号池容量不是瓶颈，409 个未删账号中约 330 个满足调度条件，代理探测正常，Redis 账号锁基本为空，成功任务 `created_at -> started_at` 等待时间 p95 为 0 秒。近 24 小时成功任务平均约 124 秒、p50 约 106 秒、p90 约 204 秒、p95 约 246 秒；近 1 小时直连外置通道成功平均约 90 秒。主要波动来自外置 `codex-cli-proxy-image` 在 15:47-15:52 间连续 `context deadline exceeded`，任务会先等外置无参考图 90 秒/有参考图 2 分钟，再回落内置账号池，因此即使账号池有空闲也会被额外加 90-120 秒。该通道当时一度 `fail_count=3/unhealthy`，随后 15:52:49 又成功并恢复 healthy；后续若再次出现成批慢单，优先看通道连续超时和是否需要临时禁用/增加熔断跳过，而不是先补账号。
- 同次排查确认：`/p/img/<task>` 首次代理取图/超分会额外消耗数秒到二十余秒（样本约 3-26 秒），这不计入 `image_tasks.finished_at`，但会影响下游“看到图/保存图”的体感总耗时。区分问题时应把“任务生成耗时”和“代理下载/超分/保存耗时”分开看。
- 2026-04-27 16:50 CST 隔离测试 free 账号走 Codex 图片通道：用 gpt2api 中 `plan_type=free` 的账号 297、278 分别生成临时 `cli-proxy-api` auth-dir，并在独立容器端口 `127.0.0.1:18317` 复用生产 `gpt-image-2` Codex payload 测试 `/v1/images/generations`；两次均在约 2 秒返回 400 `invalid_request_error`，上游消息为 `Tool choice 'image_generation' not found in 'tools' parameter.`。同日对比：生产 Plus/Team Codex 通道可成功出图，内置 Web Runner 强制 free 账号也可出图。当前结论是 free 账号可以走 Web Runner 图片链路，但不具备/未暴露 Codex `image_generation` 工具，不能作为 Codex image channel 主力。测试容器和临时 token 文件已清理。
- 2026-04-27 17:05 CST 复核生产 Codex auth 目录 `/home/ubuntu/CLIProxyAPI/auths`：当前无 free 账号文件，合计 14 个 Codex auth 文件，其中 11 个 `plus`、3 个 `team`，无未知后缀；gpt2api 的 `gpt-image-2` 只映射到外置 `codex-cli-proxy-image -> http://cli-proxy-api:8317`，不会把本仓库 `oai_accounts.plan_type=free` 自动接入 Codex 通道。已新增 `scripts/check-codex-auth-plans.sh`，后续导入/轮换 Codex 账号后必须运行，发现 `*-free.json` 或未知后缀即失败，避免 free 混入 Codex 图片通道。
- 2026-04-27 17:40 CST 修复参考图上传偶发失败：`upload reference N: upload PUT ... oaiusercontent.com ... EOF/context deadline exceeded` 属于 Azure SAS 上传端点或代理链路瞬态网络错误，不是图片参数错误。内置 ChatGPT Web Runner 的 Azure PUT 已改用独立标准 HTTP/TLS transport，不再复用面向 `chatgpt.com` 的 uTLS transport；PUT 与 `/backend-api/files/{file_id}/uploaded` 都会对 EOF、timeout、5xx/408/429 做最多 3 次短重试。参考图上传失败现在归类为 `network_transient` 并保留 2 次 Runner 尝试，外置 Codex image channel 失败后回落内置 Runner 时也会有第二次参考图上传兜底。边界：外置 `cli-proxy-api` 容器内部的上传实现仍是黑盒，若错误只发生在外置通道，gpt2api 侧通过缩短外置等待、fallback 和内置上传重试降低失败率，但不能直接修改该容器内部代码。

## 已清理的历史流水

- 账号批量导入的具体文件路径、导入数量和一次性数据库计数已删除；这些是阶段性操作记录，不适合作为长期记忆。
- 2026-04-21 至 2026-04-24 的图片任务修复、后台预览修复、Nginx 分流修复已折叠进“当前事实”和“长期注意事项”。
- 曾经“CLIProxyAPI 管理接口公网拦截”和后来“公网开放管理界面”的冲突记录已改写为当前状态：公网可访问，依赖强密钥。

## 下游异步图片失败日志口径

- 2026-04-26 确认：`new-api` 用量日志里的 `LogTypeConsume / quota=0 / use_time=0 / 操作 textGenerate` 只是异步图片提交记录，不代表任务最终成功；最终状态仍以 `tasks.status/fail_reason` 和 `/v1/tasks/{task_id}` 为准。
- 同日已在 `212.50.232.214:/root/new-api` 修复：任务从非终态切换到 `FAILURE` 时，即使 `quota=0` 也额外写 `LogTypeError`，内容形如 `异步任务失败：<fail_reason>`，并在 `other` 写入 public `task_id` 与号池 `upstream_task_id`。
- 已补写 2026-04-26 当天 `gpt-image%`、`status=FAILURE`、`quota=0`、有 `fail_reason` 且缺少错误日志的历史记录，共 108 条；用户 1552 的相关失败已能在下游日志中看到 `任务被服务重启中断,请重新提交`。
- 边界：提交日志仍会存在且通常为 0 费用；后台/前端展示时不能把这条提交日志当成功结果，必须同时展示后续错误日志或任务记录失败原因。

## 下游前端异步图片失败展示

- 2026-04-27 确认：`43.161.219.135:/home/ubuntu/new-api-web` 是下游前端源码与静态发布机，站点静态目录为 `/home/ubuntu/preview.dimilinks.com`。
- 同日已修复并发布 `/console/logs`：使用日志列表新增“状态说明”列，图片失败日志会直接显示“图像生成失败”和后端错误原因；异步提交日志显示“图像生成已提交”，提示最终结果以后续状态为准。
- 相关前端提交：`new-api-web` 的 `f1c6797 fix(logs): surface async image failure reasons`；发布 chunk 已包含 `状态说明 / 图像生成失败 / 最终结果以后续状态为准`。
- 边界：43 前端只展示后端 `new-api` 已返回的日志；若用户仍看不到失败原因，先确认是否登录同一用户、时间范围是否包含失败时间、日志类型是否筛掉了“错误”。
- 2026-04-27 复核下游 `new-api` 文档 `docs/gpt-image-2-async-api-development.md`：API Key 客户端/业务后端提交图片任务走下游 `POST /v1/images/generations?async=true`；登录态前端提交走下游同源 `POST /pg/images/generations?async=true`；两者轮询都走下游 `GET /v1/tasks/{task_id}`。浏览器不要直接请求号池 `https://lmage2.dimilinks.com/v1` 或 CLIProxyAPI；`/v1/tasks/batch` 当前不建议用于 `gpt-image-2`，因为号池未提供批量任务查询接口。
- 2026-04-27 今早 AI 画布失败已定位到下游前端 Opentu 默认图片 payload：尺寸把比例发成 `1x1 / 3x2`，参考图允许继续以 URL 透传，进入号池后分别触发上游 `Invalid size '1x1/3x2'` 和参考图 MIME=`text/html`。已在 `43.161.219.135:/home/ubuntu/new-api-web` 的 `scripts/patch-opentu-canvas.mjs` 修复并发布：构建期把 `gpt-image-2` 比例尺寸转成像素尺寸，`1k/2k/4k` 改为 `resolution`，参考图发往 `/pg/images/generations` 前转为 `data:image`；构建机已通过 Opentu typecheck/build/sw build，发布目录为 `/home/ubuntu/preview.dimilinks.com/console/images/canvas`。
