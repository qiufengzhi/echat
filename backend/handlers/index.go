package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"echat-backend/config"
	"echat-backend/transport"
)

// wsURL 根据监听地址推断 WebSocket 入口地址（开发用，仅作参考）。
func wsURL() string {
	host := config.Get().Server.Addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	return fmt.Sprintf("ws://%s/ws", host)
}

// IndexHandler 返回一个简单的状态页面，方便直接在浏览器里查看后端和 WebSocket 入口。
//
// 注意：本 handler 在 app.mountTransportEntry 里注册在 "/" 上，而 Go 的 ServeMux 中 "/"
// 是「兜底（catch-all）」——所有没匹配到其它路由的请求都会落到这里（例如被网关改错前缀的
// /v1/auth/login）。曾经的线上事故正是如此：请求路径被 nginx 改坏后本该报 404，
// 却被这个 HTML 状态页以 200 接住，看起来像"前端页面被返回了"，排查成本极高。
//
// 因此这里显式收口：只有精确访问 "/" 才给状态页，其它未匹配路径一律返回标准 JSON 404，
// 让路由/代理配置错误立刻暴露，而不是被一个 HTML 页面掩盖。
func IndexHandler(w http.ResponseWriter, r *http.Request) {
	// 非根路径＝未命中任何业务路由：走统一错误包络，Content-Type 保持 application/json
	if r.URL.Path != "/" {
		transport.WriteError(w, &transport.Error{
			Code:    "NOT_FOUND",
			Message: "接口不存在",
			Status:  http.StatusNotFound,
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>eChat 语音间</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
            max-width: 720px;
            margin: 40px auto;
            padding: 24px;
            line-height: 1.6;
            background: #f5f7fb;
            color: #1f2937;
        }
        main {
            background: #ffffff;
            border-radius: 18px;
            padding: 28px;
            box-shadow: 0 18px 50px rgba(15, 23, 42, 0.12);
        }
        h1 {
            margin-top: 0;
        }
        code {
            background: #eef2ff;
            padding: 2px 8px;
            border-radius: 6px;
        }
        .card {
            margin-top: 20px;
            padding: 16px 18px;
            background: #f8fafc;
            border-radius: 12px;
        }
    </style>
</head>
<body>
    <main>
        <h1>Voice Room Backend</h1>
        <p>后端服务已经启动，可以通过 WebSocket 为前端提供房间和信令能力。</p>

        <div class="card">
            <strong>WebSocket 地址</strong>
            <p><code>%s</code></p>
        </div>

        <div class="card">
            <strong>启动方式</strong>
            <p><code>cd backend && go run .</code></p>
        </div>

        <div class="card">
            <strong>前端默认地址</strong>
            <p><code>http://localhost:5173</code></p>
        </div>
    </main>
</body>
</html>
`, wsURL())
}
