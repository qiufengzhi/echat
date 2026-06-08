import type { RoomActivityItem } from '../../types/voiceRoomUi'

interface RoomActivityProps {
  activities: RoomActivityItem[] // 当前房间动态列表，由页面根据连接和成员状态生成。
}

// RoomActivity 展示房间里发生了什么，让等待、加入、静音等状态有明确反馈。
export default function RoomActivity({ activities }: RoomActivityProps) {
  return (
    <section className="side-card room-activity" aria-labelledby="room-activity-title">
      <div className="side-heading">
        <h2 id="room-activity-title">房间动态</h2>
      </div>

      <div className="activity-list">
        {activities.map(activity => (
          <div className="activity-row" key={activity.id}>
            <span className="activity-icon">·</span>
            <div>
              <strong>{activity.title}</strong>
              <em>{activity.detail}</em>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}
