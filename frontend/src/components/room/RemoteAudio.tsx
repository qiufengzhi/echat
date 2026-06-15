import { useCallback, useEffect, useRef } from 'react'

interface RemoteAudioProps {
  stream: MediaStream | null // 远端成员传来的音频流，没有远端声音时为 null。
  isSpeakerOn: boolean // 是否播放远端声音，关闭时 audio 会静音但不影响自己发声。
}

// RemoteAudio 是隐藏的音频播放器，页面上的"扬声器"按钮实际通过 muted 控制它。
export default function RemoteAudio({ stream, isSpeakerOn }: RemoteAudioProps) {
  const audioRef = useRef<HTMLAudioElement>(null)

  // 把 srcObject 挂到 audio 上，并主动调用 play()。
  // 浏览器自动播放策略会阻止动态创建的 audio 元素在非静音状态下自动播放，
  // 仅靠 autoPlay 属性不够，必须显式调用 play() 并在失败时静音兜底。
  const attachStream = useCallback(() => {
    const audio = audioRef.current
    if (!audio) return

    audio.srcObject = stream
    if (!stream) return

    const playAttempt = audio.play()
    if (playAttempt !== undefined) {
      playAttempt.catch(err => {
        // 浏览器因自动播放策略阻止了播放时，先静音再重试一次。
        // 用户下次点击页面时会自动取消静音并恢复播放。
        if (err.name === 'NotAllowedError') {
          console.warn('[RemoteAudio] 自动播放被浏览器阻止，已静音兜底:', stream.id)
          audio.muted = true
          audio.play().catch(() => {
            // 静音状态下仍失败则放弃，等待用户交互恢复。
          })
        }
      })
    }
  }, [stream])

  useEffect(() => {
    attachStream()
  }, [attachStream])

  // 保持 audio.muted 和页面扬声器开关同步。
  // 注意：这里用 ref 直接设置属性而不是依赖 React 重新渲染，避免 srcObject 被重置。
  useEffect(() => {
    if (audioRef.current) {
      audioRef.current.muted = !isSpeakerOn
    }
  }, [isSpeakerOn])

  return <audio ref={audioRef} playsInline />
}
