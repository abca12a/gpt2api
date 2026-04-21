# Documentation

## 当前状态

- 后台账号池目前支持 `JSON / OAuth / AT / RT / ST` 五种导入方式。
- 新增的 OAuth 导入只负责把 OpenAI 官方 OAuth 登录流接进现有账号池，账号入库、AES 加密、自动刷新、额度探测、代理绑定仍复用原有逻辑。

## 最近变更

### 2026-04-21 OpenAI OAuth 导入

- 决策：新增 `/api/admin/accounts/oauth/generate-auth-url` 与 `/api/admin/accounts/oauth/exchange-code` 两个后台接口，并在账号池“批量导入”弹窗里增加 `OAuth 登录` 页签。
- 原因：当前库已经能完整存储 `AT / RT / client_id / chatgpt_account_id`，问题主要在“账号获取方式不方便”，因此选择补一条更顺手的导入通道，而不是重写整套账号模型。
- 影响：管理员可以直接通过 OpenAI OAuth 登录拿到 `AT / RT` 并导入账号池，导入完成后会继续进入现有的自动刷新、额度探测和调度流程。
- 边界：
  - 默认回调地址固定为 `http://localhost:1455/auth/callback`，当前实现不在本机监听该端口；用户完成登录后，需要把浏览器最终跳转的完整地址复制回后台，前后端会自动提取 `code/state`。
  - OAuth 会话状态只保存在服务端内存，TTL 为 30 分钟；服务重启后需要重新生成授权链接。
  - `proxy_id` 在 OAuth 导入里既用于服务端向 `auth.openai.com` 换 token，也用于新建账号时的默认代理绑定；如果是更新已有账号，不会自动改绑它原来的代理。
  - 现有 `JSON / AT / RT / ST` 导入链路保持不变，OAuth 只是新增入口，不替换旧流程。
