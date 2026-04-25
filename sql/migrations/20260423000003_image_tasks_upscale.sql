-- +goose Up
-- +goose StatementBegin
--
-- 面板新增「4K 出图」能力。
--
-- 上游 chatgpt.com 生图原生只有 1024×1024 / 1792×1024 / 1024×1792 三档,
-- 这里的 upscale 字段记录"拿到原图后希望由本服务做哪档超分"。
-- 当前实现会在图片代理首次访问时调用阿里云生成式图像超分;
-- 早期版本曾使用本地 Catmull-Rom 插值,字段语义保持兼容。
--
-- 值:
--   ''   原图直出(默认)
--   '2k' 长边 2560 PNG
--   '4k' 长边 3840 PNG
--
-- 放大执行时机:/v1/images/proxy/:task_id/:idx 首次被请求时,对单张图做
-- 阿里云超分并放进进程内 LRU 缓存(默认 512MB),之后
-- 同一条代理 URL 的请求毫秒级命中,不会重复计算。
ALTER TABLE `image_tasks`
    ADD COLUMN `upscale` VARCHAR(8) NOT NULL DEFAULT '' AFTER `size`;
-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin
ALTER TABLE `image_tasks` DROP COLUMN `upscale`;
-- +goose StatementEnd
