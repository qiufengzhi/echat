import { useState } from 'react'
import { useNavigate } from 'react-router-dom'

import { roomExists } from '../../services/rooms'

// createRoomCode 生成 6 位大写频道号，作为新频道的默认编号
function createRoomCode() {
  return Math.random().toString(36).slice(2, 8).toUpperCase()
}

// ROOM_CARDS / ROOM_TAGS 复刻设计稿示例行的静态展示
// 后端房间列表/搜索/热门接口未开放，此处只还原设计稿结构与占位行，接入后替换为真实数据
const ROOM_TAGS = ['𝄞 音乐', '🎮 游戏', '🫖 闲谈', '📚 学习', '🌙 深夜电台']
const ROOM_CARDS = [
  { thumb: '🎤', bg: 'linear-gradient(150deg, #FFB7C5, #FB7299)', title: '和朋友的夜聊', online: '3 人在线', tag: '#音乐' },
  { thumb: '🎮', bg: 'linear-gradient(150deg, #9BD8F5, #23ADE5)', title: '周末开黑语音', online: '12 人在线', tag: '#游戏' },
  { thumb: '🌙', bg: 'linear-gradient(150deg, #D9C8F8, #A98FF0)', title: '深夜电台·助眠', online: '6 人在线', tag: '#深夜电台' },
]

// ChannelsTab 是主界面「频道」页：创建/加入频道 + 热门标签 + 大家都在开列表
export default function ChannelsTab() {
  const navigate = useNavigate()
  const [joinCode, setJoinCode] = useState('') // 「输入频道号加入」输入的值
  const [joinError, setJoinError] = useState<string | null>(null) // 加入校验提示（频道不存在/查询失败）
  const [activeTag, setActiveTag] = useState(ROOM_TAGS[0]) // 高亮的频道标签，默认第一个

  const handleCreate = () => {
    navigate(`/channel/${createRoomCode()}`)
  }

  // handleJoin 先向后端确认频道号当前有在线房间，不存在则提示而不进房，避免误建新频道
  const handleJoin = async () => {
    const code = joinCode.trim().toUpperCase()
    if (!code) return
    const exists = await roomExists(code)
    if (exists === null) {
      setJoinError('查询失败，请稍后重试')
      return
    }
    if (!exists) {
      setJoinError('频道不存在')
      return
    }
    setJoinError(null)
    navigate(`/channel/${code}`)
  }

  return (
    <div className="channels-tab">
      <button type="button" className="create-banner" onClick={handleCreate}>
        <span className="cb-ic">＋</span>
        <span className="cb-txt">
          <b>创建频道</b>
        </span>
      </button>

      <div className="join-card">
        <input
          value={joinCode}
          onChange={event => {
            setJoinCode(event.target.value)
            setJoinError(null)
          }}
          onKeyDown={event => {
            if (event.key === 'Enter') handleJoin()
          }}
          placeholder="输入频道号加入"
          maxLength={12}
          aria-label="输入频道号加入"
        />
        <button type="button" onClick={handleJoin}>
          加入
        </button>
      </div>
      {joinError && <p className="join-error">{joinError}</p>}

      <div className="sec-title">
        热门频道
        <span className="more">查看全部 ›</span>
      </div>
      <div className="taglist">
        {ROOM_TAGS.map(tag => (
          <button
            type="button"
            className={`chip-tag ${tag === activeTag ? 'on' : ''}`}
            key={tag}
            onClick={() => setActiveTag(tag)}
          >
            {tag}
          </button>
        ))}
      </div>

      <div className="sec-title">大家都在开</div>
      <div className="room-list">
        {ROOM_CARDS.map(card => (
          <div className="room-card" key={card.title}>
            <div className="rc-thumb" style={{ background: card.bg }}>
              {card.thumb}
            </div>
            <div className="rc-body">
              <div className="rc-title">{card.title}</div>
              <div className="rc-meta">
                <span className="rc-on">
                  <i />
                  {card.online}
                </span>
                <span>{card.tag}</span>
              </div>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}