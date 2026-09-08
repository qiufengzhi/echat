import { useState } from 'react'

// ChatSeg 是「聊天」页的分段维度：消息与会话分开展示
type ChatSeg = 'messages' | 'friends'

// ChatsTab 是主界面「聊天」页：消息/好友分段切换，产品不做文本/好友持久化，两段均展示空态
export default function ChatsTab() {
  const [seg, setSeg] = useState<ChatSeg>('messages')

  return (
    <div className="chats-tab">
      <div className="chat-seg">
        <button
          type="button"
          className={seg === 'messages' ? 'active' : ''}
          onClick={() => setSeg('messages')}
        >
          消息
        </button>
        <button
          type="button"
          className={seg === 'friends' ? 'active' : ''}
          onClick={() => setSeg('friends')}
        >
          好友
        </button>
      </div>

      {seg === 'messages' ? (
        <div className="empty-state">
          <span className="es-ic">💬</span>
          <b>还没有消息</b>
        </div>
      ) : (
        <div className="empty-state">
          <span className="es-ic">👥</span>
          <b>还没有好友</b>
        </div>
      )}
    </div>
  )
}