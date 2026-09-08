// handle.go signaling 编排层：连接门面、信令分发与房间用例编排
//
// 编排职责：把 gateway 的连接事件接进聚合操作（aggregate），并把结果路由给
// 事实落库（persist）、授权投影（projection）与 SFU 引擎；广播仍在本层收敛
// 实时优先：落库/投影失败仅告警，不阻断广播与信令主链路
package signaling

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"echat-backend/global"
	"echat-backend/room/aggregate"
	"echat-backend/room/events"
	"echat-backend/room/gateway"
	"echat-backend/room/projection"
)

// authzTimeout 单次授权投影调用的超时：实时信令路径不等待授权服务
const authzTimeout = 1500 * time.Millisecond

// HandleConnection 为新信令连接创建会话并绑定鉴权身份，随后启动读写与房间编排
// 供 WebSocket 与 WebTransport 传输端点共用（对外签名与原 room.HandleConnection 对齐）
func HandleConnection(conn gateway.MessageFramer, identity gateway.ConnIdentity) {
	gateway.HandleConnection(conn, identity, signalHandler{})
}

// signalHandler 桥接 gateway 连接生命周期到编排层：上行分发、退出收尾
type signalHandler struct{}

// OnFrame 读到一帧上行原始 JSON：解码为 Message 后按类型分发
func (signalHandler) OnFrame(c *gateway.Client, raw []byte) {
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		logger.Warnw("无效消息", "userID", c.UserID, "error", err)
		return
	}
	handleMessage(c, &msg)
}

// OnClosed 连接读循环退出：按断线语义收尾（正常 leave 已触发过则幂等跳过）
func (signalHandler) OnClosed(c *gateway.Client) {
	disconnect(c, "", "disconnect")
}

// handleMessage 根据消息类型把请求分发到 SFU 信令处理、加入房间、离开房间或心跳响应
//
// SFU 信令流程（客户端发起 Offer）：
//
//	客户端 join → 服务端创建 Room + SFU PeerConnection → waiting/room_ready 回客户端
//	客户端创建 Offer → sfu_offer 给服务端 → 服务端创建 Answer → sfu_answer 给客户端
//	客户端收集到 ICE Candidate → sfu_ice 给服务端
//	SFU 引擎收集到 ICE Candidate → sfu_ice 给客户端
func handleMessage(client *gateway.Client, msg *Message) {
	switch msg.Type {
	case MsgTypeJoin:
		handleJoin(client, msg)
	case MsgTypeSFUOffer:
		handleSFUOffer(client, msg.Payload)
	case MsgTypeSFUICE:
		handleSFUICE(client, msg.Payload)
	case MsgTypeRenegotiationAnswer:
		handleRenegotiationAnswer(client, msg.Payload)
	case MsgTypeLeave:
		handleLeave(client, msg.Payload)
	case MsgTypePing:
		if client.RoomID != "" {
			projection.MarkOnline(client.RoomID, client.UserID)
		}
		sendToClient(client, MsgTypePong, nil, client.RoomID)
	case MsgTypeAiToggle:
		handleAiToggle(client, msg)
	case MsgTypeRaiseHand:
		handleRaiseHand(client)
	case MsgTypeApproveMic:
		handleApproveMic(client, msg.Payload)
	case MsgTypeRejectMic:
		handleRejectMic(client, msg.Payload)
	case MsgTypeKickMic:
		handleKickMic(client, msg.Payload)
	case MsgTypeMuteMic:
		handleMuteMic(client, msg.Payload)
	}
}

// handleAiToggle 处理 AI 助手开关请求，按开关更新房间 AI 状态
// 状态迁移事件由 StartAIStateBroadcaster 统一广播给全房间，此处不再单独回复
func handleAiToggle(client *gateway.Client, msg *Message) {
	var req AiToggleReq
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		sendError(client, "invalid payload")
		return
	}

	roomID := client.RoomID
	if roomID == "" {
		sendError(client, "join a room first")
		return
	}

	// 权限控制面：manage_ai 仅房主/副主持可切换 AI，越权请求拒绝
	if !canManageAI(roomID, client.UserID) {
		sendError(client, "无权限管理 AI 助手")
		return
	}

	if req.Enable {
		aiState.Set(roomID, "online") // 开启：直接进入在线状态
		recordAiToggle(roomID, "online")
	} else {
		aiState.Set(roomID, "offline") // 关闭：回到离线状态
		recordAiToggle(roomID, "offline")
	}
}

// recordAiToggle 记录 AI 开关领域事件（聚合根 id 来自在线房间注册表）
// 失败仅告警，不阻断状态迁移广播
func recordAiToggle(roomID string, state string) {
	r := getRoomByID(roomID)
	if recorder == nil || r == nil || r.AggID == "" {
		return
	}
	aggID, err := uuid.Parse(r.AggID)
	if err != nil {
		return
	}
	recorder.Record(context.Background(), events.RoomEvent{
		Type:        events.EventTypeRoomAiToggled,
		AggregateID: aggID,
		Subject:     events.Subject(aggID, "ai_toggled"),
		Payload:     map[string]any{"room_code": roomID, "state": state},
	})
}

// handleJoin 把客户端加入指定房间，创建 SFU PeerConnection（不生成 Offer）
// 聚合状态变更 → 事实落库 → 授权投影 → 在线心跳 → SFU 引擎接入
func handleJoin(client *gateway.Client, msg *Message) {
	roomID := strings.TrimSpace(msg.RoomID)
	if roomID == "" {
		sendError(client, "room_id is required")
		return
	}

	username := parseUsername(msg.Payload)
	if username == "" {
		username = "用户" + client.UserID[:8]
	}

	// 加入信令房间（成员键是连接 ID，房主身份是用户 ID，二者分离）
	r := getOrCreateRoom(roomID)
	client.RoomID = roomID
	client.Username = username
	client.JoinedAt = time.Now()
	outcome := r.Join(client)

	logger.Infow("用户已加入房间",
		"username", username, "roomID", roomID, "userCount", outcome.UserCount, "hostID", outcome.HostID,
	)

	// 当前态写库 + 事件同事务记账；失败仅告警，不阻断实时广播（实时优先策略）
	if persister != nil {
		_ = persister.JoinRoom(context.Background(), r, client)
	}

	// 授权投影：新角色写入 SpiceDB 关系元组（用事务收敛后的聚合根 id 作对象 id），失败降级告警
	if outcome.ProjectedRole != "" && r.AggID != "" {
		assignRole(r, client.UserID, outcome.ProjectedRole)
	}

	// 在线热状态：加入即上报心跳，加入房间在线集合（瞬态层）
	projection.MarkOnline(roomID, client.UserID)

	// --- SFU 集成：创建 PeerConnection，但不生成 Offer ---
	// Offer 由客户端发起，服务端收到 sfu_offer 后通过 AcceptOffer 创建 Answer
	sfuRoom := sfuServer.GetOrCreateRoom(roomID)

	// 注册 ICE Candidate 回调：SFU 引擎收集到 candidate 后通过信令转发给客户端
	sfuRoom.SetOnICECandidate(func(clientID string, candidate webrtc.ICECandidateInit) {
		if target := clientInRoom(roomID, clientID); target != nil {
			payload := SFUPayloadFromICECandidateInit(candidate)
			sendToClient(target, MsgTypeSFUICE, payload, roomID)
		}
	})

	// 注册 renegotiation 回调：当 SFU 向订阅者添加中继音轨后，需要向该客户端发送 renegotiation Offer
	sfuRoom.SetOnRenegotiation(func(clientID string, offerSDP string) {
		if target := clientInRoom(roomID, clientID); target != nil {
			sendToClient(target, MsgTypeRenegotiationOffer, RenegotiationOfferPayload{SDP: offerSDP}, roomID)
		}
	})

	// 让 SFU 引擎为该客户端创建 PeerConnection（不生成 Offer）
	if err := sfuRoom.Join(client.ConnID); err != nil {
		logger.Warnw("加入失败", "userID", client.ConnID[:8], "error", err)
		sendError(client, "无法创建 WebRTC 连接，请重试")
		return
	}

	// --- 房间状态广播 ---
	if outcome.UserCount == 1 {
		// 首位成员：发送 waiting，告知其是房主
		sendToClient(client, MsgTypeWaiting, WaitingPayload{HostID: outcome.HostID}, roomID)
		return
	}

	// 通知已有成员新用户已加入
	broadcastToRoom(roomID, client.ConnID, MsgTypeUserJoined, UserJoinedPayload{
		UserID:   client.UserID,
		Username: username,
		HostID:   outcome.HostID,
	})

	// 通知新加入者房间成员快照
	sendToClient(client, MsgTypeRoomReady, RoomReadyPayload{
		Users:    roomUsers(roomID),
		HostID:   outcome.HostID,
		CanStart: true,
	}, roomID)
}

// handleSFUOffer 处理客户端发来的 SDP Offer，通过 SFU 引擎创建 Answer 并返回
func handleSFUOffer(client *gateway.Client, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	var offer SFUOfferPayload
	if err := json.Unmarshal(payload, &offer); err != nil {
		logger.Warnw("sfu_offer 内容无效", "userID", client.ConnID[:8], "error", err)
		return
	}

	sfuRoom := sfuServer.GetRoom(client.RoomID)
	if sfuRoom == nil {
		logger.Warnw("SFU 房间未找到", "roomID", client.RoomID)
		return
	}

	answerSDP, err := sfuRoom.AcceptOffer(client.ConnID, offer.SDP)
	if err != nil {
		logger.Warnw("接受 Offer 失败", "userID", client.ConnID[:8], "error", err)
		sendError(client, "信令协商失败")
		return
	}

	// 将 Answer SDP 通过 sfu_answer 发回客户端
	sendToClient(client, MsgTypeSFUAnswer, SFUAnswerPayload{SDP: answerSDP}, client.RoomID)
}

// handleRenegotiationAnswer 处理客户端对 renegotiation Offer 的 Answer，交回 SFU 引擎处理
func handleRenegotiationAnswer(client *gateway.Client, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	var answer RenegotiationAnswerPayload
	if err := json.Unmarshal(payload, &answer); err != nil {
		logger.Warnw("renegotiation answer 内容无效", "userID", client.ConnID[:8], "error", err)
		return
	}

	sfuRoom := sfuServer.GetRoom(client.RoomID)
	if sfuRoom == nil {
		logger.Warnw("SFU 房间未找到", "roomID", client.RoomID)
		return
	}

	if err := sfuRoom.AcceptRenegotiationAnswer(client.ConnID, answer.SDP); err != nil {
		logger.Warnw("renegotiation answer 处理失败", "userID", client.ConnID[:8], "error", err)
		return
	}
}

// handleSFUICE 处理客户端发来的 ICE Candidate，传递给 SFU 引擎
func handleSFUICE(client *gateway.Client, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	var ice SFUICEPayload
	if err := json.Unmarshal(payload, &ice); err != nil {
		logger.Warnw("sfu_ice 内容无效", "userID", client.ConnID[:8], "error", err)
		return
	}

	sfuRoom := sfuServer.GetRoom(client.RoomID)
	if sfuRoom == nil {
		logger.Warnw("SFU 房间未找到", "roomID", client.RoomID)
		return
	}

	if err := sfuRoom.AcceptICECandidate(client.ConnID, ice.ToWebRTCICECandidateInit()); err != nil {
		logger.Warnw("ICE Candidate 添加失败", "userID", client.ConnID[:8], "error", err)
	}
}

// handleRelay 把 offer、answer、ice 这类 WebRTC 信令原样转发给同房间其他成员
// 已弃用，由 SFU 替代。保留以供旧客户端兼容，当前不被 handleMessage 调用
func handleRelay(client *gateway.Client, msgType string, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	broadcastRawToRoom(client.RoomID, client.ConnID, msgType, payload)
	logger.Infow("已转发信令", "msgType", msgType, "from", client.ConnID[:8])
}

// handleLeave 处理客户端主动离开房间的请求
// payload 可包含 next_host_id；服务端会在 disconnect 中校验该目标是否仍在线
func handleLeave(client *gateway.Client, payload json.RawMessage) {
	var leavePayload LeavePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &leavePayload); err != nil {
			logger.Warnw("离开消息无效", "userID", client.UserID, "error", err)
		}
	}

	logger.Infow("用户请求离开",
		"userID", client.UserID,
		"roomID", client.RoomID,
		"username", client.Username,
		"nextHost", leavePayload.NextHostID,
	)
	disconnect(client, strings.TrimSpace(leavePayload.NextHostID), "leave")
}

// disconnect 清理客户端、房间成员关系、SFU PeerConnection 与连接资源
// preferredNextHostID 只在离开者是当前房主时生效，且必须指向仍在房间内的成员
// reason 离开来源（leave 主动离开 / disconnect 断线 / kicked 封禁踢下线），供事件记账区分
func disconnect(client *gateway.Client, preferredNextHostID string, reason string) {
	client.WithCloseOnce(func() {
		roomID := client.RoomID

		// 先清理 SFU 连接，确保停止音轨转发
		if roomID != "" {
			if sfuRoom := sfuServer.GetRoom(roomID); sfuRoom != nil {
				sfuRoom.Leave(client.ConnID)
				// 如果 SFU 房间已空，也清理 SFU 房间
				if sfuRoom.PeerCount() == 0 {
					sfuServer.RemoveRoom(roomID)
				}
			}
		}

		// 如果客户端已加入房间，则先更新房间成员和房主，再广播离开事件
		if roomID != "" {
			r := getRoomByID(roomID)
			if r != nil {
				outcome := r.Leave(client.ConnID, client.UserID, preferredNextHostID)

				if outcome.Remaining > 0 {
					broadcastToRoom(roomID, client.ConnID, MsgTypeUserLeft, UserLeftPayload{
						UserID: client.UserID,
						HostID: outcome.NextHostID,
					})

					if outcome.WasHost && outcome.NextHostID != "" && outcome.NextHostID != client.UserID {
						aiState.Set(roomID, "offline") // 房主交接时重置 AI 为离线，新房主需重新开启
						broadcastToRoom(roomID, client.ConnID, MsgTypeHostChanged, map[string]string{
							"host_id": outcome.NextHostID,
						})
					}
				}

				// 当前态写库 + 事件同事务记账：离开/交接/清空在一个事务内收敛
				if persister != nil {
					_ = persister.LeaveRoom(context.Background(), r, client.UserID,
						outcome.WasHost, outcome.NextHostID, outcome.ShouldDelete, reason)
				}

				// 授权投影：房主交接写 SpiceDB，随后撤销离开者全部角色元组（幂等）
				if outcome.WasHost && outcome.NextHostID != "" {
					transferHost(r, client.UserID, outcome.NextHostID)
				}
				removeRoles(r, client.UserID, outcome.RemovedRole, outcome.RemovedMuted)

				// 在线热状态：离开即从房间在线集合移除（瞬态层）
				projection.MarkOffline(roomID, client.UserID)

				// 房间清空：从注册表删除并清理 AI 状态
				if outcome.ShouldDelete {
					if cur, ok := rooms.Get(roomID); ok {
						cur.Lock.RLock()
						empty := len(cur.Clients) == 0
						cur.Lock.RUnlock()
						if empty {
							rooms.Delete(roomID)
							aiState.Remove(roomID)
							logger.Infow("房间已删除", "roomID", roomID)
						}
					}
				}
			}
		}

		// 从全局客户端索引移除并关闭发送队列与底层传输
		gateway.RemoveClient(client.ConnID)
		client.CloseTransport()
		logger.Infow("客户端已断开", "userID", client.UserID)
	})
}

// assignRole 投影某用户获得某角色（write-through，幂等）
func assignRole(r *aggregate.Room, userID, role string) {
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	_ = roleProjector.Assign(ctx, r.AggID, userID, role)
}

// transferHost 投影房主交接（删旧 host 写新 host，防双房主）
func transferHost(r *aggregate.Room, from, to string) {
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	_ = roleProjector.TransferHost(ctx, r.AggID, from, to)
}

// removeRoles 撤销离开者在房间的全部授权元组：互斥角色与静音叠加
// role 离开时持有的互斥角色，muted 是否带静音叠加
func removeRoles(r *aggregate.Room, userID, role string, muted bool) {
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	if role != "" {
		_ = roleProjector.Remove(ctx, r.AggID, userID, role)
	}
	if muted {
		_ = roleProjector.Remove(ctx, r.AggID, userID, aggregate.RoleMuted)
	}
}

// canManageAI 判定用户是否可管理 AI 助手（manage_ai = host + cohost）
func canManageAI(roomID, userID string) bool {
	return canPerm(roomID, userID, "manage_ai")
}

// canModMic 判定用户是否可管理麦克风（mod_mic = host + cohost，审批/请下麦/静音前置）
func canModMic(roomID, userID string) bool {
	return canPerm(roomID, userID, "mod_mic")
}

// canPerm 判权查询 SpiceDB 派生权限：AuthZ 未注入时放行；房间不在线或判定出错保守拒绝
func canPerm(roomID, userID, permission string) bool {
	if global.AuthZ == nil {
		return true
	}
	r := getRoomByID(roomID)
	if r == nil {
		return false
	}
	return projection.Can(r.AggID, userID, permission)
}

// parseUsername 从 join 消息载荷中提取用户名，并做基本的空白裁剪
// 兼容 JSON 字符串和少量直接传原始字符串的简单客户端
func parseUsername(payload json.RawMessage) string {
	var username string
	if err := json.Unmarshal(payload, &username); err == nil {
		return strings.TrimSpace(username)
	}
	return strings.Trim(strings.TrimSpace(string(payload)), "\"")
}

// roomUsers 返回房间内当前所有用户的 wire 摘要列表（按加入时间稳定排序）
func roomUsers(roomID string) []RoomUser {
	r := getRoomByID(roomID)
	if r == nil {
		return nil
	}
	states := r.Users()
	users := make([]RoomUser, 0, len(states))
	for _, s := range states {
		users = append(users, RoomUser{ID: s.UserID, Username: s.Username, Role: s.Role})
	}
	return users
}
