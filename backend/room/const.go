package room

// 信令消息类型常量
const (
	// ---------- 客户端 -> 服务端 ----------
	MsgTypeJoin   = "join"   // 用户请求加入房间，payload 通常是昵称字符串
	MsgTypeOffer  = "offer"  // WebRTC SDP Offer，服务端不解析内容，只转发给同房间其他成员
	MsgTypeAnswer = "answer" // WebRTC SDP Answer，服务端不解析内容，只转发给同房间其他成员
	MsgTypeICE    = "ice"    // WebRTC ICE 候选地址，服务端不解析内容，只转发给同房间其他成员
	MsgTypeLeave  = "leave"  // 用户主动离开房间，房主可携带 next_host_id 指定下一任房主
	MsgTypePing   = "ping"   // 心跳探测，服务端收到后回复 pong，避免 WebSocket 被空闲断开

	// ---------- 服务端 -> 客户端 ----------
	MsgTypePong        = "pong"         // 心跳响应，回应客户端 ping
	MsgTypeWaiting     = "waiting"      // 当前房间只有自己，服务端返回 host_id 让前端先标记房主
	MsgTypeRoomReady   = "room_ready"   // 房间已有其他成员，返回成员快照、host_id，并允许开始信令协商
	MsgTypeUserJoined  = "user_joined"  // 新成员加入，广播给房间已有成员，并携带当前 host_id
	MsgTypeUserLeft    = "user_left"    // 成员离开，通知剩余成员，并携带可能更新后的 host_id
	MsgTypeHostChanged = "host_changed" // 房主发生变更，通知剩余成员刷新房主展示和交接权限
	MsgTypeError       = "error"        // 服务端错误消息，payload.message 可给前端转换成用户提示
)
