import { useEffect, useRef } from 'react'

interface RemoteAudioProps {
  stream: MediaStream | null // 远端成员传来的音频流，没有远端声音时为 null。
  isSpeakerOn: boolean // 是否播放远端声音，关闭时 audio 会静音但不影响自己发声。
}

// RemoteAudio 是隐藏的音频播放器，页面上的“扬声器”按钮实际通过 muted 控制它。
export default function RemoteAudio({ stream, isSpeakerOn }: RemoteAudioProps) {
  const audioRef = useRef<HTMLAudioElement>(null)

  useEffect(() => {
    if (!audioRef.current) return

    audioRef.current.srcObject = stream
  }, [stream])

  return <audio ref={audioRef} autoPlay playsInline muted={!isSpeakerOn} />
}
