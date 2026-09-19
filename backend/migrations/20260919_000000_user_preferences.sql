-- 用户进入房间默认行为设置（进房默认开/关麦克风与扬声器）
-- 存为一个 jsonb 设置包，缺省即空对象：Go 侧反序列化缺键为 false（静音进房）
-- 加后续设置项只需扩展对象，无需再加列

ALTER TABLE public.users ADD COLUMN preferences jsonb NOT NULL DEFAULT '{}'::jsonb;