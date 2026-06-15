package room

// 信令消息类型常量
const (
	// ---------- 客户端 -> 服务端 ----------
	MsgTypeJoin   = "join"   // 用户请求加入房间，payload 通常是昵称字符串
	MsgTypeOffer  = "offer"  // （已弃用，由 SFU 替代）WebRTC SDP Offer，保留占位以防旧客户端
	MsgTypeAnswer = "answer" // （已弃用）WebRTC SDP Answer
	MsgTypeICE    = "ice"    // （已弃用）WebRTC ICE 候选地址
	MsgTypeLeave  = "leave"  // 用户主动离开房间，房主可携带 next_host_id 指定下一任房主
	MsgTypePing   = "ping"   // 心跳探测，服务端收到后回复 pong，避免 WebSocket 被空闲断开

	// SFU 信令：客户端 -> 服务端
	// 客户端创建 SDP Offer 后通过 sfu_offer 发给 SFU 引擎。
	MsgTypeSFUOffer = "sfu_offer" // 客户端发起的 SDP Offer
	// 客户端的 ICE Candidate 通过 sfu_ice 发送给 SFU 引擎。
	MsgTypeSFUICE = "sfu_ice" // 客户端的 ICE Candidate

	// ---------- 服务端 -> 客户端 ----------
	MsgTypePong        = "pong"         // 心跳响应，回应客户端 ping
	MsgTypeWaiting     = "waiting"      // 当前房间只有自己，服务端返回 host_id 让前端先标记房主
	MsgTypeRoomReady   = "room_ready"   // 房间已有其他成员，返回成员快照、host_id，并允许开始信令协商
	MsgTypeUserJoined  = "user_joined"  // 新成员加入，广播给房间已有成员，并携带当前 host_id
	MsgTypeUserLeft    = "user_left"    // 成员离开，通知剩余成员，并携带可能更新后的 host_id
	MsgTypeHostChanged = "host_changed" // 房主发生变更，通知剩余成员刷新房主展示和交接权限

	// SFU 信令：服务端 -> 客户端
	// SFU 引擎收到客户端发起的 Offer 后创建 Answer，通过 sfu_answer 发送给客户端。
	MsgTypeSFUAnswer = "sfu_answer" // SFU 引擎回复的 SDP Answer
	// SFU 引擎收集到的 ICE Candidate 通过 sfu_ice 发送给客户端。
	MsgTypeSFUICEServer = "sfu_ice" // SFU 引擎的 ICE Candidate

	// SFU renegotiation（重新协商）：当 AddTrack 后触发，由 SFU 向订阅者客户端发送新 Offer。
	MsgTypeRenegotiationOffer = "sfu_renegotiation_offer" // SFU 向客户端发送的新 Offer（服务端 -> 客户端）
	// 客户端收到 renegotiation Offer 后回复 Answer。
	MsgTypeRenegotiationAnswer = "sfu_renegotiation_answer" // 客户端回复的 Answer（客户端 -> 服务端）

	MsgTypeError = "error" // 服务端错误消息，payload.message 可给前端转换成用户提示
)
