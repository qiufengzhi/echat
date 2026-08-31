// 实验用 JetStream 订阅探针：以 pull 持久消费者消化 user.> 事件，验证「Outbox → JetStream → 消费者」全链闭环
// 用法:
//   go run ./probe/jetstream_consumer -seconds 20 -url 127.0.0.1:4222
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

func main() {
	url := flag.String("url", "127.0.0.1:4222", "NATS 地址")
	stream := flag.String("stream", "echat_events", "JetStream 流名")
	durable := flag.String("durable", "echat-user-events-demo", "持久消费者名，测试断点续传")
	seconds := flag.Int("seconds", 15, "订阅时长（秒），到时退出")
	flag.Parse()

	nc, err := nats.Connect(*url, nats.Name("jetstream-consumer-probe"))
	if err != nil {
		fmt.Printf("CONNECT_FAIL %v\n", err)
		return
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		fmt.Printf("JETSTREAM_FAIL %v\n", err)
		return
	}

	// 幂等创建持久消费者：限定 user.>，显式 ack + 全部投递（含历史上已发布事件，验证回放）
	if _, err = js.AddConsumer(*stream, &nats.ConsumerConfig{
		Durable:       *durable,
		AckPolicy:     nats.AckExplicitPolicy,
		DeliverPolicy: nats.DeliverAllPolicy,
		FilterSubject: "user.>",
	}); err != nil && !errors.Is(err, nats.ErrConsumerNameAlreadyInUse) {
		fmt.Printf("ADD_CONSUMER_FAIL %v\n", err)
		return
	}

	// pull 订阅：靠 Fetch 拉取，消费进度持久化到流上，可断点续传
	sub, err := js.PullSubscribe("user.>", *durable, nats.Bind(*stream, *durable))
	if err != nil {
		fmt.Printf("SUBSCRIBE_FAIL %v\n", err)
		return
	}
	defer sub.Drain()

	deadline := time.Now().Add(time.Duration(*seconds) * time.Second)
	for time.Now().Before(deadline) {
		fetchCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		msgs, err := sub.Fetch(16, nats.Context(fetchCtx))
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) {
				continue
			}
			fmt.Printf("FETCH_END %v\n", err)
			return
		}
		for _, m := range msgs {
			_ = m.Ack()
			fmt.Printf("[%s] msg_id=%s subject=%s %s\n",
				time.Now().Format("15:04:05"), m.Header.Get("Nats-Msg-Id"), m.Subject, string(m.Data))
		}
	}
	fmt.Println("SUBSCRIBE_DONE")
}