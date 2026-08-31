-- eChat 用户系统初始迁移（v2026-08-31）
--
-- 来源：backend/ent/schema/*.go（users / identities / sessions / auth_tokens）
-- 策略：开发期可用 store.Migrate() 按 Ent Schema 自动迁移；
--       生产环境以本目录版本化文件为准，由 atlas migrate apply 执行
-- 约定：enum 在 ent 层校验（varchar 存储），故此处不追加 CHECK 约束，保持与迁移工具产出一致

CREATE TABLE users (
    id            uuid                   NOT NULL,
    email         character varying,
    username      character varying      NOT NULL,
    display_name  character varying      NOT NULL,
    avatar_url    character varying,
    status        character varying      NOT NULL DEFAULT 'pending',
    token_version bigint                 NOT NULL DEFAULT 0,
    password_hash character varying      NOT NULL,
    created_at    timestamp with time zone NOT NULL,
    updated_at    timestamp with time zone NOT NULL,
    deleted_at    timestamp with time zone,
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX users_username_key ON users (username);
-- email 部分唯一索引：允许多个未绑定邮箱的本地账号共存
CREATE UNIQUE INDEX user_email ON users (email) WHERE email IS NOT NULL;

CREATE TABLE identities (
    id           uuid          NOT NULL,
    user_id      uuid          NOT NULL,
    provider     character varying NOT NULL,
    provider_uid character varying NOT NULL,
    metadata     jsonb,
    created_at   timestamp with time zone NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT identities_users_identities FOREIGN KEY (user_id) REFERENCES users (id)
);

-- 一个登录标识只对应一个用户
CREATE UNIQUE INDEX identity_provider_provider_uid ON identities (provider, provider_uid);
-- 一个用户在同一来源只绑一条凭据
CREATE UNIQUE INDEX identity_provider_user_id ON identities (provider, user_id);

CREATE TABLE sessions (
    id                 uuid                   NOT NULL,
    refresh_token_hash character varying      NOT NULL,
    token_version      bigint                 NOT NULL,
    expires_at         timestamp with time zone NOT NULL,
    revoked_at         timestamp with time zone,
    ip                 character varying      NOT NULL,
    user_agent         character varying,
    device_fingerprint character varying,
    created_at         timestamp with time zone NOT NULL,
    user_id            uuid                   NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT sessions_users_sessions FOREIGN KEY (user_id) REFERENCES users (id)
);

-- 重放检测：按哈希唯一命中已作废的 refresh
CREATE UNIQUE INDEX sessions_refresh_token_hash_key ON sessions (refresh_token_hash);
-- 支撑「查活会话 / 按用户吊销全部」
CREATE INDEX session_revoked_at_user_id ON sessions (revoked_at, user_id);

CREATE TABLE auth_tokens (
    id          uuid                   NOT NULL,
    user_id     uuid                   NOT NULL,
    purpose     character varying      NOT NULL,
    token_hash  character varying      NOT NULL,
    expires_at  timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at  timestamp with time zone NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT auth_tokens_users_auth_tokens FOREIGN KEY (user_id) REFERENCES users (id)
);

-- 支撑「查某用户某用途的未消费令牌」与「同用途防滥用」
CREATE INDEX authtoken_purpose_consumed_at_user_id ON auth_tokens (purpose, consumed_at, user_id);