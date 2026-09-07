import { useState } from 'react'
import { useNavigate } from 'react-router-dom'

// createRoomCode 生成 6 位大写频道号，作为新频道的默认编号
function createRoomCode() {
  return Math.random().toString(36).slice(2, 8).toUpperCase()
}

// ChannelsTab 是主界面「频道」页：搜索(UI) + 创建/加入频道 + 热门标签(静态) + 频道列表空态
// 频道列表/搜索/热门的取数接口后端尚未实现，均以 UI 结构 + 空态呈现，不 mock 假数据
export default function ChannelsTab() {
  const navigate = useNavigate()
  const [joinCode, setJoinCode] = useState('') // 「输入频道号加入」输入的值
  const [searchText, setSearchText] = useState('') // 搜索框文本，当前仅作结构，未接搜索接口

  const handleCreate = () => {
    navigate(`/channel/${createRoomCode()}`)
  }

  const handleJoin = () => {
    const code = joinCode.trim().toUpperCase()
    if (!code) return
    navigate(`/channel/${code}`)
  }

  return (
    <div className="channels-tab">
      <label className="search-bar">
        <span className="si">🔍</span>
        <input
          value={searchText}
          onChange={event => setSearchText(event.target.value)}
          placeholder="搜索频道或频道号…"
          aria-label="搜索频道或频道号"
        />
      </label>

      <button type="button" className="create-banner" onClick={handleCreate}>
        <span className="cb-ic">＋</span>
        <span className="cb-txt">
          <b>创建频道</b>
          <span>开一个房间，喊朋友们进来</span>
        </span>
      </button>

      <div className="join-card">
        <input
          value={joinCode}
          onChange={event => setJoinCode(event.target.value)}
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

      <div className="sec-title">热门标签</div>
      <div className="taglist">
        {['𝄞 音乐', '🎮 游戏', '🫖 闲谈', '📚 学习', '🌙 深夜电台'].map(tag => (
          <span className="chip-tag" key={tag}>
            {tag}
          </span>
        ))}
      </div>

      <div className="sec-title">热门频道</div>
      <div className="room-list">
        <div className="empty-state">
          <span className="es-ic">📡</span>
          <b>频道列表还没接好</b>
          <p>等朋友创建频道或你先开一个，这里就会慢慢热闹起来</p>
        </div>
      </div>
    </div>
  )
}