-- 用户房间历史读模型表（投影目标，P6-1）
-- 事件源：room.joined / room.left / room.host_transferred，由 projection.UserHistoryProjector 幂等重建
-- (user_id, room_id) 唯一：一个用户在某房间只有一条在场历史，重复加入重置 joined_at 并清 left_at

CREATE TABLE public.user_room_history (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    room_id uuid NOT NULL,
    room_code character varying NOT NULL,
    joined_at timestamp with time zone NOT NULL,
    left_at timestamp with time zone,
    role character varying NOT NULL
);

ALTER TABLE ONLY public.user_room_history
    ADD CONSTRAINT user_room_history_pkey PRIMARY KEY (id);

-- upsert 键：同一用户同一房间至多一条在场记录
CREATE UNIQUE INDEX user_room_history_user_room_key ON public.user_room_history USING btree (user_id, room_id);

-- 「我的历史」查询加速：按用户取回其全部在场历史
CREATE INDEX user_room_history_user_id_idx ON public.user_room_history USING btree (user_id);

ALTER TABLE ONLY public.user_room_history
    ADD CONSTRAINT user_room_history_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

ALTER TABLE ONLY public.user_room_history
    ADD CONSTRAINT user_room_history_room_id_fkey FOREIGN KEY (room_id) REFERENCES public.rooms(id);
