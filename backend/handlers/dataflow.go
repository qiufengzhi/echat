package handlers

import (
	"net/http"

	"echat-backend/dataplane"
	"echat-backend/transport"
)

// DataflowSnapshotHandler 返回数据面各队列的积压与累计丢弃快照
//
// 丢弃告警只在累计次数达到阈值后稀疏出现，而队列积压是连续量，
// 该端点用于在没有告警的时候查看现状
//
// return: 处理器，把快照以 JSON 写出，字段见 dataplane.SnapshotEntry
func DataflowSnapshotHandler() transport.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		transport.WriteJSON(w, http.StatusOK, map[string]any{
			"queues": dataplane.Snapshot(),
		})
		return nil
	}
}
