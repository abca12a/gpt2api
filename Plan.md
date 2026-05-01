# Plan

## 当前维护计划

### 1. 先分层定位

- 前端问题先确认是展示/保存/轮询问题，还是下游任务真实失败。
- 下游问题先查 `new-api.tasks`、`logs.other`、`private_data.upstream_task_id` 和 `billing_context`。
- 号池问题再查 `image_tasks`、`provider_trace`、容器日志、外置 channel 状态和账号状态。
- 外部上游问题最后再看 `cli-proxy-api`、APIMart、ChatGPT Web Runner 和代理网络。

### 2. gpt-image-2 路由验收

- `1k`：请求缺省或显式 `1k` 时，号池应跳过 Codex/APIMart，只用 strict free runner。
- `2k`：优先 `codex-cli-proxy-image`，Codex 不可用或瞬态失败时尝试 APIMart，再按策略 free runner 兜底。
- `4k`：同 `2k`，但重参考图或大像素任务要保留更长外置等待窗口。
- 所有任务查询响应都应回显规范化 `resolution` 和 `provider_trace_summary`。

### 3. 下游计费验收

- 当前默认单价：`1k=0`、`2k=0.06`、`4k=0.12` ⭐。
- 总价必须按 `n` 放大，`n <= 0` 按 `1`，`n > 4` 按 `4`。
- 下游 `billing_context.model_price` 表示单张图单价，`billing_context.image_count` 表示本次计费张数。
- 旧单复核时要按订单创建时价格判断，不能拿当前 `/api/pricing` 直接判老单错误。

### 4. 常用验证

- 号池单测：`go test ./internal/image ./internal/upstream/chatgpt ./internal/gateway ./internal/scheduler -count=1`
- 号池部署前构建：`bash deploy/build-local.sh`
- 容器更新：`docker compose -f deploy/docker-compose.yml build server && docker compose -f deploy/docker-compose.yml up -d server`
- 三档真单联调：`cd scripts && npm run gpt-image-2:e2e -- --resolutions 1k,2k,4k`

### 5. 文档维护

- 长期规则和机器拓扑只写 `AGENTS.md`。
- 当前事实、排查入口、最近仍有复用价值的变更写 `Documentation.md`。
- 纠正过的误解和不要再犯的问题写 `Corrections.md`。
- 面向调用方的接口写 `docs/API_MANUAL.md`；面向下游系统协作写 `docs/DOWNSTREAM_INTEGRATION.md`。

## 当前风险点

- 当前工作区可能存在未提交的实现草稿；部署或构建前必须确认不会混入无关改动。
- 本项目 Dockerfile 不会在镜像内重新 `go build`；只 `docker compose build/up` 会复制已有 `deploy/bin/gpt2api`。
- `free runner` 多图生产依赖并发拆单策略；不要承诺单个 free 账号单次会话稳定一次性出满 4 张。
- 号池内部 `models.image_price_per_call` 仍可能显示固定成本，这不是下游用户实际扣费依据。
