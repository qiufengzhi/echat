package room

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client tracks one connected browser and the room membership metadata
// needed to keep host ownership stable.
type Client struct {
	ID        string
	RoomID    string
	Username  string
	JoinedAt  time.Time
	Conn      *websocket.Conn
	Send      chan []byte
	closeOnce sync.Once
}

// Room keeps the current host as authoritative backend state.
type Room struct {
	ID      string
	HostID  string
	Clients map[string]*Client
	Lock    sync.RWMutex
}

// RoomUser is the room member summary returned to the frontend.
type RoomUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// WaitingPayload is sent to the first member so the client can mark the host
// before anyone else joins.
type WaitingPayload struct {
	HostID string `json:"host_id"`
}

// RoomReadyPayload is sent to later joiners with the full room snapshot.
type RoomReadyPayload struct {
	Users    []RoomUser `json:"users"`
	HostID   string     `json:"host_id"`
	CanStart bool       `json:"can_start"`
}

// UserJoinedPayload notifies existing members about the newcomer and host.
type UserJoinedPayload struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	HostID   string `json:"host_id"`
}

// UserLeftPayload notifies remaining members after someone leaves and any
// host reassignment has completed.
type UserLeftPayload struct {
	UserID string `json:"user_id"`
	HostID string `json:"host_id,omitempty"`
}

// LeavePayload lets the current host nominate a successor before leaving.
type LeavePayload struct {
	NextHostID string `json:"next_host_id,omitempty"`
}

// Message is the shared websocket envelope between frontend and backend.
type Message struct {
	Type    string          `json:"type"`
	RoomID  string          `json:"room_id"`
	UserID  string          `json:"user_id"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
