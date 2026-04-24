# Documentation

## 当前状态

- 后台账号池目前支持 `JSON / OAuth / AT / RT / ST` 五种导入方式。
- 新增的 OAuth 导入只负责把 OpenAI 官方 OAuth 登录流接进现有账号池，账号入库、AES 加密、自动刷新、额度探测、代理绑定仍复用原有逻辑。

## 最近变更

### 2026-04-24 修复 N>1 图片历史缩略图不可见

- 结论：图片任务历史中 `n=3` 成功任务显示破图，后端日志显示 `/p/img/...` 返回 `503/502`；根因是并发生图时每张图可能来自不同账号和 conversation，但旧逻辑只在 `image_tasks` 里保存单个 `account_id/conversation_id`，且并发子任务没有回写任务账号，代理下载无法定位对应账号。
- 决策：在 `file_ids` 的单图元素内写入 `account_id + conversation_id + file_ref` 元信息，图片代理优先读取单图元信息回源；非新格式继续兼容旧任务，仍回退到任务级 `account_id/conversation_id`。
- 影响：后续 `N>1` 并发生图的历史缩略图、打开原图和下载都会按每张图的真实账号下载，不再因为 `account_id=0` 或旧账号软删而破图。
- 线上处理：已重新编译并重建 `gpt2api-server`；同时把 2026-04-24 19:50:53 的 `33333` 任务根据运行日志补写了三张图的单图元信息。
- 验证：`go test ./...` 通过；`bash deploy/build-local.sh` 前后端产物构建通过；`docker compose -f deploy/docker-compose.yml up -d --build server` 后服务健康。

### 2026-04-24 升级到 upstream/main 最新版

- 结论：本地 `main` 已合并 `github.com/432539/gpt2api` 的 `upstream/main`，最新上游提交为 `d36db51 feat(channel): multi-upstream channels with OpenAI/Gemini adapters + text/image routing`。
- 决策：合并时保留本地 OAuth 账号导入、额度汇总、个人图片代理与 nginx/端口部署定制，同时接入上游新增的多上游渠道、移动端响应式、图片任务后台与 N>1 并发生图修复。
- 冲突处理：`internal/image/runner.go` 保留上游并发生图与本地额度即时扣减逻辑；`internal/server/router.go` 接入 `ChannelH`；`web/src/views/admin/Accounts.vue` 同时保留 OAuth 回调监听与额度汇总加载。
- 验证：`go test ./...` 通过；`cd web && npm run build` 通过，构建期间仅出现 Sass legacy JS API 与大 chunk 体积警告。
- 边界：本地部署改动仍以独立提交保留，默认不 push；如需要线上生效，还需重建前端/后端产物并重启 Docker Compose 服务。

### 2026-04-24 gpt2api/200.zip 账号导入

- 结论：`/home/ubuntu/gpt2api/200.zip` 内含 200 个 `codex-*.json` token 文件，格式与现有账号导入器兼容。
- 决策：继续通过 gpt2api 项目自身 `internal/account.Service.ImportBatch` 导入，避免绕过 AES 加密、邮箱去重、状态复活等现有规则。
- 结果：解析 200 个账号，导入 200 个账号，200 个新建、0 个失败；导入后 `oai_accounts` 总数为 215，其中本次 zip 账号 200 个均为未删除 `healthy` 状态。
- 安全处理：导入后将含密钥的压缩包从仓库根目录移动到已忽略的 `tmp/200.zip`，避免作为未跟踪文件被误提交。
- 边界：导入前已存在的 15 条账号记录当前为软删除状态，未在本次导入中恢复；本次只保证 zip 内 200 个账号已加入并处于可调度状态。

### 2026-04-24 CLIProxyAPI 账号导入 gpt2api

- 结论：`/home/ubuntu/CLIProxyAPI/200.zip` 当前未在服务器文件系统中找到；本次改用已存在的 `/home/ubuntu/CLIProxyAPI/auths/*.json` 作为账号来源。
- 决策：通过 gpt2api 项目自身 `internal/account.Service.ImportBatch` 逻辑导入账号，而不是直接拼 SQL，确保 `AT / RT` 按当前 `CRYPTO_AES_KEY` 加密，重复邮箱按现有导入规则更新。
- 结果：识别并导入 14 个账号文件，14 个新建、0 个失败；导入后 `oai_accounts` 总数为 15，未删除账号数为 15。
- 边界：本次只导入了 `auths` 目录下实际存在的 14 个 JSON 账号；没有导入不存在的 `200.zip`，也没有把 token 明文写入仓库或日志。

### 2026-04-24 gpt2api 外网主页分流修正

- 结论：`https://lmage2.dimilinks.com/` 已恢复为 gpt2api 项目主页；`https://cliproxyapi.845817074.xyz/` 继续进入 CLIProxyAPI。
- 决策：
  - 将 gpt2api 默认 HTTPS 站点显式声明为 `listen 443 ssl default_server`，并把 `lmage2.dimilinks.com` 写入该站点的 `server_name`。
  - 将默认 HTTP 站点显式声明为 `listen 80 default_server`，避免非 `cliproxyapi.845817074.xyz` 的域名落入 CLIProxyAPI 入口。
  - 因 Docker 文件绑定挂载保留旧 inode，修改 `deploy/nginx.conf` 后需要重建 `gpt2api-nginx` 容器，而不是只执行 `nginx -s reload`。
- 原因：
  - 之前 `cliproxyapi.845817074.xyz` 的 443 server 块排在 gpt2api 默认站点前面，而 gpt2api 站点没有显式 `default_server`，导致 `lmage2.dimilinks.com` 外网访问误落到 CLIProxyAPI。
- 影响：
  - 公网访问 `https://lmage2.dimilinks.com/` 返回 gpt2api 前端 HTML。
  - 公网访问 `https://cliproxyapi.845817074.xyz/` 仍返回 CLIProxyAPI，不影响 CLIProxyAPI 独立入口。

### 2026-04-22 CLIProxyAPI 公网入口接入

- 结论：`cliproxyapi.845817074.xyz` 已改由当前服务器接管，并通过现有 `gpt2api-nginx` 统一处理 `80/443` 入口。
- 决策：
  - 为 `cliproxyapi.845817074.xyz` 单独签发 Let’s Encrypt 证书，并让 Nginx 按域名分流到 `cli-proxy-api:8317`。
  - `cli-proxy-api` 额外加入 `deploy_default` 共享 Docker 网络，避免把其本地监听端口直接暴露到公网。
  - Nginx 上游改为通过 Docker 内置 DNS `127.0.0.11` 动态解析，而不是在启动时固化容器 IP。
  - 公网反代显式屏蔽 `/management.html` 与 `/v0/management*`，管理功能继续只走本机 `127.0.0.1:8317`。
  - `gpt2api-nginx` 挂载 `/etc/letsencrypt`，并通过 certbot renewal hook 在证书续期后自动 `nginx -s reload`。
- 原因：
  - Cloudflare Zone 当前使用 `SSL=Strict`，原有 Nginx 证书不覆盖 `cliproxyapi.845817074.xyz`，仅改 DNS 会导致回源 TLS 校验失败。
  - 复用现有前置 Nginx 比额外开放新公网端口更简单，也更便于统一管理 TLS。
- 影响：
  - 外网访问 `https://cliproxyapi.845817074.xyz` 现在会进入 CLIProxyAPI，而不是落到旧目标机器。
  - `gpt2api` 默认站点流量保持不变，因为新入口使用独立 `server_name` 分流。
  - CLIProxyAPI 的管理面既可在本机使用，也可通过公网域名访问；因此必须依赖管理密钥做鉴权。
  - 后续即使 `gpt2api-server` 或 `cli-proxy-api` 容器因重建而更换 Docker IP，Nginx 也能自动跟随解析，不必手工改 upstream IP。

### 2026-04-22 CLIProxyAPI 管理界面外网开放

- 决策：
  - 移除 `cliproxyapi.845817074.xyz` 站点中对 `/management.html` 与 `/v0/management*` 的 Nginx 层拦截。
  - 在放开公网入口前，先把 CLIProxyAPI 的管理密钥轮换为新的强随机值。
- 原因：
  - 之前公网仅开放 API，不开放管理面；若直接取消拦截而继续沿用示例风格密钥，风险过高。
- 影响：
  - 现在可直接从公网访问管理页面并登录。
  - 旧的本地示例管理密钥失效，后续应使用新的管理密钥。

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
