package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"echat-backend/authn"
	"echat-backend/config"
	"echat-backend/logging"
	"echat-backend/room/gateway"
	"echat-backend/room/signaling"

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
		// checkOrigin 按配置决定握手来源校验策略
		// ws_check_origin=true 时保持 nil，走 gorilla 默认同源校验（无 Origin 头放行，Origin host 必须等于 Host）
		// false 时显式放行任意来源，仅供本地调试与压测等受控环境使用
		var checkOrigin func(r *http.Request) bool
		if !cfg.WSCheckOrigin {
			checkOrigin = func(r *http.Request) bool { return true }
		}
		upgrader := websocket.Upgrader{
			ReadBufferSize:  cfg.WSReadBuffer,
			WriteBufferSize: cfg.WSWriteBuffer,
			CheckOrigin:     checkOrigin,
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

		signaling.HandleConnection(conn, gateway.ConnIdentity{
			UserID:       claims.SubjectUUID().String(),
			SessionID:    claims.SessionID.String(),
			TokenVersion: claims.TokenVersion,
		})
	})
}

// writeUpgradeError 在升级前把鉴权失败写成 JSON，浏览器 WS 表现为握手失败
// 非 *authn.Error（如 DB 超时/连接池饱和）会以 500 兜底——必须落日志留根因，
// 否则高负载握手失败无法归因（压测 2 万档 1834 个 500 无从定位的教训）
func writeUpgradeError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	code := "TOKEN_INVALID"
	message := "登录已失效，请重新登录"
	if ae, ok := err.(*authn.Error); ok {
		status = ae.Status
		code = ae.Code
		message = ae.Message
	} else {
		logger.Warnw("ws upgrade auth failed（非领域错误，按 500 兜底）", "error", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}