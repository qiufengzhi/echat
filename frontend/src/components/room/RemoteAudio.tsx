import { useEffect, useRef } from 'react'

interface RemoteAudioProps {
  stream: MediaStream | null // 远端成员传来的音频流，没有远端声音时为 null
  isSpeakerOn: boolean // 是否播放远端声音，关闭时 audio 会静音但不影响自己发声
}

// RemoteAudio 是隐藏的音频播放器，页面上的"扬声器"按钮实际通过 muted 控制它
export default function RemoteAudio({ stream, isSpeakerOn }: RemoteAudioProps) {
  const audioRef = useRef<HTMLAudioElement>(null)
  // 用 ref 镜像最新扬声器开关，供一次性手势回调读取，避免闭包读到过期的 isSpeakerOn
  const speakerOnRef = useRef(isSpeakerOn)

  // 保持 audio.muted 与页面扬声器开关同步
  // 注意：这里用 ref 直接设置属性而不是依赖 React 重新渲染，避免 srcObject 被重置
  useEffect(() => {
    speakerOnRef.current = isSpeakerOn
    if (audioRef.current) {
      audioRef.current.muted = !isSpeakerOn
    }
  }, [isSpeakerOn])

  // 挂载远端流并尝试播放；被浏览器自动播放策略拦截时，登记一次性用户手势回调，
  // 待首次点击/按键后按当前开关重试，使「进房默认开扬声器」在深链直接进入（无前置点击）时也能出声
  useEffect(() => {
    const audio = audioRef.current
    if (!audio) return
    audio.srcObject = stream
    if (!stream) return

    // 手势恢复：播放前按最新开关对齐 muted，成功后移除监听，保证只触发一次
    const resume = () => {
      document.removeEventListener('pointerdown', resume)
      document.removeEventListener('keydown', resume)
      audio.muted = !speakerOnRef.current
      audio.play().catch(() => {})
    }

    audio.muted = !speakerOnRef.current
    const playAttempt = audio.play()
    if (playAttempt !== undefined) {
      playAttempt.catch(err => {
        // 浏览器因自动播放策略阻止了播放时，等用户首次交互后恢复，而不是永久静音
        if (err.name === 'NotAllowedError') {
          console.warn('[RemoteAudio] 自动播放被浏览器阻止，等待用户手势后恢复:', stream.id)
          document.addEventListener('pointerdown', resume)
          document.addEventListener('keydown', resume)
        }
      })
    }

    // 流切换或卸载时移除监听，避免残留回调操作已失效的 audio
    return () => {
      document.removeEventListener('pointerdown', resume)
      document.removeEventListener('keydown', resume)
    }
  }, [stream])

  return <audio ref={audioRef} playsInline />
}
