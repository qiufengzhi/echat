-- 用户历史读模型的 LWW 版本守卫水位：随投影行记录最后一次应用的业务序号
-- 投影语句以 last_event_seq < $seq 为条件应用（新行插入即落水位），旧事件晚到不覆盖已更新的在场状态
-- 与 outbox_events.seq（20261012 迁移）同源：joined/left/host_transferred 的乱序重投据此拒绝

ALTER TABLE public.user_room_history ADD COLUMN last_event_seq bigint NOT NULL DEFAULT 0;