// 实验用 WS 探针：带 access token 握手 + join 房间 + 打印消息 / 关闭码
// 用法:
//   带合法 token 进房并在被踢时观察 4001:  go run ./probe/ws_auth -url "ws://127.0.0.1:8081/ws?token=<access>" -join
//   带非法 token 观察 401 拒绝:          go run ./probe/ws_auth -url "ws://127.0.0.1:8081/ws?token=bad"
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	url := flag.String("url", "", "ws 地址，如 ws://127.0.0.1:8081/ws?token=xxx")
	join := flag.Bool("join", false, "连接成功后发送 join 进房")
	readTimeout := flag.Duration("read-timeout", 30*time.Second, "整体读取时长上限，超时主动收尾")
	flag.Parse()

	if *url == "" {
		fmt.Println("缺少 -url")
		os.Exit(2)
	}

	conn, resp, err := websocket.DefaultDialer.Dial(*url, nil)
	if err != nil {
		fmt.Printf("DIAL_FAIL err=%v\n", err)
		if resp != nil {
			body := make([]byte, 512)
			n, _ := resp.Body.Read(body)
			fmt.Printf("  http_status=%d body=%s\n", resp.StatusCode, string(body[:n]))
		}
		return
	}
	defer conn.Close()
	fmt.Println("CONNECTED")

	if *join {
		if err := conn.WriteJSON(map[string]any{"type": "join", "room_id": "TESTAUTH", "payload": "wsprobe"}); err != nil {
			fmt.Printf("JOIN_SEND_FAIL err=%v\n", err)
			return
		}
		fmt.Println("JOIN_SENT")
	}

	start := time.Now()
	deadlineAbs := start.Add(*readTimeout)
	for {
		remain := time.Until(deadlineAbs)
		if remain <= 0 {
			fmt.Println("READY_TIMEOUT")
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(remain))
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			if ce, ok := err.(*websocket.CloseError); ok {
				fmt.Printf("CLOSE code=%d text=%q\n", ce.Code, ce.Text)
				return
			}
			fmt.Printf("READ_END err=%v\n", err)
			return
		}
		fmt.Printf("MSG(%d) %.4fs %s\n", mt, time.Since(start).Seconds(), string(msg))
	}
}