import { useMemo, useState } from 'react'

import BaseModal from './BaseModal'

interface InviteModalProps {
  isOpen: boolean // 邀请弹窗是否显示。
  roomId: string // 当前房间号，用于展示和复制。
  hostName: string // 房主昵称，用于告诉用户是谁创建了房间。
  memberCount: number // 当前在线成员数量。
  onClose: () => void // 关闭邀请弹窗。
}

// InviteModal 负责把当前房间的链接和房间号交给用户，复制成功后给出轻量反馈。
export default function InviteModal({ isOpen, roomId, hostName, memberCount, onClose }: InviteModalProps) {
  const [copyText, setCopyText] = useState('复制')

  const inviteLink = useMemo(() => {
    if (typeof window === 'undefined') return roomId

    const url = new URL(window.location.href)
    url.searchParams.set('room', roomId)
    return url.toString()
  }, [roomId])

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(inviteLink)
      setCopyText('已复制')
      window.setTimeout(() => setCopyText('复制'), 1600)
    } catch {
      setCopyText('复制失败')
      window.setTimeout(() => setCopyText('复制'), 1600)
    }
  }

  return (
    <BaseModal
      isOpen={isOpen}
      title="邀请好友加入"
      description="复制链接，朋友打开就能进来"
      closeLabel="关闭邀请"
      onClose={onClose}
    >
      <div className="invite-link-row">
        <span>{inviteLink}</span>
        <button className="copy-button" type="button" onClick={handleCopy}>
          {copyText}
        </button>
      </div>

      <div className="invite-options">
        <div className="invite-option">
          <strong>房间号</strong>
          <span>{roomId}</span>
        </div>
        <div className="invite-option">
          <strong>房主</strong>
          <span>{hostName || '待确认'}</span>
        </div>
        <div className="invite-option">
          <strong>当前人数</strong>
          <span>{memberCount} 人在线</span>
        </div>
      </div>

      <p className="modal-footnote">也可以只告诉朋友房间号</p>
    </BaseModal>
  )
}
