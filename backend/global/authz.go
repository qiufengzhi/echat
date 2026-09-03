package global

import "echat-backend/authz"

// AuthZ SpiceDB 授权服务全局引用，由 main 启动时注入
// 未启用（spicedb.enabled=false）或启动时连接失败时为 nil，调用方需判空降级
var AuthZ *authz.Client