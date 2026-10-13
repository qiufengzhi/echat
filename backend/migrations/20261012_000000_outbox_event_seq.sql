-- 事件业务序号：读侧版本守卫（LWW by seq）的坐标
-- 写侧在记事件事务内按插入顺序从全局单调序列取号，随行落库并由 relay 原样透传进 JetStream 信封
-- 投影据此拒绝「较旧事件晚到」：至少一次投递下，陈旧事件重投不得把已重新加入的成员误删、
-- 不得把已关闭的房间复活
-- 取舍：用全局序列而非 occurred_at 墙钟（跨节点不单调、NTP 回拨翻转）、非流序号（按发布序分配，
-- 旧事件重投反而拿到更大序号，检测不到业务序倒挂）

CREATE SEQUENCE public.event_seq_seq;
ALTER TABLE public.outbox_events ADD COLUMN seq bigint NOT NULL DEFAULT 0;
ALTER TABLE public.outbox_events ALTER COLUMN seq SET DEFAULT nextval('event_seq_seq');