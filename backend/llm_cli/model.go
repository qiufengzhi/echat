package llm_cli

import llmpb "echat-backend/proto/llm"

func NewLLMRequest(sessionID, roomID, clientID, userText string, isLast bool, seq int64) *llmpb.LLMRequest {
	return &llmpb.LLMRequest{
		SessionId: sessionID,
		RoomId:    roomID,
		ClientId:  clientID,
		UserText:  userText,
		IsLast:    isLast,
		Seq:       seq,
	}
}
