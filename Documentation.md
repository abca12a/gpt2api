# Documentation

## 当前事实

- 账号池支持 `JSON / OAuth / AT / RT / ST` 五种导入方式；账号入库、AES 加密、自动刷新、额度探测、代理绑定共用 `internal/account` 现有逻辑。
- OAuth 导入只是新增获取账号凭据的入口，不替换旧导入链路；默认回调仍优先使用 OpenAI/Codex 常用的 `http://localhost:1455/auth/callback`。
- 图片生成已改为异步任务：前端提交后保存 `task_id` 并轮询任务接口；`dispatched` 表示等待账号调度，拿到账号 lease 后才进入 `running`。
- 图片生成兼容下游任务协议：`POST /v1/images/generations?async=true` 与 `Prefer: respond-async` 会按异步任务 body 返回但 HTTP 状态保持 `200`，避免下游网关把 `202` 当上游错误；`GET /v1/tasks/:id` 返回 OpenAI/Sora 风格 `image.task` 包装，`GET /v1/images/tasks/:id` 保持原历史响应。
- 图片异步任务运行在当前进程 goroutine 内；服务启动时会把启动前仍处于 `queued / dispatched / running` 的任务标记为 `interrupted`，避免部署或重启后下游长期轮询永不完成的旧任务。
- 图片任务调度默认扫描最多 500 个候选账号，避免账号池规模较大时只看前 30 个候选导致误报 `no_available_account`；`poll_timeout` 属于可换号重试错误，异步任务总超时会随重试次数延长并封顶 15 分钟。
- 无参考图的异步生图已改为更快换号：默认最多 4 次尝试、单次 2 分钟、轮询 90 秒；某账号 `poll_timeout` 后会临时降级暂停调度，避免坏账号连续拖慢任务。
- 图片任务对前端展示时优先返回本站 `/p/img/<task_id>/<idx>` 签名代理 URL，不直接暴露上游临时 `result_urls`；缺少 `file_ids` 的极老任务才兜底旧直链。
- `file_ids` 的单图元素可携带 `account_id / conversation_id / file_ref`；图片代理优先使用单图元信息回源，兼容旧任务的任务级账号信息。
- 本地已合并上游多渠道能力，并保留 OAuth 导入、额度汇总、个人图片代理、nginx/端口等本地定制。
- `deploy/nginx.conf` 当前由同一个 `gpt2api-nginx` 处理公网入口：`lmage2.dimilinks.com` 进入 gpt2api，`cliproxyapi.845817074.xyz` 进入 CLIProxyAPI。
- 2026-04-25 用户纠正：`43.165.170.99` 是下游 `new-api` 后端机器，不要再默认当作 `gpt2api` 号池生产机；当前 Codex 工作目录 `/home/ubuntu/gpt2api` 只是本地项目目录，是否为实际线上部署需另行确认。
- 如需确认 `gpt2api` 线上是否已部署当前提交，先向用户确认实际部署机器、仓库路径和可用 SSH 凭据；不要沿用旧记录中的主机归属。

## 长期注意事项

- 不要用 `file_id` 是否以 `sed:` 开头判断 IMG1/preview；`sediment / estuary` 也可能是正常 IMG2 最终结果，只有内部持久化的 `preview:` 前缀才表示真正兜底。
- 修图片裂图时先看任务是否有 `file_ids` 和单图代理元信息；浏览器直接访问上游临时图链失败，通常不是任务生成失败。
- `N>1` 并发生图可能每张图来自不同账号和 conversation；不要再假设一个任务只有一个可用于下载全部图片的账号上下文。
- OAuth 会话状态保存在服务端内存，TTL 为 30 分钟；服务重启或超时后需要重新生成授权链接。
- OAuth 的 `proxy_id` 同时用于服务端换 token 和新建账号默认代理绑定；更新已有账号时不会自动改绑原代理。
- 修改 `deploy/nginx.conf` 后，如容器内仍读取旧配置，优先重建 `gpt2api-nginx`，不要只依赖 `nginx -s reload`。
- CLIProxyAPI 管理界面当前经公网域名可访问，安全性依赖强管理密钥；若要改回仅本机可用，需要重新加 Nginx 层拦截。
- 排查下游 `Failed to update video task / parseTaskResult` 时，先区分三类现象：上游提交 HTTP 状态必须是 `200`、下游返回客户端 `202` 是正常异步响应、任务失败原因若是 `no_available_account / poll_timeout / interrupted` 则根因在号池任务执行或部署中断，不是前端问题。
- 2026-04-25 针对 `img_0af0fe5de388490597197ee8` 的 `poll_timeout` 已完成热修复部署；部署后本机 smoke 任务 `img_3fa25b0cbe914af58b11c27d` 约 26 秒成功返回 1 张图。
- 2026-04-25 13:12-13:15 CST 生产号池监控检查：`gpt2api-server/mysql/redis/nginx` 均 healthy，`account.refresh_enabled=true`、`account.quota_probe_enabled=true`，刷新/探测无待办欠账；账号池 200 个活跃账号，检查末快照约 185 healthy / 15 warned，0 dead/suspicious/throttled，图片剩余额度合计约 3837。
- 同次检查发现两个需后续关注点：所有账号 `image_quota_total=-1`，导致 `/api/admin/accounts/quota-summary` 的 `total_capacity` 原始汇总会显示负数；代理池为空且 200 个账号均未绑定代理，出图日志仍有 `turnstile required` 与 `poll_timeout`，会把相关账号临时降为 warned 并冷却 24 小时。
- 2026-04-25 14:08 CST 按用户提供的临时 zip 向当前本机生产号池导入 200 个新账号；导入前与现有 200 个账号邮箱无重叠，导入结果 `created=200 / failed=0`，新增账号 ID 为 216-415，均已写入 RT 和过期时间；导入后活跃账号 400 个，快照约 298 healthy / 102 warned，0 dead/suspicious/throttled。临时 zip/导入 payload 已从 `/tmp` 清理，凭据未写入仓库。
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

## 已清理的历史流水

- 账号批量导入的具体文件路径、导入数量和一次性数据库计数已删除；这些是阶段性操作记录，不适合作为长期记忆。
- 2026-04-21 至 2026-04-24 的图片任务修复、后台预览修复、Nginx 分流修复已折叠进“当前事实”和“长期注意事项”。
- 曾经“CLIProxyAPI 管理接口公网拦截”和后来“公网开放管理界面”的冲突记录已改写为当前状态：公网可访问，依赖强密钥。
