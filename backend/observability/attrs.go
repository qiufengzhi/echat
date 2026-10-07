// attrs.go 集中定义 span attribute 键，业务代码不写裸字符串
//
// 关联键只做 span attribute，禁止上 metric label（基数纪律见 docs/observability.md §3.2.5d）
package observability

import "go.opentelemetry.io/otel/attribute"

const (
	// AttrTurnID 一轮对话的唯一 ID，跨 Go/Agent/TTS join 的锚点
	AttrTurnID = "echat.turn_id"
	// AttrSessionID 会话标识，服务端生成的不透明 ID（s_<uuid>），一次会话多次轮次共用
	AttrSessionID = "echat.session_id"
	// AttrRoomID 房间短码
	AttrRoomID = "echat.room_id"
	// AttrClientID 客户端连接 ID
	AttrClientID = "echat.client_id"
	// AttrUserID 说话用户的业务 ID
	AttrUserID = "echat.user_id"
	// AttrInterrupted 本轮是否被打断（true/false）
	AttrInterrupted = "echat.turn.interrupted"
	// AttrProvider TTS/LLM 提供商名（aliyun | xfyun | deepseek）
	AttrProvider = "echat.provider"
)

// PII 属性前缀：默认不写入 span；capture_content=true（仅本机 dev）时承载对话内容
const (
	// AttrPIIUserText 用户输入文本
	AttrPIIUserText = "echat.pii.user_text"
	// AttrPIIAgentText 助手回复文本
	AttrPIIAgentText = "echat.pii.agent_text"
	// AttrPIIToolArgs 工具调用参数 JSON
	AttrPIIToolArgs = "echat.pii.tool_arguments"
)

// TurnID 返回 turn_id 属性
//
// v: 轮次 ID
// return: OTel attribute 键值对
func TurnID(v string) attribute.KeyValue { return attribute.String(AttrTurnID, v) }

// SessionID 返回 session_id 属性
//
// v: 会话 ID
// return: OTel attribute 键值对
func SessionID(v string) attribute.KeyValue { return attribute.String(AttrSessionID, v) }

// RoomID 返回 room_id 属性
//
// v: 房间短码
// return: OTel attribute 键值对
func RoomID(v string) attribute.KeyValue { return attribute.String(AttrRoomID, v) }

// ClientID 返回 client_id 属性
//
// v: 客户端连接 ID
// return: OTel attribute 键值对
func ClientID(v string) attribute.KeyValue { return attribute.String(AttrClientID, v) }

// UserID 返回 user_id 属性
//
// v: 用户 ID
// return: OTel attribute 键值对
func UserID(v string) attribute.KeyValue { return attribute.String(AttrUserID, v) }

// Provider 返回 provider 属性
//
// v: 提供商名（aliyun | xfyun | deepseek）
// return: OTel attribute 键值对
func Provider(v string) attribute.KeyValue { return attribute.String(AttrProvider, v) }

// Bool 返回指定键的布尔属性
//
// key: attribute 键（如 AttrInterrupted 字符串）
// v: 布尔值
// return: OTel attribute 键值对
func Bool(key string, v bool) attribute.KeyValue { return attribute.Bool(key, v) }

// PIIUserText 返回用户输入文本属性（capture_content=true 时才写入）
//
// v: 用户输入
// return: OTel attribute 键值对
func PIIUserText(v string) attribute.KeyValue { return attribute.String(AttrPIIUserText, v) }

// PIIAgentText 返回助手回复文本属性（capture_content=true 时才写入）
//
// v: 助手回复
// return: OTel attribute 键值对
func PIIAgentText(v string) attribute.KeyValue { return attribute.String(AttrPIIAgentText, v) }
