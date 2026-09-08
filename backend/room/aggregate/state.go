package aggregate

// 房间事实行状态取值（rooms.status，varchar 存储，值语义见 RoomStatus*）
// 状态机：scheduled ──> active ──> closed ──> archived
// 预约房提前建行不占内存（只在 rooms 表）；active 才进内存 RoomRegistry；closed 清空；archived 归档可查
const (
	// RoomStatusScheduled 预约房：已建事实行未开播，首位成员加入时转 active 并载入内存
	RoomStatusScheduled = "scheduled"
	// RoomStatusActive 进行中：房间在内存中运行，成员在场关系与事件正常落库
	RoomStatusActive = "active"
	// RoomStatusClosed 已关闭：房间清空，等待归档策略收敛
	RoomStatusClosed = "closed"
	// RoomStatusArchived 已归档：只存在于事实表与读模型，可查询不可再入内存
	RoomStatusArchived = "archived"
)

// RoomStatuses 全部合法状态集合，供外部校验房间 status 值
var RoomStatuses = []string{RoomStatusScheduled, RoomStatusActive, RoomStatusClosed, RoomStatusArchived}

// CanRoomTransition 判定房间状态迁移是否合法（显式状态机，防非法跳转）
// from 迁移前状态，to 迁移后状态；合法返回 true
func CanRoomTransition(from, to string) bool {
	switch from {
	case RoomStatusScheduled:
		// 预约房可被首位成员激活，也可直接取消归档
		return to == RoomStatusActive || to == RoomStatusArchived
	case RoomStatusActive:
		// 进行中清空即关闭（成员离开收敛）；也可直接归档
		return to == RoomStatusClosed || to == RoomStatusArchived
	case RoomStatusClosed:
		// 已关闭可被新首位成员复开，或按归档策略归档
		return to == RoomStatusActive || to == RoomStatusArchived
	default:
		// scheduled/archived 之外的不明状态或已归档不允许再迁移
		return false
	}
}
