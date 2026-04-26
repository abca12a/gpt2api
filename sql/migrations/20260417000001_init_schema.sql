-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 用户分组
-- ============================================================
CREATE TABLE IF NOT EXISTS `user_groups` (
    `id`                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `name`               VARCHAR(64)     NOT NULL,
    `ratio`              DECIMAL(6,4)    NOT NULL DEFAULT 1.0000 COMMENT '分组倍率,1.0 默认,VIP=0.8',
    `daily_limit_credits` BIGINT         NOT NULL DEFAULT 0      COMMENT '0 表示不限',
    `rpm_limit`          INT             NOT NULL DEFAULT 60,
    `tpm_limit`          BIGINT          NOT NULL DEFAULT 60000,
    `remark`             VARCHAR(255)    NOT NULL DEFAULT '',
    `created_at`         DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at`         DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    `deleted_at`         DATETIME        NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='用户分组';

INSERT INTO `user_groups` (`name`, `ratio`, `rpm_limit`, `tpm_limit`, `remark`) VALUES
    ('default', 1.0000,  60,  60000, '默认分组'),
    ('vip',     0.8000, 300, 300000, 'VIP 分组,倍率 0.8'),
    ('svip',    0.6000, 600, 600000, 'SVIP 分组,倍率 0.6')
ON DUPLICATE KEY UPDATE `name` = VALUES(`name`);

-- ============================================================
-- 用户
-- ============================================================
CREATE TABLE IF NOT EXISTS `users` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `email`           VARCHAR(128)    NOT NULL,
    `password_hash`   VARCHAR(128)    NOT NULL,
    `nickname`        VARCHAR(64)     NOT NULL DEFAULT '',
    `group_id`        BIGINT UNSIGNED NOT NULL DEFAULT 1,
    `role`            VARCHAR(16)     NOT NULL DEFAULT 'user'  COMMENT 'user | admin',
    `status`          VARCHAR(16)     NOT NULL DEFAULT 'active' COMMENT 'active | banned',
    `credit_balance`  BIGINT          NOT NULL DEFAULT 0        COMMENT '积分余额(单位:厘)',
    `credit_frozen`   BIGINT          NOT NULL DEFAULT 0        COMMENT '冻结积分',
    `version`         BIGINT UNSIGNED NOT NULL DEFAULT 0        COMMENT '乐观锁版本号',
    `last_login_at`   DATETIME        NULL,
    `last_login_ip`   VARCHAR(64)     NOT NULL DEFAULT '',
    `created_at`      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at`      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    `deleted_at`      DATETIME        NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_email` (`email`),
    KEY `idx_group` (`group_id`),
    KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='用户';

-- ============================================================
-- 下游 API KEY
-- ============================================================
CREATE TABLE IF NOT EXISTS `api_keys` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `user_id`         BIGINT UNSIGNED NOT NULL,
    `name`            VARCHAR(64)     NOT NULL DEFAULT '',
    `key_prefix`      VARCHAR(16)     NOT NULL COMMENT '前 8 位明文,用于展示/检索',
    `key_hash`        VARCHAR(128)    NOT NULL COMMENT '完整 KEY 的 SHA-256',
    `quota_limit`     BIGINT          NOT NULL DEFAULT 0 COMMENT '额度上限(厘),0=不限',
    `quota_used`      BIGINT          NOT NULL DEFAULT 0,
    `allowed_models`  JSON            NULL COMMENT 'null=全部允许,否则白名单',
    `allowed_ips`     JSON            NULL,
    `rpm`             INT             NOT NULL DEFAULT 0 COMMENT '0=继承分组',
    `tpm`             BIGINT          NOT NULL DEFAULT 0,
    `expires_at`      DATETIME        NULL,
    `enabled`         TINYINT(1)      NOT NULL DEFAULT 1,
    `last_used_at`    DATETIME        NULL,
    `last_used_ip`    VARCHAR(64)     NOT NULL DEFAULT '',
    `created_at`      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at`      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    `deleted_at`      DATETIME        NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_key_hash` (`key_hash`),
    KEY `idx_user` (`user_id`),
    KEY `idx_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='下游 API KEY';

-- ============================================================
-- 积分流水
-- ============================================================
CREATE TABLE IF NOT EXISTS `credit_transactions` (
    `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `user_id`        BIGINT UNSIGNED NOT NULL,
    `key_id`         BIGINT UNSIGNED NOT NULL DEFAULT 0,
    `type`           VARCHAR(32)     NOT NULL COMMENT 'consume | recharge | refund | redeem | admin_adjust | freeze | unfreeze',
    `amount`         BIGINT          NOT NULL COMMENT '正值增加,负值减少',
    `balance_after`  BIGINT          NOT NULL,
    `ref_id`         VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '关联业务单号(订单号/任务ID/请求ID)',
    `remark`         VARCHAR(255)    NOT NULL DEFAULT '',
    `created_at`     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    KEY `idx_user_created` (`user_id`, `created_at`),
    KEY `idx_key` (`key_id`),
    KEY `idx_type` (`type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='积分流水';

-- ============================================================
-- 代理池
-- ============================================================
CREATE TABLE IF NOT EXISTS `proxies` (
    `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `scheme`         VARCHAR(16)     NOT NULL DEFAULT 'http' COMMENT 'http | https | socks5',
    `host`           VARCHAR(128)    NOT NULL,
    `port`           INT             NOT NULL,
    `username`       VARCHAR(128)    NOT NULL DEFAULT '',
    `password_enc`   VARCHAR(512)    NOT NULL DEFAULT '' COMMENT '加密存储',
    `country`        VARCHAR(8)      NOT NULL DEFAULT '',
    `isp`            VARCHAR(64)     NOT NULL DEFAULT '',
    `health_score`   INT             NOT NULL DEFAULT 100 COMMENT '0-100',
    `last_probe_at`  DATETIME        NULL,
    `last_error`     VARCHAR(255)    NOT NULL DEFAULT '',
    `enabled`        TINYINT(1)      NOT NULL DEFAULT 1,
    `remark`         VARCHAR(255)    NOT NULL DEFAULT '',
    `created_at`     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at`     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    `deleted_at`     DATETIME        NULL,
    PRIMARY KEY (`id`),
    KEY `idx_enabled_health` (`enabled`, `health_score`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='代理池';

-- ============================================================
-- chatgpt.com 账号池
-- ============================================================
CREATE TABLE IF NOT EXISTS `oai_accounts` (
    `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `email`               VARCHAR(128)    NOT NULL,
    `auth_token_enc`      TEXT            NOT NULL COMMENT 'AES-256-GCM 加密的 AT',
    `refresh_token_enc`   TEXT            NULL,
    `token_expires_at`    DATETIME        NULL,
    `oai_session_id`      VARCHAR(128)    NOT NULL DEFAULT '',
    `oai_device_id`       VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '首次写死,不要换',
    `plan_type`           VARCHAR(32)     NOT NULL DEFAULT 'plus' COMMENT 'free | plus | pro | team | enterprise',
    `daily_image_quota`   INT             NOT NULL DEFAULT 100,
    `status`              VARCHAR(16)     NOT NULL DEFAULT 'healthy' COMMENT 'healthy | warned | throttled | suspicious | dead',
    `warned_at`           DATETIME        NULL,
    `cooldown_until`      DATETIME        NULL,
    `last_used_at`        DATETIME        NULL,
    `today_used_count`    INT             NOT NULL DEFAULT 0,
    `today_used_date`     DATE            NULL,
    `notes`               VARCHAR(500)    NOT NULL DEFAULT '',
    `created_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    `deleted_at`          DATETIME        NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_email` (`email`),
    KEY `idx_status_last_used` (`status`, `last_used_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='chatgpt.com 账号池';

-- ============================================================
-- 账号 cookies
-- ============================================================
CREATE TABLE IF NOT EXISTS `oai_account_cookies` (
    `account_id`     BIGINT UNSIGNED NOT NULL,
    `cookie_json_enc` TEXT           NOT NULL,
    `updated_at`     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`account_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='账号 cookies(加密)';

-- ============================================================
-- 账号-代理绑定(一号一出口)
-- ============================================================
CREATE TABLE IF NOT EXISTS `account_proxy_bindings` (
    `account_id`    BIGINT UNSIGNED NOT NULL,
    `proxy_id`      BIGINT UNSIGNED NOT NULL,
    `bound_at`      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (`account_id`),
    KEY `idx_proxy` (`proxy_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='账号-代理绑定';

-- ============================================================
-- 账号额度快照
-- ============================================================
CREATE TABLE IF NOT EXISTS `account_quota_snapshots` (
    `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `account_id`     BIGINT UNSIGNED NOT NULL,
    `feature_name`   VARCHAR(64)     NOT NULL,
    `remaining`      INT             NOT NULL DEFAULT 0,
    `reset_after`    DATETIME        NULL,
    `snapshot_at`    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    KEY `idx_account_feature_time` (`account_id`, `feature_name`, `snapshot_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='账号额度快照';

-- ============================================================
-- 模型配置
-- ============================================================
CREATE TABLE IF NOT EXISTS `models` (
    `id`                      BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `slug`                    VARCHAR(64)     NOT NULL COMMENT '下游暴露名,例 gpt-5.1',
    `type`                    VARCHAR(16)     NOT NULL COMMENT 'chat | image',
    `upstream_model_slug`     VARCHAR(64)     NOT NULL COMMENT '上游 chatgpt.com 实际 slug',
    `input_price_per_1m`      BIGINT          NOT NULL DEFAULT 0 COMMENT '每百万 token 积分价(厘)',
    `output_price_per_1m`     BIGINT          NOT NULL DEFAULT 0,
    `cache_read_price_per_1m` BIGINT          NOT NULL DEFAULT 0,
    `image_price_per_call`    BIGINT          NOT NULL DEFAULT 0 COMMENT '每次生图积分价(厘)',
    `description`             VARCHAR(255)    NOT NULL DEFAULT '',
    `enabled`                 TINYINT(1)      NOT NULL DEFAULT 1,
    `created_at`              DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at`              DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    `deleted_at`              DATETIME        NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_slug` (`slug`),
    KEY `idx_type` (`type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='模型配置';

INSERT INTO `models` (`slug`, `type`, `upstream_model_slug`, `input_price_per_1m`, `output_price_per_1m`, `image_price_per_call`, `description`) VALUES
    ('gpt-5',            'chat',  'gpt-5',            25000,  75000, 0, 'GPT-5 主力模型'),
    ('gpt-5-mini',       'chat',  'gpt-5-mini',        5000,  15000, 0, 'GPT-5 轻量'),
    ('gpt-5-codex-max',  'chat',  'gpt-5-codex-max',  50000, 150000, 0, '代码专用'),
    ('gpt-image-1',      'image', 'auto',                 0,      0, 500000, '生图(每张 50 积分=5角)')
ON DUPLICATE KEY UPDATE `slug` = VALUES(`slug`);

-- ============================================================
-- 分组-模型倍率覆盖
-- ============================================================
CREATE TABLE IF NOT EXISTS `billing_ratios` (
    `id`        BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `model_id`  BIGINT UNSIGNED NOT NULL,
    `group_id`  BIGINT UNSIGNED NOT NULL,
    `ratio`     DECIMAL(6,4)    NOT NULL DEFAULT 1.0000,
    `created_at` DATETIME       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at` DATETIME       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_model_group` (`model_id`, `group_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='模型分组倍率';

-- ============================================================
-- 使用日志(当月;后续按月分表)
-- ============================================================
CREATE TABLE IF NOT EXISTS `usage_logs` (
    `id`                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `user_id`              BIGINT UNSIGNED NOT NULL,
    `key_id`               BIGINT UNSIGNED NOT NULL DEFAULT 0,
    `model_id`             BIGINT UNSIGNED NOT NULL,
    `account_id`           BIGINT UNSIGNED NOT NULL DEFAULT 0,
    `request_id`           VARCHAR(64)     NOT NULL,
    `type`                 VARCHAR(16)     NOT NULL COMMENT 'chat | image',
    `input_tokens`         INT             NOT NULL DEFAULT 0,
    `output_tokens`        INT             NOT NULL DEFAULT 0,
    `cache_read_tokens`    INT             NOT NULL DEFAULT 0,
    `cache_write_tokens`   INT             NOT NULL DEFAULT 0,
    `image_count`          INT             NOT NULL DEFAULT 0,
    `credit_cost`          BIGINT          NOT NULL DEFAULT 0,
    `duration_ms`          INT             NOT NULL DEFAULT 0,
    `status`               VARCHAR(16)     NOT NULL COMMENT 'success | failed',
    `error_code`           VARCHAR(64)     NOT NULL DEFAULT '',
    `ip`                   VARCHAR(64)     NOT NULL DEFAULT '',
    `ua`                   VARCHAR(255)    NOT NULL DEFAULT '',
    `created_at`           DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    KEY `idx_user_time` (`user_id`, `created_at`),
    KEY `idx_key_time` (`key_id`, `created_at`),
    KEY `idx_model_time` (`model_id`, `created_at`),
    KEY `idx_account_time` (`account_id`, `created_at`),
    KEY `idx_request_id` (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='使用日志';

-- ============================================================
-- 异步生图任务
-- ============================================================
CREATE TABLE IF NOT EXISTS `image_tasks` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `task_id`         VARCHAR(64)     NOT NULL,
    `user_id`         BIGINT UNSIGNED NOT NULL,
    `key_id`          BIGINT UNSIGNED NOT NULL DEFAULT 0,
    `model_id`        BIGINT UNSIGNED NOT NULL,
    `account_id`      BIGINT UNSIGNED NOT NULL DEFAULT 0,
    `downstream_user_id`    VARCHAR(64)  NOT NULL DEFAULT '',
    `downstream_username`   VARCHAR(128) NOT NULL DEFAULT '',
    `downstream_user_email` VARCHAR(128) NOT NULL DEFAULT '',
    `downstream_user_label` VARCHAR(255) NOT NULL DEFAULT '',
    `prompt`          TEXT            NOT NULL,
    `n`               INT             NOT NULL DEFAULT 1,
    `size`            VARCHAR(32)     NOT NULL DEFAULT '1024x1024',
    `status`          VARCHAR(16)     NOT NULL DEFAULT 'queued' COMMENT 'queued | dispatched | running | success | failed',
    `conversation_id` VARCHAR(64)     NOT NULL DEFAULT '',
    `file_ids`        JSON            NULL,
    `result_urls`     JSON            NULL,
    `error`           VARCHAR(500)    NOT NULL DEFAULT '',
    `estimated_credit` BIGINT         NOT NULL DEFAULT 0,
    `credit_cost`     BIGINT          NOT NULL DEFAULT 0,
    `created_at`      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `started_at`      DATETIME        NULL,
    `finished_at`     DATETIME        NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_task_id` (`task_id`),
    KEY `idx_user_time` (`user_id`, `created_at`),
    KEY `idx_downstream_user` (`downstream_user_id`),
    KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='异步生图任务';

-- ============================================================
-- 充值订单
-- ============================================================
CREATE TABLE IF NOT EXISTS `recharge_orders` (
    `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `order_no`       VARCHAR(64)     NOT NULL,
    `user_id`        BIGINT UNSIGNED NOT NULL,
    `amount`         BIGINT          NOT NULL COMMENT 'RMB,单位:分',
    `credits`        BIGINT          NOT NULL COMMENT '到账积分(厘)',
    `channel`        VARCHAR(16)     NOT NULL COMMENT 'wechat | alipay | manual',
    `epay_trade_no`  VARCHAR(64)     NOT NULL DEFAULT '',
    `status`         VARCHAR(16)     NOT NULL DEFAULT 'pending' COMMENT 'pending | paid | cancelled | refunded',
    `paid_at`        DATETIME        NULL,
    `callback_raw`   TEXT            NULL,
    `created_at`     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at`     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_order_no` (`order_no`),
    KEY `idx_user_time` (`user_id`, `created_at`),
    KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='充值订单';

-- ============================================================
-- 兑换码
-- ============================================================
CREATE TABLE IF NOT EXISTS `redeem_codes` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `code`            VARCHAR(64)     NOT NULL,
    `batch_id`        VARCHAR(32)     NOT NULL DEFAULT '',
    `credits`         BIGINT          NOT NULL,
    `used_by_user_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,
    `used_at`         DATETIME        NULL,
    `expires_at`      DATETIME        NULL,
    `created_at`      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_code` (`code`),
    KEY `idx_batch` (`batch_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='兑换码';

-- ============================================================
-- 系统配置(KV 热更新)
-- ============================================================
CREATE TABLE IF NOT EXISTS `system_configs` (
    `config_key`   VARCHAR(128) NOT NULL,
    `config_value` TEXT         NOT NULL,
    `remark`       VARCHAR(255) NOT NULL DEFAULT '',
    `updated_at`   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`config_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='系统配置';

-- ============================================================
-- 公告
-- ============================================================
CREATE TABLE IF NOT EXISTS `announcements` (
    `id`        BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `title`     VARCHAR(128)    NOT NULL,
    `content`   TEXT            NOT NULL,
    `level`     VARCHAR(16)     NOT NULL DEFAULT 'info' COMMENT 'info | warn | danger',
    `enabled`   TINYINT(1)      NOT NULL DEFAULT 1,
    `start_at`  DATETIME        NULL,
    `end_at`    DATETIME        NULL,
    `created_at` DATETIME       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at` DATETIME       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    KEY `idx_enabled_time` (`enabled`, `start_at`, `end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='公告';

-- ============================================================
-- 审计日志
-- ============================================================
CREATE TABLE IF NOT EXISTS `audit_logs` (
    `id`        BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `admin_id`  BIGINT UNSIGNED NOT NULL,
    `action`    VARCHAR(64)     NOT NULL,
    `target`    VARCHAR(128)    NOT NULL DEFAULT '',
    `diff`      JSON            NULL,
    `ip`        VARCHAR(64)     NOT NULL DEFAULT '',
    `created_at` DATETIME       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    KEY `idx_admin_time` (`admin_id`, `created_at`),
    KEY `idx_action` (`action`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='审计日志';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS `audit_logs`;
DROP TABLE IF EXISTS `announcements`;
DROP TABLE IF EXISTS `system_configs`;
DROP TABLE IF EXISTS `redeem_codes`;
DROP TABLE IF EXISTS `recharge_orders`;
DROP TABLE IF EXISTS `image_tasks`;
DROP TABLE IF EXISTS `usage_logs`;
DROP TABLE IF EXISTS `billing_ratios`;
DROP TABLE IF EXISTS `models`;
DROP TABLE IF EXISTS `account_quota_snapshots`;
DROP TABLE IF EXISTS `account_proxy_bindings`;
DROP TABLE IF EXISTS `oai_account_cookies`;
DROP TABLE IF EXISTS `oai_accounts`;
DROP TABLE IF EXISTS `proxies`;
DROP TABLE IF EXISTS `credit_transactions`;
DROP TABLE IF EXISTS `api_keys`;
DROP TABLE IF EXISTS `users`;
DROP TABLE IF EXISTS `user_groups`;
-- +goose StatementEnd
