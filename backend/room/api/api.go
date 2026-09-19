package api

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"echat-backend/authn"
	"echat-backend/transport"
)

// Handler 房间查询 REST 处理器，依赖事实表连接池与 Redis 读模型
type Handler struct {
	// pool 数据库连接池，读 rooms / user_room_history
	pool *pgxpool.Pool
	// rdb Redis 客户端，读 active_rooms / room:members:* 读模型
	rdb *redis.Client
	// auth 认证域处理器，提供 AccessRequired 中间件与 claims 解析
	auth *authn.Handler
}

// NewHandler 构造房间查询处理器
// pool 数据库连接池，rdb Redis 客户端，auth 认证域处理器（MyRooms 需用户身份）
func NewHandler(pool *pgxpool.Pool, rdb *redis.Client, auth *authn.Handler) *Handler {
	return &Handler{pool: pool, rdb: rdb, auth: auth}
}

// Register 房间查询路由注册（P6-2 新增，取代过渡期仅返回 exists 的占位端点）
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/rooms", transport.Adapt(h.auth.AccessRequired(h.listRooms)))
	mux.HandleFunc("GET /api/v1/rooms/{code}", transport.Adapt(h.auth.AccessRequired(h.getRoom)))
	mux.HandleFunc("GET /api/v1/me/rooms", transport.Adapt(h.auth.AccessRequired(h.myRooms)))
	mux.HandleFunc("GET /api/v1/me/preferences", transport.Adapt(h.auth.AccessRequired(h.getPreferences)))
	mux.HandleFunc("PUT /api/v1/me/preferences", transport.Adapt(h.auth.AccessRequired(h.putPreferences)))
}
