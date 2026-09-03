-- eChat schema 全量基线（由 ent schema 在空库上建立的权威结构导出）
-- 管理者：Atlas 版本化迁移（启动时经 store.Migrate apply，日记表 atlas_schema_migrations）
-- 注意：enum 状态在 ent 层校验（varchar 存储），DDL 不追加 CHECK，保持与 ORM 层一致

CREATE TABLE public.auth_tokens (
    id uuid NOT NULL,
    purpose character varying NOT NULL,
    token_hash character varying NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    user_id uuid NOT NULL,
    target character varying
);

CREATE TABLE public.identities (
    id uuid NOT NULL,
    provider character varying NOT NULL,
    provider_uid character varying NOT NULL,
    metadata jsonb,
    created_at timestamp with time zone NOT NULL,
    user_id uuid NOT NULL
);

CREATE TABLE public.outbox_events (
    id uuid NOT NULL,
    event_type character varying NOT NULL,
    aggregate_id uuid NOT NULL,
    subject character varying NOT NULL,
    payload jsonb,
    status character varying DEFAULT 'pending'::character varying NOT NULL,
    created_at timestamp with time zone NOT NULL,
    published_at timestamp with time zone,
    attempts bigint DEFAULT 0 NOT NULL,
    version bigint DEFAULT 0 NOT NULL
);

CREATE TABLE public.room_members (
    id uuid NOT NULL,
    username character varying NOT NULL,
    joined_at timestamp with time zone NOT NULL,
    left_at timestamp with time zone,
    room_id uuid NOT NULL,
    user_id uuid NOT NULL
);

CREATE TABLE public.rooms (
    id uuid NOT NULL,
    room_code character varying NOT NULL,
    status character varying DEFAULT 'active'::character varying NOT NULL,
    closed_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    host_id uuid NOT NULL
);

CREATE TABLE public.sessions (
    id uuid NOT NULL,
    refresh_token_hash character varying NOT NULL,
    token_version bigint NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    ip character varying NOT NULL,
    user_agent character varying,
    device_fingerprint character varying,
    created_at timestamp with time zone NOT NULL,
    user_id uuid NOT NULL
);

CREATE TABLE public.users (
    id uuid NOT NULL,
    email character varying,
    username character varying NOT NULL,
    display_name character varying NOT NULL,
    avatar_url character varying,
    status character varying DEFAULT 'pending'::character varying NOT NULL,
    token_version bigint DEFAULT 0 NOT NULL,
    password_hash character varying NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone
);

ALTER TABLE ONLY public.auth_tokens
    ADD CONSTRAINT auth_tokens_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.identities
    ADD CONSTRAINT identities_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.outbox_events
    ADD CONSTRAINT outbox_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.room_members
    ADD CONSTRAINT room_members_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.rooms
    ADD CONSTRAINT rooms_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

CREATE INDEX authtoken_purpose_consumed_at_user_id ON public.auth_tokens USING btree (purpose, consumed_at, user_id);
CREATE UNIQUE INDEX identity_provider_provider_uid ON public.identities USING btree (provider, provider_uid);
CREATE UNIQUE INDEX identity_provider_user_id ON public.identities USING btree (provider, user_id);
CREATE INDEX outboxevent_status_created_at ON public.outbox_events USING btree (status, created_at);
CREATE UNIQUE INDEX roommember_room_id_user_id ON public.room_members USING btree (room_id, user_id);
CREATE UNIQUE INDEX rooms_room_code_key ON public.rooms USING btree (room_code);
CREATE INDEX session_revoked_at_user_id ON public.sessions USING btree (revoked_at, user_id);
CREATE UNIQUE INDEX sessions_refresh_token_hash_key ON public.sessions USING btree (refresh_token_hash);
CREATE UNIQUE INDEX user_email ON public.users USING btree (email) WHERE (email IS NOT NULL);
CREATE UNIQUE INDEX users_username_key ON public.users USING btree (username);

ALTER TABLE ONLY public.auth_tokens
    ADD CONSTRAINT auth_tokens_users_auth_tokens FOREIGN KEY (user_id) REFERENCES public.users(id);
ALTER TABLE ONLY public.identities
    ADD CONSTRAINT identities_users_identities FOREIGN KEY (user_id) REFERENCES public.users(id);
ALTER TABLE ONLY public.room_members
    ADD CONSTRAINT room_members_rooms_memberships FOREIGN KEY (room_id) REFERENCES public.rooms(id);
ALTER TABLE ONLY public.room_members
    ADD CONSTRAINT room_members_users_room_memberships FOREIGN KEY (user_id) REFERENCES public.users(id);
ALTER TABLE ONLY public.rooms
    ADD CONSTRAINT rooms_users_hosted_rooms FOREIGN KEY (host_id) REFERENCES public.users(id);
ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_users_sessions FOREIGN KEY (user_id) REFERENCES public.users(id);