# Documentation

## 当前事实

- 账号池支持 `JSON / OAuth / AT / RT / ST` 五种导入方式；账号入库、AES 加密、自动刷新、额度探测、代理绑定共用 `internal/account` 现有逻辑。
- OAuth 导入只是新增获取账号凭据的入口，不替换旧导入链路；默认回调仍优先使用 OpenAI/Codex 常用的 `http://localhost:1455/auth/callback`。
- 图片生成已改为异步任务：前端提交后保存 `task_id` 并轮询任务接口；`dispatched` 表示等待账号调度，拿到账号 lease 后才进入 `running`。
- 图片生成兼容下游任务协议：`POST /v1/images/generations?async=true` 与 `Prefer: respond-async` 会按异步任务返回；`GET /v1/tasks/:id` 是 `GET /v1/images/tasks/:id` 的兼容别名。
- 图片任务对前端展示时优先返回本站 `/p/img/<task_id>/<idx>` 签名代理 URL，不直接暴露上游临时 `result_urls`；缺少 `file_ids` 的极老任务才兜底旧直链。
- `file_ids` 的单图元素可携带 `account_id / conversation_id / file_ref`；图片代理优先使用单图元信息回源，兼容旧任务的任务级账号信息。
- 本地已合并上游多渠道能力，并保留 OAuth 导入、额度汇总、个人图片代理、nginx/端口等本地定制。
- `deploy/nginx.conf` 当前由同一个 `gpt2api-nginx` 处理公网入口：`lmage2.dimilinks.com` 进入 gpt2api，`cliproxyapi.845817074.xyz` 进入 CLIProxyAPI。
- 号池服务器 `212.50.232.214` 的 `root` SSH 授权已包含当前 Codex 公钥；推荐通过 `22222` 端口连接：`ssh -p 22222 root@212.50.232.214`。
- 若 SSH 未自动选中私钥，使用当前本机私钥路径显式连接：`ssh -i /home/ubuntu/.ssh/cliproxyapi_212_50_232_214_ed25519 -p 22222 root@212.50.232.214`。

## 长期注意事项

- 不要用 `file_id` 是否以 `sed:` 开头判断 IMG1/preview；`sediment / estuary` 也可能是正常 IMG2 最终结果，只有内部持久化的 `preview:` 前缀才表示真正兜底。
- 修图片裂图时先看任务是否有 `file_ids` 和单图代理元信息；浏览器直接访问上游临时图链失败，通常不是任务生成失败。
- `N>1` 并发生图可能每张图来自不同账号和 conversation；不要再假设一个任务只有一个可用于下载全部图片的账号上下文。
- OAuth 会话状态保存在服务端内存，TTL 为 30 分钟；服务重启或超时后需要重新生成授权链接。
- OAuth 的 `proxy_id` 同时用于服务端换 token 和新建账号默认代理绑定；更新已有账号时不会自动改绑原代理。
- 修改 `deploy/nginx.conf` 后，如容器内仍读取旧配置，优先重建 `gpt2api-nginx`，不要只依赖 `nginx -s reload`。
- CLIProxyAPI 管理界面当前经公网域名可访问，安全性依赖强管理密钥；若要改回仅本机可用，需要重新加 Nginx 层拦截。

## 已清理的历史流水

- 账号批量导入的具体文件路径、导入数量和一次性数据库计数已删除；这些是阶段性操作记录，不适合作为长期记忆。
- 2026-04-21 至 2026-04-24 的图片任务修复、后台预览修复、Nginx 分流修复已折叠进“当前事实”和“长期注意事项”。
- 曾经“CLIProxyAPI 管理接口公网拦截”和后来“公网开放管理界面”的冲突记录已改写为当前状态：公网可访问，依赖强密钥。
