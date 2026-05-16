import { useState, useCallback } from 'react'
import { useVoiceRoom } from './hooks/useVoiceRoom'
import Room from './components/Room'

function App() {
  const [roomId, setRoomId] = useState('')
  const [username, setUsername] = useState('')
  const [joined, setJoined] = useState(false)

  const voiceRoom = useVoiceRoom()

  const handleJoin = useCallback(async () => {
    if (!roomId.trim() || !username.trim()) return

    const joinedRoom = await voiceRoom.joinRoom(roomId, username)
    setJoined(joinedRoom)
  }, [roomId, username, voiceRoom])

  const handleLeave = useCallback(() => {
    voiceRoom.leaveRoom()
    setJoined(false)
  }, [voiceRoom])

  const generateRoomId = () => {
    const id = Math.random().toString(36).substring(2, 8).toUpperCase()
    setRoomId(id)
  }

  if (joined) {
    return (
      <div className="app">
        <Room
          roomId={roomId}
          username={username}
          voiceRoom={voiceRoom}
          onLeave={handleLeave}
        />
      </div>
    )
  }

  return (
    <div className="app">
      <header className="header">
        <h1>Voice Room Demo</h1>
        <p>两人实时语音对话</p>
      </header>

      <section className="join-section">
        {voiceRoom.error && (
          <div className="error" style={{ marginBottom: '16px' }}>
            <p>{voiceRoom.error}</p>
          </div>
        )}

        <div className="input-group">
          <label>你的昵称</label>
          <input
            type="text"
            placeholder="给自己起个名字"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            maxLength={20}
          />
        </div>

        <div className="input-group">
          <label>房间号</label>
          <input
            type="text"
            placeholder="输入房间号"
            value={roomId}
            onChange={(e) => setRoomId(e.target.value.toUpperCase())}
            maxLength={10}
          />
        </div>

        <button className="btn btn-primary" onClick={handleJoin} disabled={!roomId || !username}>
          加入房间
        </button>

        <button className="btn" onClick={generateRoomId} style={{ background: '#333' }}>
          随机房间号
        </button>

        <p className="hint">
          提示：打开两个浏览器窗口，使用相同的房间号即可开始语音通话
        </p>
      </section>
    </div>
  )
}

export default App
