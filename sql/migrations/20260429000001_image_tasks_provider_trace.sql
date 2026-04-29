-- +goose Up
-- +goose StatementBegin
ALTER TABLE `image_tasks`
    ADD COLUMN `provider_trace` JSON NULL AFTER `result_urls`;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE `image_tasks`
    DROP COLUMN `provider_trace`;
-- +goose StatementEnd
