# scripts

运维/自测辅助脚本集合。

> **⚠️ 注意** 本目录下所有 `--admin-email` / `--admin-pass` / `--user-email` / `--user-pass` 以及
> 形如 `admin@smoke.test`、`Admin123456`、`User123456` 这样的字面量,都是**冒烟脚本自己创建测试账号时用的临时凭证**,
> 它们 **不是** 部署后的默认管理员账号密码。
>
> GPT2API 本身不内置任何默认账号 —— **首位访问 `/register` 的用户自动成为 admin**(见根目录 README.md 的部署说明)。

## smoke.mjs · e2e 冒烟

对已启动的后端(本地 `go run` 或 `docker compose up -d`)做一轮端到端闭环自检。

### 覆盖用例


| #   | 用例                       | 说明                                                      |
| --- | ------------------------ | ------------------------------------------------------- |
| 1   | `/healthz`               | 后端可达                                                    |
| 2   | 首位用户自动 admin             | 若 users 表为空,register 的第一个账号自动拿到 admin 角色                |
| 3   | 普通用户注册 / 登录              |                                                         |
| 4   | `/api/me`、`/api/me/menu` | 断言 admin/user 各自的 role、permissions、menu 非空              |
| 5   | API Keys CRUD            | 用户视角 create / list / patch(禁用)/ delete                  |
| 6   | 越权校验                     | user token 访问 `/api/admin/*` 应 401/403;匿名访问 admin 应 401 |
| 7   | Admin 用户 / 分组列表          |                                                         |
| 8   | 调账 `+delta`              | 正确密码过,错误密码被拒(403),流水可查,用户余额同步                           |
| 9   | 审计日志                     | 含 `users.credit.adjust` 等动作                             |
| 10  | 备份链路                     | 创建 → 列表包含 → 下载 → 删除(二次密码);宿主缺 `mysqldump` 时跳过           |


### 用法

前置条件:Node ≥ 18(原生 fetch / FormData),后端已启动。

```bash
cd scripts
npm run smoke
```

或直接指定参数:

```bash
node scripts/smoke.mjs \
  --base http://localhost:8080 \
  --admin-email admin@smoke.test \
  --admin-pass  Admin123456 \
  --user-email  user@smoke.test \
  --user-pass   User123456
```

- `--keep true` 保留脚本创建的 Key、备份文件(便于后续手动验证)
- 环境变量 `GPT2API_BASE` 可覆盖 `--base`

### 退出码

- `0` 全部通过
- `1` 至少一条 FAIL
- `2` 脚本级异常(如 `/healthz` 不可达)

### 复跑行为

脚本是幂等的——已经存在的账号走登录路径,已经存在的 key 不影响新建。但它假设 "首位用户 = admin" 那步只在空库时成立,所以:

- 对全新库:能完整跑通
- 对已经跑过的库:要么复用相同 admin 账号(`--admin-email` 指向那个),要么清空 users 表再跑

## check-codex-auth-plans.sh · Codex 账号计划校验

检查 `cli-proxy-api` 的 Codex auth 目录，确保图片 Codex 通道只混入 `plus/team` 账号，不允许 `free` 或未知后缀账号文件进入生产通道。

```bash
scripts/check-codex-auth-plans.sh
```

- 默认检查 `/home/ubuntu/CLIProxyAPI/auths`
- 可用 `CODEX_AUTH_DIR=/path/to/auths` 覆盖目录
- 只输出文件名和汇总计数，不读取或打印 token 内容
- 退出码 `0` 表示全部合规；`1` 表示存在 `free/未知` 账号；`2` 表示目录不存在

## gpt-image-2-single-e2e.mjs · 1K/2K/4K 真单联调

对 `gpt-image-2` 的 `1k / 2k / 4k` 单张图请求做可重复联调，自动整理：

- 号池任务状态、`resolution`、`provider_trace`、最终 provider、fallback、timing。
- 号池本机 `image_tasks` 关键字段与最近容器日志。
- 下游后端 `tasks.private_data.billing_context`、`quota`、`upstream_task_id`。
- 前端 `/api/pricing` 是否返回 `resolution_options`，以及当前展示价。
- 可选按订单时点核价，区分“价格漂移”“路由异常”和 `billing_context` 真不一致。

### 发起新请求并轮询

需要提供可调用 `gpt-image-2` 的号池 API Key：

```bash
cd scripts
GPT2API_IMAGE_TEST_KEY=sk-xxx npm run gpt-image-2:e2e -- \
  --base https://lmage2.dimilinks.com \
  --resolutions 1k,2k,4k \
  --n 1 \
  --json-out /tmp/gpt-image-2-e2e.json
```

### 复用已有任务核对

只有号池任务号时：

```bash
node scripts/gpt-image-2-single-e2e.mjs \
  --task-ids img_xxx,img_yyy,img_zzz \
  --json-out /tmp/gpt-image-2-existing.json
```

有下游任务号时，脚本会通过下游 Postgres 的 `private_data.upstream_task_id` 自动映射到号池 `img_*`：

```bash
node scripts/gpt-image-2-single-e2e.mjs \
  --downstream-task-ids task_xxx,task_yyy,task_zzz \
  --json-out /tmp/gpt-image-2-downstream.json
```

复核老单时，显式传入这些订单创建时应使用的分档单价，避免后续改价导致旧 `billing_context` 被误报 FAIL：

```bash
node scripts/gpt-image-2-single-e2e.mjs \
  --downstream-task-ids task_xxx,task_yyy,task_zzz \
  --order-prices 1k=0.06,2k=0.10,4k=0.20 \
  --json-out /tmp/gpt-image-2-old-orders.json
```

`--order-prices` 也可以写成 JSON，例如 `--order-prices '{"1k":0,"2k":0.06,"4k":0.12}'`。提供该参数后，脚本会用它核对 `billing_context.model_price`；当前前端展示价只用于标记 `price_drift`。未提供该参数时，脚本默认按当前前端展示价核对，适合验收新单。

### 常用参数

- `--frontend-pricing-urls`：逗号分隔，默认检查 `https://dimilinks.com/api/pricing` 与 `https://preview.dimilinks.com/api/pricing`。
- `--order-prices`：逗号分隔或 JSON 格式的下单时应价，例如 `1k=0.06,2k=0.10,4k=0.20`；也可用环境变量 `GPT_IMAGE2_ORDER_PRICES`。
- `--skip-local-db true`：不查本机 `gpt2api-mysql`。
- `--skip-logs true`：不抓 `gpt2api-server` 最近 4 小时日志。
- `--skip-downstream-db true`：不通过 SSH 查下游 `new-api-postgres-local`。
- `--downstream-ssh`、`--downstream-db-container`、`--downstream-db-name`、`--downstream-db-user`：覆盖下游连接参数；当前生产默认 `root@212.50.232.214`、`new-api-postgres-local`、`new-api`、`root`。

退出码：`0` 表示没有 FAIL；`1` 表示至少一项核对失败；`2` 表示脚本异常。

## gpt-image-2 n=4 单账号/合并/代理诊断

多图少图排查先看 [`docs/IMAGE_N4_DIAGNOSTICS.md`](../docs/IMAGE_N4_DIAGNOSTICS.md)。

最小入口:

```bash
scripts/gpt-image-2-n4-diagnose.sh --mode single --jsonl-out /tmp/gpt-image-2-n4-single.jsonl
```

`--mode single` 验证单账号单会话 `runOnce(N=4)`；`--mode parallel` 验证正式 `Runner.Run(N=4)` 的每个 part 与最终 merge。输出行以 `GPT2API_IMAGE_N4_DIAGNOSTIC_JSON=` 开头，可直接 `jq` 解析。

### 与 CI 配合

GitHub Actions 示例骨架:

```yaml
- name: docker compose up
  run: docker compose -f deploy/docker-compose.yml up -d --wait

- name: wait backend
  run: curl --retry 30 --retry-delay 2 --retry-connrefused http://localhost:8080/healthz

- name: smoke
  run: node scripts/smoke.mjs --base http://localhost:8080
```
