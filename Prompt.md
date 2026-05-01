# Prompt

## 当前目标

- 维护当前号池 `gpt2api` 的图片生成链路，重点是 `gpt-image-2` 的稳定路由、可观测性、下游计费对齐和线上排障效率。
- 遇到图片任务、下游扣费、前端展示或跨机器问题时，先按事实分层定位，再做最小可靠修复。

## 当前业务约定

- `gpt-image-2` 请求的 `resolution` 规范化为 `1k` / `2k` / `4k`。
- 缺少或无法识别 `resolution` 的旧请求，号池默认按 `1k` 处理并回显实际采用的档位。
- `1k`：号池策略为跳过外置图片渠道，只走 strict `free runner`。
- `2k` / `4k`：号池策略为优先外置渠道 `Codex -> APIMart`；外置渠道遇到可重试基础设施/瞬态错误时，允许回落 strict `free runner`。
- `2k` / `4k` 的 free runner 只作为完成率兜底，不承诺稳定原生大图能力；原生大图预期主要来自 Codex/APIMart 外置渠道。
- 用户侧价格真相源在下游后端 `new-api`，不在号池内部 `image_tasks.credit_cost`。
- 当前下游默认额度价按“单张图”计算：`1k=0`、`2k=0.06`、`4k=0.12` ⭐；总价按 `n` 放大，`n` 最大按 `4` 处理。
- 下游任务创建时应固化 `billing_context`，包括 `resolution`、单张 `model_price`、`image_count` 和 `pricing_version`；结算/退款按下单快照，不按任务结束时的新价格重算。

## 涉及范围

- 当前号池 `gpt2api`：
  - 图片任务创建、轮询、代理取图、账号调度、外置渠道 fallback。
  - `provider_trace`、timing、错误层级、账号状态回写。
  - 容器部署与 `deploy/bin/gpt2api` 预编译产物。
- 下游后端 `new-api`：
  - 用户预扣、任务快照、退款、`/api/pricing`。
  - 下游任务 ID 与号池 `img_*` 的映射。
- 下游前端 `new-api-web`：
  - 分辨率和张数选择。
  - 价格展示、任务轮询、作品库保存、失败提示。

## 成功标准

- 前端展示价格、下游实际扣费、号池 `provider_trace.resolution` 三者一致。
- `1k` 请求最终 trace 能看到外置渠道按 `resolution_runner_only` 跳过并使用 free runner。
- `2k` / `4k` 请求优先命中 `codex-cli-proxy-image`，必要时可经 APIMart 或 free runner 兜底，且错误层级可从任务详情判断。
- 多图任务结果以 `result.data[]` 为准，`n`、结果张数和下游计费数量一致。
- `/p/img` 签名 URL 在服务使用同一 `JWT_SECRET` 重启后仍可访问；签名过期或更换密钥后失效属于预期。
