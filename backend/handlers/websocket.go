package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"echat-backend/authn"
	"echat-backend/config"
	"echat-backend/logging"
	"echat-backend/room"

	"github.com/gorilla/websocket"
)

var logger = logging.New("websocket")

// wsAuthTimeout 握手鉴权上限：数据库慢查询时拒绝升级，避免占连接不鉴权（spec §9.4）
const wsAuthTimeout = 5 * time.Second

// WebSocketHandler 生成绑定鉴权服务的 WS 处理函数：握手必须携带有效 access token 才可升级
// 浏览器 WS 无法自定义 Header，access 走 ?token= query（spec §9.1）；升级成功后把
// 鉴权身份绑定到连接（取代早前「每连接随机 UUID」），之后房间内以上行连接绑定值为权威身份
func WebSocketHandler(authSvc *authn.Service) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := config.Get().Room
		upgrader := websocket.Upgrader{
			ReadBufferSize:  cfg.WSReadBuffer,
			WriteBufferSize: cfg.WSWriteBuffer,
			CheckOrigin:     func(r *http.Request) bool { return cfg.WSCheckOrigin },
		}

		authCtx, cancel := context.WithTimeout(r.Context(), wsAuthTimeout)
		defer cancel()
		claims, err := authSvc.ValidateAccess(authCtx, r.URL.Query().Get("token"))
		if err != nil {
			writeUpgradeError(w, err)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Warnw("websocket upgrade failed", "error", err)
			return
		}

		room.HandleConnection(conn, room.ConnIdentity{
			UserID:       claims.SubjectUUID().String(),
			SessionID:    claims.SessionID.String(),
			TokenVersion: claims.TokenVersion,
		})
	})
}

// writeUpgradeError 在升级前把鉴权失败写成 JSON，浏览器 WS 表现为握手失败
func writeUpgradeError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	code := "TOKEN_INVALID"
	message := "登录已失效，请重新登录"
	if ae, ok := err.(*authn.Error); ok {
		status = ae.Status
		code = ae.Code
		message = ae.Message
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}