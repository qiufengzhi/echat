// Package app 应用装配根（application 层）：显式构造各领域组件，依赖顺序在此体现
//
// 定位：四层模型中的 application 编排层——持有各领域 handler 与基础设施引用，
// 把 store / Redis / 认证服务显式装配进 App，再统一挂载到 transport 基元包装过的路由树
// main 仅保留「启动可选后台子系统」（outbox/SFU/清理协程/WebTransport 等），本阶段收编主链路
// 依赖单向向下：app import transport 与各领域/基础设施，领域间不反向依赖 app
package app

import (
	"context"
	"net/http"
	"time"

	"echat-backend/authn"
	"echat-backend/config"
	"echat-backend/handlers"
	"echat-backend/logging"
	"echat-backend/profile"
	"echat-backend/room/api"
	"echat-backend/room/projection"
	"echat-backend/store"
	"echat-backend/transport"

	"github.com/redis/go-redis/v9"
)

// App 装配根：按依赖顺序显式构造，路由与通用中间件统一在此收敛
type App struct {
	// cfg 全局配置引用，供路由层按需读取（后续阶段收编后仅存装配用）
	cfg *config.Config
	// store PostgreSQL 持久层门面（连接池 + Ent）
	store *store.Store
	// rdb 登录限流与在线热状态共享的 Redis 客户端
	rdb *redis.Client
	// authSvc 认证域服务（WebSocket/WebTransport 握手与登录限流共用）
	authSvc *authn.Service
	// auth 认证域 HTTP 处理器
	auth *authn.Handler
	// profile 资料域 HTTP 处理器（资料编辑 + 头像上传）
	profile *profile.Handler
	// mux 承载全部业务路由的根 mux
	mux *http.ServeMux
}

// New 按依赖顺序显式构造 App，失败返回 error 而非 Fatal
// cfg 已加载的完整配置；打开 DB 失败或 schema 落后均返回错误交 main 处理
func New(cfg *config.Config) (*App, error) {
	// 1. PostgreSQL 持久层：连接失败即退出（提示先启动 dev compose）
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dbCancel()
	st, err := store.Open(dbCtx, cfg.Database)
	if err != nil {
		return nil, err
	}

	// 2. 开发期 schema 校验（落后即报错提示跑迁移，不自动改结构）
	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer migCancel()
	if err = st.Migrate(migCtx); err != nil {
		st.Close()
		return nil, err
	}

	// 3. 认证域服务与 HTTP 处理器
	authSvc := authn.NewService(st.Ent(), cfg.Auth, authn.ConsoleMailer{}, cfg.Auth.VerifyBaseURL)

	// 4. Redis：优先分布式登录限流与在线热状态；连不上降级进程内实现，不阻断启动
	rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password})
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err = rdb.Ping(pingCtx).Err(); err != nil {
		logging.L().Warnw("Redis 不可用，登录限流降级为进程内实现", "addr", cfg.Redis.Addr, "error", err)
	} else {
		authSvc.SetLoginLimiter(authn.NewRedisLoginLimiter(rdb))
		projection.SetOnlineStore(rdb) // 在线热状态瞬态层复用同一连接
	}
	pingCancel()

	authH := authn.NewHandler(authSvc)
	prof, err := profile.NewHandler(st.Ent(), cfg.Uploads.Dir, authH)
	if err != nil {
		st.Close()
		return nil, err
	}

	a := &App{
		cfg:     cfg,
		store:   st,
		rdb:     rdb,
		authSvc: authSvc,
		auth:    authH,
		profile: prof,
		mux:     http.NewServeMux(),
	}
	return a, nil
}

// Mount 各领域路由注册到同一 mux（认证域 + 房间查询 API + 静态/WS 入口）
// 后续阶段收编 SFU/ASR/LLM/TTS 等领域时在此追加各领域 Register
func (a *App) Mount() {
	a.auth.Register(a.mux)
	api.NewHandler(a.store.Pool(), a.rdb, a.auth).Register(a.mux)
	a.profile.Register(a.mux)
	a.mountTransportEntry()
}

// Handler 返回套好通用中间件链的根 http.Handler，供 main 交给 http.ListenAndServe
// 中间件顺序：RequestID 最内先注入请求 ID，Recover 兜底 panic，链路行为全局一致
func (a *App) Handler() http.Handler {
	return transport.Chain(a.mux, transport.Recover, transport.RequestID)
}

// AuthService 暴露认证服务引用，供 WebTransport 端点握手复用（main 组装时取用）
func (a *App) AuthService() *authn.Service {
	return a.authSvc
}

// Store 暴露持久层门面，供 outbox relay 等后台子系统取用连接池
func (a *App) Store() *store.Store {
	return a.store
}

// Redis 暴露共享 Redis 客户端，供读模型投影（Redis 侧）与后续热状态子系统取用
func (a *App) Redis() *redis.Client {
	return a.rdb
}

// mountTransportEntry 注册静态首页与 WebSocket 信令入口
func (a *App) mountTransportEntry() {
	a.mux.HandleFunc("/", handlers.IndexHandler)
	a.mux.HandleFunc("/ws", handlers.WebSocketHandler(a.authSvc))
}
