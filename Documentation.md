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
- 2026-04-29 当前号池巡检：`gpt2api-server / mysql / redis / nginx / cli-proxy-api` 均在线，`https://127.0.0.1/healthz`、容器内 `http://127.0.0.1:8080/healthz` 与 `http://cli-proxy-api:8317/healthz` 返回正常；数据库 `oai_accounts` 共 409 条，仅 `healthy=17`（`free=8`、`plus=9`），其余 `warned=392`。当前仅 1 条代理、健康分 100、400 个账号已绑定代理，暂无“代理池整体失效”迹象。
- 2026-04-29 当前外置 Codex auth 文件池共 34 个文件（`plus=31`、`team=3`、`forbidden_or_unknown=0`），未混入 free/未知 plan；但 `codex-cli-proxy-image(channel_id=1)` 当天持续报 `Tool choice 'image_generation' not found in 'tools' parameter.`，`CLIProxyAPI/logs/main.log` 当天已出现 33 次该错误，说明问题不在 auth 文件数量，而在当前外置 Codex 图片执行链路本身。
- 2026-04-29 当前图片链路状态：近 24 小时 `image_tasks` 共 173 条，`success=163`、`failed=10`，成功率约 `94.2%`；今天截至巡检时共 111 条，`success=105`、`failed=5`，成功率约 `94.6%`。多数成功单是在外置 Codex 或 APIMart 失败后回落内置 free runner 完成，因此“整体可出图”不代表“外置图片号池健康”。
- 2026-04-29 基于最近 24 小时 `image_tasks.status='success'` 的复盘，第一层外置图片渠道成功单（`account_id=0`）平均耗时约 `99.9s`，而 fallback 到内置 runner 的成功单平均耗时约 `303.7s`。部署后能严格对齐到日志 `reference_count` 的第一层成功样本只有 2 单，均为图生图，耗时分别 `113s` 和 `227s`；同一窗口内尚未拿到“文生图且第一层成功”的严格样本，因此不能拍脑袋把图生图第一层硬切到 `120s` 以下。
- 2026-04-29 17:10（Asia/Shanghai）补做两组多轮实测后确认：当前 `Codex` 会员链路不是“整体兜不住”。直连 `cli-proxy-api:8317 /v1/images/generations` 的 5 轮文生图全部成功，平均约 `18.4s`；通过当前号池 `gpt2api /v1/images/generations?async=true` 再走完整路由的 5 轮文生图也全部成功，`image_tasks.account_id` 全为 `0`、单任务耗时约 `30~37s`，对应窗口内未出现这些任务的 `channel async image fail, try next` 或 `fallback to free account runner` 日志，说明这 5 单都停在第一层外置 Codex 成功。
- 2026-04-29 17:10（Asia/Shanghai）同批观测也确认：问题还没“完全解决”，但已经收敛到更窄的边界。最近半小时内仍能看到 `img_d1ad737a2d5d47849df9c0e0 / img_bf0aa13ff29f4d6799835124 / img_cf3953d6b30b4d869957849b` 这类任务先在 `codex-cli-proxy-image` 报 `POST /v1/images/edits ... context deadline exceeded`，随后 fallback 到 `free` 账号成功；`cli-proxy-api` 同时间窗也仍有部分 `plus` auth 自动 refresh 返回 `401`。因此当前更准确的判断是：`文生图` 路径已能稳定命中 Codex 会员池，但 `图生图/edits` 与部分 auth 刷新仍会把慢单推向 `free runner`。
- 2026-04-29 17:23（Asia/Shanghai）补做“更贴近真实请求”的图生图压测后，边界再次收窄：选取最近真实提示词中的单参考图改背景（`背景换成四季融合的背景`）和三参考图海报合成（`图2 图3 是素材合成按照第一个图的风格生成合成一张...不要抖音截图模式`），并把最近真实结果图先下载为本地 `data:image` 后再提交，排除了“参考图 URL 回源慢/失效”干扰。在此条件下，经 `gpt2api /v1/images/generations?async=true` 提交的 `2` 轮单参考图和 `3` 轮三参考图共 `5/5` 全部成功，任务号 `img_bfb928149a1f458e93c5e72a / img_dfe77a9127404c239c427a7e / img_8e81785390104fa48d66f638 / img_70f765dc0c5245f393642258 / img_3a969958efa14723893fedaf` 的 `account_id` 全为 `0`，未切到 `APIMart` 或 `free runner`。
- 2026-04-29 17:23（Asia/Shanghai）上述 5 轮图生图虽然都停在第一层外置 Codex 成功，但耗时明显高于文生图：整单约 `108.7s ~ 122.4s`；`cli-proxy-api` 对应窗口内的 `/v1/images/edits` 日志耗时约 `1m41s ~ 1m59s`。因此当前更准确的结论应改为：`图生图` 并非“必然兜不住”，而是“成功率在这批样本上可接受，但单次成功耗时本身就贴近 2 分钟超时边缘”；后续若继续优化，重点不再是证明会员链路是否可用，而是收敛 `edits` 路径尾延迟与超时阈值。
- 2026-04-29 晚间继续核实时，已拿到“兜底后原上游又完成”的硬证据，而且不是只停留在代码推断。对 `img_18cc5f852d1c49c7ad8a2712`，号池在 `16:57:49` 因 `apimart` 轮询超时报 `fallback to free account runner`，随后 free runner 于 `16:58:17` 成功；但直接查询同单 `apimart` 上游任务 `task_01KQC77A5V1SYY1MKM44HNSCY1`，其状态为 `completed`，完成时间是 `16:57:53`，也就是号池放弃后约 `4s` 就完成。对 `img_8822b0ce838d4d3194394987` 也有同类现象：号池于 `16:43:26` 放弃 `apimart` 并切 free，free runner 于 `16:44:30` 成功；但对应 `apimart` 任务 `task_01KQC6D0FHJ79D978AHZZJHWNC` 最终在 `16:47:00` completed。当前可确认：`codex-cli-proxy-image` 这跳暂未发现“同一次 /v1/images/edits 请求先被号池超时、后又在 CLIProxyAPI 里 200 完成”的样本，已核到的重复出图硬证据集中在第二跳 `apimart` 任务轮询超时后。
- 2026-04-29：已在仓库中对第二跳 `APIMart` 轮询补上“超时前最后一次终态确认”。原因是现有实现一旦 `pollAPIMartImageTask` 的上下文到点，就直接把 `context deadline exceeded` 交回上层，导致号池可能立刻切 `free runner`，而上游 `APIMart task` 实际已在超时边缘完成。当前修复把最终确认收敛在 `openaiAdapter` 内：当轮询上下文结束时，会用独立 `5s` 短上下文再查一次同一 `task_id`；若已 `completed`，直接返回成功结果，不再误切兜底；若仍 `pending/processing`，则保持原超时失败。该修复只覆盖已确认有硬证据的第二跳 `APIMart` 异步任务竞态，不改变第一跳 `codex-cli-proxy-image` 的超时策略，也不延长正常轮询窗口。
- 2026-04-29：继续追查首跳 `unknown provider for model gpt-image-2` 后确认，这批失败不是此前“坏 auth 仍参与图片轮询池”的老代码问题复发，而是运行时 provider 池被人为抽空。`CLIProxyAPI` 日志显示在 `17:24:12 ~ 17:24:46` 有一串来自管理接口的 `PATCH /v0/management/auth-files/status`，把多批 `codex-*.json` 从 `disabled: false -> true`；对应 `model_registry` 中 `gpt-image-2` 计数一路从 `29` 降到 `0`。而 `CLIProxyAPI/sdk/api/handlers/handlers.go` 的 `getRequestDetails` 在 provider 列表为空时会直接返回 `unknown provider for model gpt-image-2`，因此号池里 `17:25` 之后观测到的这批 `502 unknown provider...` 属于“首跳图片 provider 已清空”的运行时状态，不是单纯把超时线从 `120s` 往后推就能修复。当前本机直接检查 `/home/ubuntu/CLIProxyAPI/auths/codex-*.json` 也确认：`34` 个 Codex auth 文件全部处于 `disabled=true`。
- 2026-04-29：图片 `upscale=2k/4k` 已从阿里云外部服务切换为号池本地高质量缩放算法，不再依赖 AK/SK、地域、端点和异步轮询。当前策略同时收紧为“仅 `free` 账号任务触发超分”：图片代理 `/p/img/...` 在拿到原图后，会先判断该图最终账号 `plan_type`；只有 `free` 才进入本地超分 + 进程内 LRU，`plus/team` 或外置 channel 结果即使带 `upscale` 字段也直接返回原图。边界是：这是代理出口层策略，不改变上游原始生图尺寸，也不把放大结果写回 `image_tasks`。

### 图片任务协议

- 图片生成默认走异步任务：提交后保存 `task_id` 并轮询任务接口；`dispatched` 表示等待账号调度，拿到账号 lease 后才进入 `running`。
- `POST /v1/images/generations?async=true` 与 `Prefer: respond-async` 会按异步任务 body 返回，但 HTTP 状态保持 `200`，避免下游网关把 `202` 当上游错误。
- 默认异步提交返回 `{created, task_id, data: []}`；显式传 `compat=apimart`、`response_schema=apimart` 或 `X-Response-Format: apimart` 时，响应改为 APIMart 风格 `{code:200,data:[{status:"submitted",task_id}]}`。
- `GET /v1/tasks/:id` 返回 OpenAI/Sora 风格 `image.task` 包装；`GET /v1/images/tasks/:id` 保持历史响应。
- 服务启动时会把启动前仍处于 `queued / dispatched / running` 的图片任务标记为 `interrupted`，避免部署或重启后下游长期轮询不到终态。

### gpt-image-2 路由

- 下游公网入口始终是 `https://lmage2.dimilinks.com/v1`；下游不要直接调用 `cliproxyapi` 公网域名或浏览器直连号池内部接口。
- 生产 `gpt-image-2` 优先走数据库中的外置 image channel：`codex-cli-proxy-image -> http://cli-proxy-api:8317`，映射为 `gpt-image-2 -> gpt-image-2 / modality=image`。
- 外置 OpenAI 兼容图片渠道若返回 APIMart 异步任务格式 `{code:200,data:[{status:"submitted",task_id}]}`，当前适配器会自动轮询 `/v1/tasks/{task_id}` 直到 `completed/failed/cancelled`，因此可把 `apimart` 之类的异步 OpenAI 兼容图片渠道接在 Codex route 之后做第二跳兜底。
- APIMart `gpt-image-2` 的图生图要按其文档走 `POST /v1/images/generations + image_urls[]`，不要沿用通用 OpenAI 兼容的 `POST /v1/images/edits + images[{image_url}]`；当前适配器已对 `apimart.ai` 基于域名做该特判。
- APIMart 渠道若需要启用它自身的官方兜底，可在 `upstream_channels.extra` 写 JSON `{"official_fallback":true}`；当前 `openai` 适配器会把该字段原样透传给 APIMart 图片请求。
- 走外置图片渠道时，会同时保留原始比例尺寸 `size=1:1/16:9/...` 与 `resolution=1k/2k/4k` 给 APIMart 这类比例协议上游；Codex/native 渠道仍可继续吃转换后的像素尺寸，不再因为第二跳协议不同而把 `1:1` 强制压成 `1024x1024`。
- `gpt2api-server` 与 `cli-proxy-api` 必须同在 Docker 网络 `deploy_default`；容器内 `cli-proxy-api` DNS 和 `/healthz` 要可用。
- 外置 Codex image channel 使用 `/home/ubuntu/CLIProxyAPI/auths/codex-*.json` 文件池，由 `cli-proxy-api` 独立调度；`gpt2api-server` 只挂载该目录和日志用于路由、展示与统计，不直接把数据库 `oai_accounts` 当作外置 Codex 调度池。
- 外置 Codex auth 文件可以与数据库账号邮箱重合，但两边不是同一个队列：外置池消耗 Codex usage/credits，内置 ChatGPT Web Runner 消耗 Web 图片额度。
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
- 图片任务对前端展示优先返回本站 `/p/img/<task_id>/<idx>` 签名代理 URL，不直接暴露上游临时 `result_urls`；缺少 `file_ids` 时，普通旧直链仍兼容保留，但 `data:image/...;base64` 这类大块内联结果也改走代理，避免把多 MB base64 直接塞进任务详情或后台弹窗响应。

### 失败归因与后台展示

- OpenAI 兼容网关错误按 APIMart 常见类型归类：401=`authentication_error`、402=`payment_required`、429=`rate_limit_error`、5xx=`server_error/service_unavailable`；APIMart 兼容模式下 HTTP 错误的 `error.code` 改为数字状态码。
- ChatGPT Web Runner 会同时提取图片 SSE 和 conversation mapping 中最新 assistant 文本；如果上游没有给出图片引用但返回自然语言拒绝/说明，会写入 `image_tasks.error` detail。
- 管理员账号池页的“Codex今日”来自外置 CLIProxyAPI 日志，只统计请求数、成功数、失败数和 429 次数；它不是官方 Codex 剩余 credits，也不等同“Web 图剩余”。
- `GET /v1/tasks/:id` 失败时除 `error{code,message,detail}` 外，还返回顶层 `error_code / error_message / error_msg / message / error_detail / failure_reason / failed_reason / fail_reason`，方便下游不同读取路径展示原因。
- 管理员“生成记录”列表不再查询或返回 `image_tasks.result_urls` 大字段，只返回任务元数据、结果数量和失败摘要；点击“查看结果 / 查看失败”时再调用 `GET /api/admin/image-tasks/:id/images` 懒加载。
- 后台生成记录不要使用 `el-image` 内置预览承载结果图；使用普通 `img` 与受控 `el-dialog`，避免 base64/data URL 误跳 `about:blank#blocked`。

### 下游集成

- `docs/DOWNSTREAM_INTEGRATION.md` 是下游 `new-api` 后端和前端对接文档；当前对外是 `gpt2api -> chatgpt.com` Web 反代与本机 Codex image channel 组合路线，不是 OpenAI 官方 API 路线。
- API Key 客户端/业务后端提交图片任务走下游 `POST /v1/images/generations?async=true`；登录态前端提交走下游同源 `POST /pg/images/generations?async=true`；两者轮询都走下游 `GET /v1/tasks/{task_id}`。
- `/v1/tasks/batch` 当前不建议用于 `gpt-image-2`，因为号池未提供批量任务查询接口。
- 下游 `new-api` token 必须命中含 `gpt-image-2` 渠道的分组；错误里出现 `under group default` 时，请求通常还没进入 gpt2api。
- 下游用量日志中的 `LogTypeConsume / quota=0 / use_time=0 / 操作 textGenerate` 只是异步提交记录，不代表任务成功；最终状态看 `tasks.status/fail_reason`、错误日志和号池 `image_tasks.error`。
- 下游 `new-api` 的 `Request ID` 不会原样透传成号池 `gpt2api` 的 `request_id`；跨系统排查时，优先用下游公开 `task_id` 在 `tasks.private_data.upstream_task_id` 映射到号池 `img_*` 任务号，或按提交时间窗口对齐两边日志。
- 2026-04-29 进一步确认：当前下游 `new-api` 中，`channels.id=20 / 自有账号 / https://lmage2.dimilinks.com` 表示请求先进入当前号池；这类任务在下游 `tasks.private_data.upstream_task_id` 中会映射为号池 `img_*` 任务号。若随后在号池日志里看到 `channel_id=2 apimart` 的 `503`，这是号池内部“Codex -> APIMart -> free runner”链路的第二跳报错，不等于“下游后端 APIMart 兜底失败”。
- 2026-04-29 进一步确认：当前下游 `new-api` 中，`channels.id=18 / 图片上游apimart / https://api.apimart.ai` 表示后端直连 APIMart；这类任务的 `upstream_task_id` 通常仍是 APIMart 自己的 `task_*`。若下游 `tasks.fail_reason` 或后端日志里出现 `all channels failed. Last error: HTTP 400 ... Tool choice 'image_generation' not found in 'tools' parameter.`，应归因为下游后端这条直连 APIMart 链路，而不是号池第二跳。
- 下游前端 `/console/logs` 已新增状态说明：图片失败日志显示“图像生成失败”和后端错误原因，异步提交日志显示“图像生成已提交”。
- 2026-04-28 进一步确认：下游 `new-api` 的 `default` 分组里，`gpt-image-2` 当前会优先选 `channels.id=18 / 图片上游apimart / https://api.apimart.ai`，因为它的 ability/channel priority 高于 `channels.id=20 / 自有账号 / https://lmage2.dimilinks.com`。因此登录态 `POST /pg/images/generations?async=true` 若命中 `group=default`，请求会直接走下游 APIMart，不会进入当前号池；若要先经过号池，需在下游调整 `gpt-image-2` 的分组归属或优先级。

## 排查入口

- 慢单先分层：`new-api` 分组/任务状态、gpt2api `image_tasks`、外置 `codex-cli-proxy-image` 健康、内置 Runner、`/p/img` 回源/超分/保存链路分别看；不要只补账号。
- 成功任务 `created_at -> started_at` 等待时间正常但总体慢时，优先查外置渠道连续超时；当前异步任务改为“双层预算”：整单总窗口无参考图 8 分钟、参考图 8 分 30 秒；外置 Codex/APIMart 渠道在整单窗口内单独占用 4 分 30 秒，free runner 兜底只复用剩余时间，不再重新起一整段独立倒计时。
- `/p/img/<task>` 首次代理取图或超分可能额外消耗数秒到二十余秒，不计入 `image_tasks.finished_at`；区分“生成耗时”和“代理下载/超分/保存耗时”。
- 参考图不生效先看 gpt2api 图片参数日志的 `reference_count`：`0` 表示前端或 `new-api` 没传到号池，`>0` 后再查上传和上游生成效果。
- 图片裂图先看任务是否有 `file_ids` 和单图代理元信息；浏览器直接访问上游临时图链失败，通常不是任务生成失败。
- 修改 `deploy/nginx.conf` 后若容器内仍读取旧配置，优先重建 `gpt2api-nginx`，不要只依赖 `nginx -s reload`。
- CLIProxyAPI 管理界面当前经公网域名可访问，安全性依赖强管理密钥；若要改回仅本机可用，需要重新加 Nginx 层拦截。

## 最近变更

- 2026-04-29：`fix(image): retry transient reference fetch failures` 已正式部署到当前号池。执行路径为：在当前仓库运行 `bash deploy/build-local.sh` 生成最新 `deploy/bin/gpt2api` 与 `web/dist/`，随后执行 `docker compose -f deploy/docker-compose.yml build server && docker compose -f deploy/docker-compose.yml up -d server` 重建 `gpt2api-server`；之后在 `/home/ubuntu/CLIProxyAPI` 执行 `docker compose up -d --no-build --pull never --force-recreate cli-proxy-api`，让 `deploy_default` 外部网络配置持久生效且不拉取新镜像。部署后 `gpt2api-server` 启动时间为 `2026-04-29 15:44:32 +0800`，`cli-proxy-api` 启动时间为 `2026-04-29 15:44:54 +0800`，本机 `https://127.0.0.1/healthz`、容器内 `http://127.0.0.1:8080/healthz` 与 `http://cli-proxy-api:8317/healthz` 均返回正常；`gpt2api-server` 启动日志记录本次重启将 `3` 个运行中图片任务标记为 `interrupted`。
- 2026-04-29：已定位并修复本轮大量 `openai: image request ... lookup cli-proxy-api on 127.0.0.11:53: no such host`。根因不是数据库路由或账号池失效，而是 `cli-proxy-api` 容器在最近一次重建后只挂在 `cliproxyapi_default`，没有继续加入 `deploy_default`，导致 `gpt2api-server` 容器内 DNS 无法解析 `cli-proxy-api`。现场先用 `docker network connect deploy_default cli-proxy-api` 恢复运行态，再把 `/home/ubuntu/CLIProxyAPI/docker-compose.yml` 改成默认同时加入外部网络 `deploy_default`，避免下次 `docker compose up` 后再次漂移。修复后容器内 `getent hosts cli-proxy-api` 与 `http://cli-proxy-api:8317/healthz` 已恢复正常，随后新任务已再次出现成功单。
- 2026-04-29：已为 JSON 参考图下载补上短重试，覆盖 `5xx/408/429` 与 `timeout/EOF/connection reset/broken pipe` 等瞬态失败；同时把单次 HTTP 拉取超时放宽到 `20s`，并新增 `internal/gateway/images_reference_test.go` 覆盖“`5xx` 后重试成功”“读 body 中断后重试成功”“`404` 不重试”三类场景。边界不变：参考图 URL 若持续不可达，仍在落任务前直接返回 `400 invalid_reference_image`，不会创建残留 `image_tasks`。
- 2026-04-28：已将 `sub2api-valid-openai-json-20260427-105111.zip` 中可用于生产 Codex 图片渠道的账号导入本机 `CLIProxyAPI` 文件池。导入策略为：跳过 `1` 个 `free` 账号，新增 `20` 个 `plus` 文件，并对与现网重合的 `10` 个文件做保守更新——仅当 zip 中 token 明显更新时才覆盖，避免把 `CLIProxyAPI` 已自动刷新的新 token 回滚成旧值。按 `2026-04-27` live-check 结果，`5` 个 `ok` 新号设为 `priority=1` 主池，`15` 个 `quota_or_rate_limit` 新号设为 `priority=0` 冷备；原来仍在主池但最新 live-check 已 429 的重合号也降为 `priority=0`。导入后 `scripts/check-codex-auth-plans.sh` 校验通过，当前文件池为 `34` 个 auth（`plus=31`、`team=3`、`free=0`），其中 `29` 个启用、`5` 个禁用。导入时 `cli-proxy-api` watcher 已热加载这些文件；但日志同时表明，旧池里已有少量账号、以及本次提升到主池的部分新号，在自动 refresh 时会返回 `401`，说明这些号当前 access token 可继续用到过期，但 refresh token 并不都长期稳定，后续若追求长期稳定容量，应补 fresh OAuth。
- 2026-04-28：`fix(image): align apimart reference image protocol` 已部署到当前号池。部署路径为：在构建机 `43.152.240.30` 的 `/home/ubuntu/gpt2api-build-6d119d5` 运行 `bash deploy/build-local.sh` 产出 `deploy/bin/gpt2api`、`deploy/bin/goose` 和 `web/dist/`，再回传到当前仓库后执行 `docker compose -f deploy/docker-compose.yml build server && docker compose -f deploy/docker-compose.yml up -d server`。部署后 `gpt2api-server` 于 `2026-04-28 16:07:03 +0800` 重启，容器健康状态为 `healthy`，容器内 `http://127.0.0.1:8080/healthz`、容器内到 `http://cli-proxy-api:8317/healthz` 以及本机 `https://127.0.0.1/healthz` 均返回 `ok`。
- 2026-04-28：已对齐 APIMart `gpt-image-2` 官方文档的图生图协议。当前 `apimart.ai` 渠道在存在参考图时不再走通用 OpenAI 兼容的 `/v1/images/edits`，而是改走 `/v1/images/generations` 并发送 `image_urls[]`；同时 `openai` 适配器开始读取 `upstream_channels.extra` 中的 `official_fallback` 布尔配置并透传给 APIMart。已补适配器单测覆盖“参考图走 generations + image_urls”和“official_fallback 透传”。
- 2026-04-28：已修复异步图片渠道成功任务在 `file_ids=null` 且 `result_urls` 为 `data:image/...;base64` 时，后台“生成记录”详情和 `/v1/tasks/{id}` 直接返回多 MB JSON 的问题。当前逻辑会把这类内联图片也改走本站 `/p/img/...` 代理，由代理按需解码并返回图片字节；这样不会再把整张图 base64 直接回给前端。排查时已确认这不是整机负载问题：当时主机负载不高，但最近任务中大量成功任务的 `result_urls` 长度在 3MB~14MB，足以触发管理后台 30 秒 axios 超时。修复已在当前号池 `gpt2api-server` 部署。
- 2026-04-28：已把 `apimart(channel_id=2)` 补上映射 `gpt-image-2 -> gpt-image-2 / modality=image`，并在当前号池部署“APIMart 异步任务 + 比例尺寸/分辨率保留”修复。真实烟测时短暂停掉 `cli-proxy-api`，日志出现 `channel_id=1 codex-cli-proxy-image ... no such host` 后，同一任务 `img_de94e2474a8b4a21ac64fe13` 最终 `succeeded`，结果图来自 `upload.apimart.ai`，证明链路已按“Codex 失败 -> APIMart -> 内置 free runner”顺序工作。
- 2026-04-28：已补齐构建机 `43.152.240.30` 的基础构建环境。`ubuntu` 用户下安装了系统级 `nodejs`/`npm`，并把 `/usr/local/go/bin` 提前注入到 `~/.bashrc` 的非交互分支与 `~/.profile`，保证 `ssh 构建机 'cmd'` 这类远程非交互执行也能直接拿到 `go`。随后在构建机同步当前工作树并完整跑通 `bash deploy/build-local.sh`，成功产出 `deploy/bin/gpt2api`、`deploy/bin/goose` 和 `web/dist/index.html`；当前只剩 Sass legacy JS API 与 Vite 大 chunk 警告，不影响构建成功。
- 2026-04-28：已将当前 Codex 环境公钥对应的私钥 `~/.ssh/cliproxyapi_212_50_232_214_ed25519` 加入构建机 `43.152.240.30` 上 `ubuntu` 用户的 `~/.ssh/authorized_keys`，并完成免密 SSH 验证。后续涉及老前端或画布构建时，可直接从当前号池机器进入构建机，不再依赖临时密码登录。
- 2026-04-28：为 OpenAI 兼容图片适配器补充 APIMart 异步任务兼容。`/v1/images/generations` 若返回 `task_id`，服务端会自动轮询 `/v1/tasks/{id}` 并提取 `result.images[].url`；这样数据库里的 `apimart` 渠道可以作为 `gpt-image-2` 的第二跳外置兜底，而不是只能直落内置 free runner。
- 2026-04-28：量化最近 24 小时 `gpt2api-server` 日志，外置 Codex 图片渠道触发 `fallback to free account runner` 约 68 次，而异步生图提交约 182 次；主要触发源不是参数错误，而是 `cli-proxy-api:8317` 在旧的 90 秒/2 分钟窗口内大量 `context deadline exceeded`。现已把外置渠道改成“单次独立限时 + 同渠道最多重试一次”，并把外置阶段收敛到统一 `4 分 30 秒` 窗口；其中参考图单次等待也从 3 分钟收短到 2 分钟，避免 Codex/APIMart 两跳都把 free 兜底时间吃空。
- 2026-04-29：继续针对“第一层拖几分钟才切层”收敛策略，代码已调整为“按 route 分配第一层预算”，不再让 `codex-cli-proxy-image` 靠两次 `timeout` 吃光整段外置窗口；同时对 `context deadline exceeded / timeout` 不再在同一渠道内重试，只保留 `502/EOF/stream disconnected` 这类快失败的短重试。当前本地阈值为：无参考图单 route `90s`、两条 route 总预算 `3m30s`；有参考图单 route `2m`、两条 route 总预算 `4m30s`。截至本次记录时该改动已在仓库通过 `go test ./internal/gateway/...`，但尚未热部署，因为线上仍有进行中的图片任务，直接重启会把它们标成 `interrupted`。
- 2026-04-29：用户反馈“下游前端生成了几分钟结果任务都没了”后，已跨系统核对 `1575 / 1589` 对应任务，确认号池 `image_tasks` 与下游 `new-api.tasks` 均保留了成功/失败记录，不存在后端删任务。根因在下游前端 `new-api-web` 的 AI 创作作品库：它原先只展示“已有生成图资产”或“仍含正在生成文案”的消息，导致图片任务超时/失败后会从作品库直接消失。该问题已在下游前端提交 `725f769 fix(ai-studio): keep failed image tasks visible` 修复并发布到 `https://dimilinks.com/`；后续再遇到“任务没了”先区分是前端展示消失，还是号池/后端真实丢单。
- 2026-04-28：继续收敛异步图片长尾后确认，`10+` 分钟不出图的主因是“外置图片渠道超时后，再给 free runner 新开一整段总预算”，而不是任务根本没下发。现已将图片任务改为共享总窗口：无参考图整单 8 分钟、参考图整单 8 分 30 秒；外置渠道仍只占用前 4 分 30 秒，Codex/APIMart 连续超时后，free runner 只吃剩余时间，不再把总时长串行叠加到 10~15 分钟以上。此次阈值保留了近 24 小时里真实存在的 `7m53s` 参考图成功单空间，同时把此前 `12m47s` 的失败长尾提前收口。
- 2026-04-28：上述“异步图片总预算收敛”已联动部署到三段链路。当前号池 `gpt2api` 已上线共享总窗口策略；下游后端 `new-api` 已把同步图片等待上限从 10 分钟收短到 9 分钟；下游前端 `new-api-web` 已把首轮轮询从 10 秒提前到 3 秒，并把总等待提示窗口从 15 分钟收短到 9 分钟。部署后分别验证了 `gpt2api-server` 健康检查、`new-api-local` 的 `/api/status`、以及 `https://dimilinks.com/` 的线上 hash，三边均已生效。
- 2026-04-28：上述“减少 Codex 会员链路过早切 Free”修复已在当前号池部署。执行 `bash deploy/build-local.sh`、`docker compose -f deploy/docker-compose.yml build server`、`docker compose -f deploy/docker-compose.yml up -d server` 后，`gpt2api-server` 容器重新启动并恢复 `healthy`；容器内 `http://127.0.0.1:8080/healthz` 与 `http://cli-proxy-api:8317/healthz` 均返回 ok。本次重启有 1 个运行中图片任务被标记为 interrupted。
- 2026-04-27：手工移植元项目 2026-04-26 的关键修复：图生图 SSE 结果会剔除参考图 file_id；用量日志成功图片按真实产出张数写入并对历史 image_count=0 兜底；账号额度探测支持 max_value/cap/total/limit 和“今日已用+剩余”估算；UploadFile 创建文件步骤加入瞬时错误重试；在线体验参考图限制对齐后端 4 张/20MB。
- 2026-04-27：上述元项目关键修复已部署到当前号池 `gpt2api-server`；部署命令为 `bash deploy/build-local.sh` 后 `docker compose -f deploy/docker-compose.yml build server && docker compose -f deploy/docker-compose.yml up -d server`，本机与 Nginx `/healthz` 均返回 ok，容器内 `cli-proxy-api:8317/healthz` 可达。重启时有 1 个运行中图片任务被标记为 interrupted。
- 2026-04-27：外置图片渠道等待窗口收敛为无参考图 90 秒、有参考图 2 分钟；超时后尽快走内置 Runner 兜底，下游前端轮询窗口需覆盖到 15 分钟。
- 2026-04-27：对外 `/p/img` 代理图统一补绝对 URL，避免下游把相对路径补到错误域名后下载到 HTML。
- 2026-04-27：管理员“生成记录”改为轻量列表与懒加载图片/失败详情；不要把 base64/data URL 或 `result_urls` 大字段重新放回列表接口。
- 2026-04-29：`image_tasks` 已新增结构化 `provider_trace`，同步/异步图片请求都会记录原始命中 provider、每一步尝试顺序、触发 free fallback 的原因、最终成功 provider，以及最终使用的内置账号（若落到 runner）。管理侧“生成记录”列表与详情会直接展示这条链路，`codex / apimart / free runner` 不再需要靠日志人工反推。边界是：外置渠道内部若还有更细的 auth 文件/子账号选择，当前号池只能看到命中的 channel，拿不到它们各自服务内部的具体 auth 标识。
- 2026-04-29 19:51（Asia/Shanghai）：`feat(image): trace provider fallback chain` 已部署到当前号池。执行流程为：在当前仓库运行 `bash deploy/build-local.sh` 重新产出 `deploy/bin/gpt2api` 与 `web/dist/`，随后执行 `docker compose -f deploy/docker-compose.yml build server && docker compose -f deploy/docker-compose.yml up -d server` 重建 `gpt2api-server`；容器启动时自动执行 goose，`20260429000001_image_tasks_provider_trace.sql` 已成功应用，库内确认存在 `image_tasks.provider_trace`。部署后 `gpt2api-server` 于 `2026-04-29 19:51:33 +0800` 启动并恢复 `healthy`，本机 `https://127.0.0.1/healthz`、容器内 `http://127.0.0.1:8080/healthz` 与容器内 `http://cli-proxy-api:8317/healthz` 均返回正常；本次重启日志记录有 `1` 个运行中图片任务被标记为 `interrupted`。
- 2026-04-29：图片外置兜底已收敛为“可配置策略”，入口在系统设置 `gateway.image_*`。当前支持五项热更：`image_channel_fallback_order`（按 provider/渠道名配置外置顺序）、`image_account_fallback_order`（按账号类型配置内置 runner 兜底顺序，支持 `free/plus/team/pro/codex/any/none`）、`image_channel_cooldown_sec`（连续失败后的冷却时间）、`image_channel_fail_threshold`（连续失败熔断阈值）、`image_skip_codex_to_apimart`（有 APIMart route 时直接跳过 Codex）。实现边界：该策略只作用于图片链路，不改文字渠道；“渠道冷却/熔断”复用 `upstream_channels.fail_count + last_test_at` 推断，不额外引入新状态表。
- 2026-04-29：管理侧“生成记录”已新增 `/api/admin/image-tasks/stats` 最小命中统计面板，直接基于 `image_tasks.provider_trace` 聚合最近窗口内的总任务、成功率、fallback 触发次数、各 provider 首跳/终态命中，以及主要转移链路（如 `Codex -> APIMart`、`Codex -> Free Runner`）。当前目的是辅助继续压低 Codex 兜底频率，而不是替代完整 BI；统计粒度仍停留在 provider / channel / plan_type，不含外置服务内部 auth 文件级别明细。
- 2026-04-29：号池图片链路已补“最小侵入”的卡顿定位日志。`image_tasks.provider_trace` 现在除 provider fallback 外，还会累计 `request_ms / queue_ms / submit_ms / upstream_wait_ms / poll_ms / download_ms / total_ms`；`/v1/images/generations`、`/v1/images/edits`、`chat->image`、Runner、外置 channel(APIMart/Codex/OpenAI/Gemini)都会在关键阶段更新这组 timing，并在阶段过慢时输出 `slow image task stage` / `slow image web request` / `slow image task query` / `slow image dao query`。用途是快速区分卡顿主要落在入口 Web、上游等待、上游轮询还是 `image_tasks` 相关数据库查询；边界是：这些 timing 只覆盖当前号池可见的阶段，拿不到外部服务内部更细的 auth 文件/子任务耗时。
- 2026-04-29：`GET /api/admin/image-tasks/stats` 已在原 provider stats 基础上追加“最近慢任务概览”，支持查询参数 `slow_ms`（默认 `90000`）和 `slow_limit`（默认 `10`，最大 `50`）。返回体会按最近活跃时间给出慢任务列表、各慢阶段计数和每单 `dominant_phase`，便于和上述慢日志互相对照；任务详情接口 `/v1/images/tasks/:id`、`/v1/tasks/:id`、`/api/me/images/tasks*`、管理员生成记录列表也会带上 `timing` 字段，便于不翻日志先看单任务耗时拆分。
- 2026-04-29 20:18（Asia/Shanghai）：`feat(image): add configurable fallback strategy` 已部署到当前号池。执行流程为：在当前仓库运行 `bash deploy/build-local.sh` 重新产出 `deploy/bin/gpt2api` 与 `web/dist/`，随后执行 `docker compose -f deploy/docker-compose.yml build server && docker compose -f deploy/docker-compose.yml up -d server` 重建 `gpt2api-server`。本次 `goose` 启动时确认 `no migrations to run, current version: 20260429000001`；部署后 `gpt2api-server` 启动时间为 `2026-04-29 20:18:30 +0800`，容器健康状态为 `healthy`，本机 `https://127.0.0.1/healthz`、容器内 `http://127.0.0.1:8080/healthz` 与容器内 `http://cli-proxy-api:8317/healthz` 均返回正常。
- 2026-04-27：内置 Runner 与外置图片渠道成功结果落库/结算前统一按请求 `n` 截断，防止上游多产出导致下游展示多图。
- 2026-04-27：新增 `scripts/check-codex-auth-plans.sh`，用于 Codex auth 导入/轮换后拦截 free 或未知 plan 文件。
- 2026-04-27：参考图上传的 Azure PUT/确认链路增加短重试；上传失败归类为 `network_transient`，不再直接当图片参数错误。
- 2026-04-27：下游前端 AI 画布已在构建期修正 `gpt-image-2` payload，把比例尺寸转像素尺寸、`1k/2k/4k` 改为 `resolution`，参考图发往 `/pg/images/generations` 前转为 `data:image`。

## 已清理内容

- 已删除 2026-04-25 至 2026-04-27 的大部分单次任务 ID、smoke 任务耗时、账号导入文件路径、临时容器/临时 token 细节和一次性数据库快照。
- 账号导入、SSE 超时、参考图解析、外置渠道接入、下游日志展示等历史流水已折叠到“当前事实”“排查入口”和 `Corrections.md`。
- 机器拓扑与连接方式已收敛到 `AGENTS.md`；后续不要在本文件复制 SSH 详情，避免形成两套来源。

## 最近变更

- 2026-04-30 10:00（Asia/Shanghai）：复查“免费兜底很多”的原因时确认，`provider_trace` 完整部署后的窗口（自 `2026-04-29 19:51:33` 起）共 81 个图片任务，其中 50 个最终命中 `free_runner`、31 个命中 `apimart`、0 个命中 `codex`。根因不是免费兜底策略误触发，而是外置链路连续失败后按既定策略兜底：Codex 渠道 `codex-cli-proxy-image` 仍按默认顺序排第一，但当前 `fail_count=56/status=unhealthy`，常见失败为 `unknown provider for model gpt-image-2`；APIMart 作为第二跳的主要失败为 `context deadline exceeded`，其次是 4K 比例不支持（例如 `4K file does not support ratio=4:3/1:1/3:4`）。线上尚未写入 `gateway.image_*` 热更项，因此当前使用代码默认 `image_channel_fallback_order=codex,apimart`、`image_account_fallback_order=free`、`cooldown=300s`、`fail_threshold=3`、`skip_codex_to_apimart=false`。
