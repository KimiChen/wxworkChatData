-- 企业微信会话存档数据库建表 SQL (参考用，程序会通过 GORM AutoMigrate 自动建表)
-- 使用前请先创建数据库:
-- CREATE DATABASE IF NOT EXISTS wework_archive CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
--
-- 注意：实际表名由 storage_filewords 配置决定，格式为 corp_{filewords}_xxx
-- 例如 storage_filewords: "YYYY-MM-DD-hh" 时，表名为：
--   corp_2026-04-11-03_messages
--   corp_2026-04-11-03_seq_cursors
--   corp_2026-04-11-03_media_tasks

-- 消息表：存储所有类型的解密后消息（示例使用 corp_ 前缀）
CREATE TABLE IF NOT EXISTS `corp_messages` (
    `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `corp_name`     VARCHAR(64)     NOT NULL COMMENT '企业标识名',
    `msg_id`        VARCHAR(128)    NOT NULL COMMENT '消息唯一ID',
    `seq`           BIGINT UNSIGNED NOT NULL COMMENT '消息序号，用于增量拉取',
    `action`        VARCHAR(16)     NOT NULL DEFAULT 'send' COMMENT '消息动作: send/recall/switch',
    `from_user`     VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '发送方ID',
    `to_list`       JSON            NULL     COMMENT '接收方列表',
    `room_id`       VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '群聊ID，单聊为空',
    `msg_type`      VARCHAR(32)     NOT NULL DEFAULT '' COMMENT '消息类型: text/image/voice/video/file/...',
    `msg_time`      BIGINT          NOT NULL DEFAULT 0  COMMENT '消息时间戳(毫秒,UTC)',
    `content`       JSON            NULL     COMMENT '完整解密后消息JSON',
    `publickey_ver` INT UNSIGNED    NOT NULL DEFAULT 0  COMMENT 'RSA公钥版本号',
    `created_at`    DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_corp_msgid` (`corp_name`, `msg_id`),
    KEY `idx_corp_seq` (`corp_name`, `seq`),
    KEY `idx_msg_time` (`msg_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='企业微信会话存档消息';

-- 拉取游标表：记录每个企业的消息拉取进度
CREATE TABLE IF NOT EXISTS `corp_seq_cursors` (
    `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `corp_name`  VARCHAR(64)     NOT NULL COMMENT '企业标识名',
    `last_seq`   BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '最后成功处理的seq',
    `updated_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_corp` (`corp_name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='每个企业的消息拉取游标';

-- 媒体下载任务表：记录媒体文件的下载状态
CREATE TABLE IF NOT EXISTS `corp_media_tasks` (
    `id`          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `corp_name`   VARCHAR(64)     NOT NULL COMMENT '企业标识名',
    `msg_id`      VARCHAR(128)    NOT NULL COMMENT '消息ID',
    `msg_type`    VARCHAR(32)     NOT NULL COMMENT '消息类型',
    `sdk_file_id` TEXT            NOT NULL COMMENT 'SDK媒体文件ID',
    `md5sum`      VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '预期MD5校验值',
    `file_size`   BIGINT UNSIGNED NOT NULL DEFAULT 0  COMMENT '文件大小',
    `file_ext`    VARCHAR(16)     NOT NULL DEFAULT '' COMMENT '文件扩展名',
    `file_path`   VARCHAR(512)    NOT NULL DEFAULT '' COMMENT '下载后本地路径',
    `status`      TINYINT         NOT NULL DEFAULT 0  COMMENT '状态: 0=待下载 1=下载中 2=完成 3=失败',
    `retry_count` INT             NOT NULL DEFAULT 0  COMMENT '重试次数',
    `error_msg`   VARCHAR(512)    NOT NULL DEFAULT '' COMMENT '错误信息',
    `msg_time`    BIGINT          NOT NULL DEFAULT 0  COMMENT '消息时间戳(毫秒)',
    `created_at`  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    `updated_at`  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_corp_msg_type` (`corp_name`, `msg_id`, `msg_type`),
    KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='媒体文件下载任务队列';
