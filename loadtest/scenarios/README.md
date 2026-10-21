# 压测场景参数说明

场景实现在 `internal/scenario/`（Go 代码即场景定义），本目录记录各场景的推荐参数与
负载 profile 取值依据（论证见 `docs/loadtest.md` §4/§5）。

## smoke（链路冒烟）

| 参数 | 值 | 说明 |
|---|---|---|
| users | 1 | 单会话串行 |
| ping-count | 10 | 心跳 RTT 基线 |

运行方式与分工见 `loadtest/README.md`。

## conn（连接层风暴）

| 参数 | 推荐值 | 依据 |
|---|---|---|
| users | 100 / 500 / 1000 / 2000… 阶梯 | 逐级找劣化拐点，单级不跳倍 |
| ramp | 30-90s | 建连铺开速率，避免 ramp 本身成为压力 |
| hold | 5m | 稳态观察窗口，覆盖 goroutine/内存稳定段 |
| ping-gap | 25s | 低于 WS 空闲超时，仅防断链 |
| login-mode | per-user 与 shared 各跑一轮 | 差值 = 登录链路（argon2）开销贡献 |

## session（会话轮次，commit C2）

| 参数 | 推荐值 | 依据 |
|---|---|---|
| 会话到达 | 恒定到达率阶梯（泊松用于 soak） | 拐点可解读性（docs/loadtest.md §5.2） |
| 每会话轮次间隔 | 8-20s | 语料句长 2-6s + 听回复时间，自论证 |
| 说话时长 | 2-6s（语料回放） | real-smoke-corpus 每句 ≤5s 实测 |
| 并发阶梯 | 10 / 30 / 50 / 100 / 200… | 受 loadgen 单机音频会话预算约束（§5.3） |

## interrupt（打断层，commit C3）与 broadcast（数据面，commit C4）

参数在对应 commit 落地时补充本文件。
