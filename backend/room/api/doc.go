// Package api 房间查询 REST 处理器：读模型与事实表组合出「频道列表/房间详情/我的历史」
//
// 数据来源分层：
//   - 活跃房间索引 active_rooms（Redis SET，由投影重建）决定「有哪些频道」
//   - rooms / user_room_history 事实与读模型表（PG）补齐元数据
//   - room:members:{code}（Redis HASH）补齐成员快照
//
// 设计意图：实时信令层只维护内存房间，查询一律落读模型/Redis，进程重启不丢历史
package api
