-- +goose Up
-- +goose StatementBegin
ALTER TABLE `image_tasks`
    ADD COLUMN `downstream_user_id`    VARCHAR(64)  NOT NULL DEFAULT '' AFTER `account_id`,
    ADD COLUMN `downstream_username`   VARCHAR(128) NOT NULL DEFAULT '' AFTER `downstream_user_id`,
    ADD COLUMN `downstream_user_email` VARCHAR(128) NOT NULL DEFAULT '' AFTER `downstream_username`,
    ADD COLUMN `downstream_user_label` VARCHAR(255) NOT NULL DEFAULT '' AFTER `downstream_user_email`,
    ADD KEY `idx_downstream_user` (`downstream_user_id`);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE `image_tasks`
    DROP KEY `idx_downstream_user`,
    DROP COLUMN `downstream_user_label`,
    DROP COLUMN `downstream_user_email`,
    DROP COLUMN `downstream_username`,
    DROP COLUMN `downstream_user_id`;
-- +goose StatementEnd
