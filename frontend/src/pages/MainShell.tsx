import { useState } from 'react'

import ChannelsTab from './tabs/ChannelsTab'
import ChatsTab from './tabs/ChatsTab'
import MeTab from './tabs/MeTab'
import { getAuthUser } from '../services/auth'

// TabKey 是主界面底部三个 Tab 的枚举，进入主界面默认落在频道
type TabKey = 'channels' | 'chats' | 'me'

// MainShell 是登录后落地的移动壳主界面：顶部品牌栏 + 单个内容面板 + 底部三 Tab 切换
// 频道为默认 Tab，聊天页产品层面不做文本会话，我的页展示登录用户资料
export default function MainShell() {
  const [tab, setTab] = useState<TabKey>('channels')
  const user = getAuthUser()
  const avatarText = (user?.display_name || user?.username || '我').trim().slice(0, 1) || '我'

  return (
    <div className="main-shell">
      <header className="main-top">
        <div className="wordmark">
          <span className="d" />
          苍月草
        </div>
        {/* 顶部头像一键跳转「我的」页，点击后切换到底部 Tab */}
        <button
          type="button"
          className="avatar"
          aria-label="去我的页面"
          onClick={() => setTab('me')}
        >
          {avatarText}
        </button>
      </header>

      <div className="tab-panel">
        {tab === 'channels' && <ChannelsTab />}
        {tab === 'chats' && <ChatsTab />}
        {tab === 'me' && <MeTab />}
      </div>

      <nav className="bottom-tabs" aria-label="主功能切换">
        {(
          [
            ['channels', '🏠', '频道'],
            ['chats', '💬', '聊天'],
            ['me', '👤', '我的'],
          ] as [TabKey, string, string][]
        ).map(([key, icon, label]) => (
          <button
            key={key}
            type="button"
            className={`b-tab ${tab === key ? 'active' : ''}`}
            onClick={() => setTab(key)}
          >
            <span className="bi">{icon}</span>
            <span className="bl">{label}</span>
          </button>
        ))}
      </nav>
    </div>
  )
}