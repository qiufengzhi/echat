// Package gateway 信令传输适配层：连接对象、传输无关帧抽象与连接生命周期
//
// 分层：transport 适配层——只关心「一条连接的读写与存活」，不做房间业务
// 上行一帧原始 JSON 交回编排层（ClientHandler.OnFrame），连接退出交由编排层收尾（ClientHandler.OnClosed）
// 房间/角色等域状态一律不在此出现，成员连接以 room/aggregate.Session 接口暴露给领域
package gateway
