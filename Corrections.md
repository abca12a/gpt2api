# Corrections

## 出图快速换号

- 2026-04-25 修正：不能因为 SSE 已结束且缺少 `image_gen_task_id`、缺少图片引用就立即判定失败；生产任务 `img_5cf852f2b9724e1daeb9dabd` 因此 22 秒内三次换号后失败。
- 正确做法：这种情况只能说明上游可能未真正受理生图任务，应先做短 Poll（当前 20 秒）给 conversation mapping 一个补出 tool/image 消息的机会；短 Poll 仍无图时再暂停该账号并换号。
- 边界：已有 `image_gen_task_id` 或已有任意 file/sediment 引用时，继续使用常规 Poll 窗口，不走短 Poll。
