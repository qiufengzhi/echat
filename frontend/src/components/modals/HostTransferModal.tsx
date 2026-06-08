import { useEffect, useState } from 'react'

import BaseModal from './BaseModal'

interface HostTransferCandidate {
  id: string
  name: string
}

interface HostTransferModalProps {
  isOpen: boolean
  candidates: HostTransferCandidate[]
  onClose: () => void
  onConfirm: (nextHostId: string) => void
  onRandom: () => void
}

export default function HostTransferModal({
  isOpen,
  candidates,
  onClose,
  onConfirm,
  onRandom,
}: HostTransferModalProps) {
  const [selectedId, setSelectedId] = useState('')

  useEffect(() => {
    if (!isOpen) {
      setSelectedId('')
      return
    }

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

      <div className="host-transfer-actions">
        <button className="secondary-button" type="button" onClick={onRandom}>
          直接离开，自动选择
        </button>
        <button
          className="primary-button"
          type="button"
          disabled={!selectedId}
          onClick={() => onConfirm(selectedId)}
        >
          指定后离开
        </button>
      </div>
    </BaseModal>
  )
}
