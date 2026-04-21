# Documentation

## 当前状态

- 后台账号池目前支持 `JSON / OAuth / AT / RT / ST` 五种导入方式。
- 新增的 OAuth 导入只负责把 OpenAI 官方 OAuth 登录流接进现有账号池，账号入库、AES 加密、自动刷新、额度探测、代理绑定仍复用原有逻辑。

## 最近变更

### 2026-04-22 个人图片任务结果改走代理 / preview 判定修正

- 结论：网页“在线体验”和个人图片面板里的裂图/空白，不是任务没成功，而是 `/api/me/images/tasks` 与 `/api/me/images/tasks/:id` 之前直接返回了 `image_tasks.result_urls` 里的上游临时直链。对 `sediment / estuary` 这类需要账号鉴权的图片，浏览器直接拿来做 `<img src>` 会 403 或坏图。
- 决策：
  - 个人图片接口不再把上游 `result_urls` 透给前端，而是和 `/v1/images/tasks/:id` 一样，统一返回自家 `/p/img/<task_id>/<idx>` 代理 URL。
  - 图片代理 URL 的签名逻辑下沉到 `internal/image` 包，供网关接口和个人图片接口共用，避免两套实现再次漂移。
  - preview 判定不再用“`file_id` 是否以 `sed:` 开头”做推断，而是在真正 IMG1 兜底时持久化写入 `preview:` 前缀；对外展示时再去掉内部标记。
- 原因：
  - 日志已证明“正常成功的 IMG2 最终结果”也可能落成 `sed:` 引用，因此 `sed:` 只能表示存储/下载通道，不能表示“这一定是 preview”。
  - 只靠前端拿到上游短链展示，既受时效影响，也无法处理必须带 Bearer 鉴权的下载链路。
- 影响：
  - 个人图片面板、在线体验页面刷新后重新拉到历史任务时，会拿到稳定的同源代理图链，不再因为上游临时 URL 失效或鉴权缺失而裂图。
  - “本次未使用 IMG2 灰度生成”这类 preview 提示只会在真正 IMG1 兜底时出现，减少误报。
  - 历史任务即使最初保存的是上游旧直链，只要 `file_ids` 仍在，重新查询任务时也会动态生成新的代理 URL。

### 2026-04-22 在线体验图片任务恢复 / 排队状态修正

- 结论：图片任务已经改成异步后，真正影响体验的不只是上游慢，还有两个前端/状态层问题：
  - 任务在等待账号调度时，后台过早把状态写成 `running`，网页看不出它其实还在排队。
  - 用户刷新页面、重新进入“在线体验”或点击“停止”后，只是停止了本地轮询，后台任务仍继续跑，但页面会丢失这张图的跟踪。
- 决策：
  - `image.Runner` 改为拿到真实账号 lease 后再把任务切到 `running`，让 `dispatched` 阶段真实表示“排队中”。
  - 在线体验把文生图 / 图生图的 `task_id` 持久化到浏览器本地；页面重新加载时会自动恢复等待，用户手动“停止等待”后也可以点“继续查看”接回结果。
- 影响：
  - 当唯一图片账号正忙时，网页会更诚实地显示“排队中”，不再把等待调度和真正生成混在一起。
  - 任务一旦已经提交到后台，即使页面刷新、切页、重新打开，用户仍能在网页里继续看到最新状态和最终结果，不容易再出现“后台其实成功了，但网页没看到”的情况。
  - “停止”现在语义更接近“停止等待”，不会误导成服务端任务已取消。

### 2026-04-21 Playground 生图超时 / Turnstile 排查

- 结论：`image 524` 不是账号绑定失败，而是图片请求在站点前面经过 Cloudflare 时同步等待过久，浏览器先超时断开；同时图片 runner 之前没有把账号 cookies 带给 `chatgpt` 客户端，容易触发 `chat-requirements` 的 Turnstile 挑战。
- 决策：
  - 图片 runner 改为复用账号 cookies，尽量降低 Turnstile 命中率。
  - Playground 生图请求改为默认 `wait_for_result=false`，先立即返回 `task_id`，前端再轮询 `/api/me/images/tasks/:id` 拿最终结果，避免网页侧直接出现 Cloudflare `524`。
- 影响：
  - 用户在“在线体验”里文生图 / 图生图时，不再同步卡住 2 分钟以上；即使上游很慢，也会先看到任务排队/轮询，而不是直接被站点层超时打断。
  - 如果任务最终仍失败，前端会优先展示更接近真实原因的错误文案，例如上游风控/Turnstile，而不是笼统的 `image 524`。

### 2026-04-21 OpenAI OAuth 导入

- 决策：新增 `/api/admin/accounts/oauth/generate-auth-url` 与 `/api/admin/accounts/oauth/exchange-code` 两个后台接口，并在账号池“批量导入”弹窗里增加 `OAuth 登录` 页签。
- 原因：当前库已经能完整存储 `AT / RT / client_id / chatgpt_account_id`，问题主要在“账号获取方式不方便”，因此选择补一条更顺手的导入通道，而不是重写整套账号模型。
- 影响：管理员可以直接通过 OpenAI OAuth 登录拿到 `AT / RT` 并导入账号池，导入完成后会继续进入现有的自动刷新、额度探测和调度流程。
- 边界：
  - OAuth 默认回调已切回 OpenAI/Codex 官方常用的 `http://localhost:1455/auth/callback`，因为内置 `client_id=app_EMoamEEZ73f0CkXaXp7hrann` 并不稳定支持任意站点域名作为回调；若强行使用站点回调，OpenAI 侧可能直接报 `验证过程中出错 (unknown_error)`。
  - 站点域名回调 `/oauth/openai/callback` 仍保留为前端可选模式。只有当 OpenAI 侧实际接受该站点回调时，才会通过 `postMessage/localStorage` 自动把 `code/state` 回传到账号池导入弹窗；否则应改用默认官方回调并手动粘贴最终 URL 或 `code`。
  - OAuth 会话状态只保存在服务端内存，TTL 为 30 分钟；服务重启后需要重新生成授权链接。
  - `proxy_id` 在 OAuth 导入里既用于服务端向 `auth.openai.com` 换 token，也用于新建账号时的默认代理绑定；如果是更新已有账号，不会自动改绑它原来的代理。
  - 现有 `JSON / AT / RT / ST` 导入链路保持不变，OAuth 只是新增入口，不替换旧流程。
