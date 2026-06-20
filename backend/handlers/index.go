package handlers

import "net/http"

// IndexHandler 返回一个简单的状态页面，方便直接在浏览器里查看后端和 WebSocket 入口
func IndexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`
<!DOCTYPE html>
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
            <p><code>ws://localhost:8080/ws</code></p>
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
	`))
}
