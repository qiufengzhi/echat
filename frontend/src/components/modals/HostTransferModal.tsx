import { useEffect, useState } from 'react'

import BaseModal from './BaseModal'

// HostTransferCandidate 表示可以接任房主的在线成员。
interface HostTransferCandidate {
  id: string // 成员 ID，会作为 next_host_id 发给服务端。
  name: string // 成员昵称，用于弹窗单选项展示。
}

// HostTransferModalProps 描述房主离开前交接弹窗需要的状态和操作。
interface HostTransferModalProps {
  isOpen: boolean // 弹窗是否显示。
  candidates: HostTransferCandidate[] // 可接任房主的在线成员，不包含当前房主。
  onClose: () => void // 关闭弹窗，不离开房间。
  onConfirm: (nextHostId: string) => void // 指定下一任房主后离开房间。
  onRandom: () => void // 不指定人选，直接离开并让服务端自动选择下一任房主。
}

// HostTransferModal 在房主离开且房间还有其他成员时出现，避免房主身份静默丢失。
export default function HostTransferModal({
  isOpen,
  candidates,
  onClose,
  onConfirm,
  onRandom,
}: HostTransferModalProps) {
  // selectedId 保存当前选中的下一任房主 ID；弹窗打开时默认选第一位候选人。
  const [selectedId, setSelectedId] = useState('')

  useEffect(() => {
    if (!isOpen) {
      // 关闭后清空选择，下一次打开时重新根据最新成员列表默认选择。
      setSelectedId('')
      return
    }

    // 候选列表可能随着成员离开变化，因此每次打开或候选更新时都重新兜底。
    setSelectedId(candidates[0]?.id || '')
  }, [candidates, isOpen])

  return (
    <BaseModal
      isOpen={isOpen}
      title="离开前交接房主"
      description="你可以先把房主交给一位在线成员，也可以直接离开，让房间自动选出下一位房主。"
      closeLabel="关闭房主交接"
      onClose={onClose}
    >
      <div className="host-transfer-options">
        {candidates.map(candidate => (
          // 单选项只负责选择下一任房主，真正的离开动作由下方确认按钮触发。
          <label className="host-transfer-option" key={candidate.id}>
            <input
              type="radio"
              name="next-host"
              value={candidate.id}
              checked={selectedId === candidate.id}
              onChange={event => setSelectedId(event.target.value)}
            />
            <span>{candidate.name}</span>
          </label>
        ))}
      </div>

      {/* 两个离开按钮分别对应“服务端自动选择”和“携带 next_host_id 指定交接”。 */}
      <div className="host-transfer-actions">
        <button className="secondary-button" type="button" onClick={onRandom}>
          直接离开
        </button>
        <button
          className="primary-button"
          type="button"
          disabled={!selectedId}
          onClick={() => onConfirm(selectedId)}
        >
          指定房主
        </button>
      </div>
    </BaseModal>
  )
}
