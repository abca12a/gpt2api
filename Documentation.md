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
- 当前号池生产机为 `43.165.170.99`，仓库路径 `/home/ubuntu/gpt2api`；默认使用已配置私钥连接：`ssh -i ~/.ssh/han_backend_inspect_20260422 ubuntu@43.165.170.99`。
- 下游 `new-api` 后端生产机为 `212.50.232.214:22222`，排查跨服务日志时再通过 `ssh -i ~/.ssh/han_backend_inspect_20260422 -p 22222 root@212.50.232.214` 连接。

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
- 2026-04-25 已针对“用户出图慢/长时间等待后失败”做快速换号优化：无参考图生图默认最多 5 次尝试、单次 90 秒、总等待封顶约 5 分钟、常规 Poll 60 秒、调度等待 10 秒；如果 SSE 已结束但没有 `image_gen_task_id` 且没有任何图片引用，只缩短为 20 秒 Poll 后再换号，避免直接失败也避免长时间空等。

## 已清理的历史流水

- 账号批量导入的具体文件路径、导入数量和一次性数据库计数已删除；这些是阶段性操作记录，不适合作为长期记忆。
- 2026-04-21 至 2026-04-24 的图片任务修复、后台预览修复、Nginx 分流修复已折叠进“当前事实”和“长期注意事项”。
- 曾经“CLIProxyAPI 管理接口公网拦截”和后来“公网开放管理界面”的冲突记录已改写为当前状态：公网可访问，依赖强密钥。
