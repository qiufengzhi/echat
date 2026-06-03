package room

// 信令消息类型常量
const (
	// ---------- 客户端 → 服务端 ----------
	MsgTypeJoin   = "join"   // 用户请求加入房间
	MsgTypeOffer  = "offer"  // WebRTC SDP Offer，转发给房间另一成员
	MsgTypeAnswer = "answer" // WebRTC SDP Answer，转发给房间另一成员
	MsgTypeICE    = "ice"    // WebRTC ICE 候选地址，转发给房间另一成员
	MsgTypeLeave  = "leave"  // 用户主动离开房间
	MsgTypePing   = "ping"   // 心跳探测，服务端回复 pong

	// ---------- 服务端 → 客户端 ----------
	MsgTypePong       = "pong"        // 心跳响应，回应客户端 ping
	MsgTypeWaiting    = "waiting"     // 等待第二人加入房间
	MsgTypeRoomReady  = "room_ready"  // 房间人数超过1人，可以开始信令协商
	MsgTypeUserJoined = "user_joined" // 新成员加入，广播给房间现有成员
	MsgTypeUserLeft   = "user_left"   // 成员离开，通知房间剩余成员
	MsgTypeError      = "error"       // 服务端错误消息
)
