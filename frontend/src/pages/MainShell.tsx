import { useState } from 'react'

import ChannelsTab from './tabs/ChannelsTab'
import ChatsTab from './tabs/ChatsTab'
import MeTab from './tabs/MeTab'

// TabKey 是主界面底部三个 Tab 的枚举，进入主界面默认落在频道
type TabKey = 'channels' | 'chats' | 'me'

// MAIN_TAB_KEY 是当前 Tab 在 sessionStorage 的键，子页返回 `/` 时用于恢复上次所在 Tab
const MAIN_TAB_KEY = 'echat.mainTab'

// readSavedTab 读取上次保存的 Tab，非法值回退到频道
function readSavedTab(): TabKey {
  const saved = sessionStorage.getItem(MAIN_TAB_KEY)
  return saved === 'chats' || saved === 'me' ? saved : 'channels'
}

// MainShell 是登录后落地的移动壳主界面：单个内容面板 + 底部三 Tab 切换
// 频道为默认 Tab，聊天页产品层面不做文本会话，我的页展示登录用户资料
export default function MainShell() {
  const [tab, setTab] = useState<TabKey>(readSavedTab)

  const switchTab = (next: TabKey) => {
    setTab(next)
    sessionStorage.setItem(MAIN_TAB_KEY, next)
  }

  return (
    <div className="main-shell">
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
            onClick={() => switchTab(key)}
          >
            <span className="bi">{icon}</span>
            <span className="bl">{label}</span>
          </button>
        ))}
      </nav>
    </div>
  )
}