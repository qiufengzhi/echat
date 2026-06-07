import type { SignalingErrorPayload, SignalingMessage } from '../types/signaling'

// WebRTCSignalingHandlerOptions 描述处理信令消息时需要由 useVoiceRoom 提供的能力。
export interface WebRTCSignalingHandlerOptions {
  getPeerConnection: () => RTCPeerConnection | null // 获取当前正在使用的 WebRTC 连接。
  clearPeerConnection: () => void // 清空当前 WebRTC 连接引用。
  createPeerConnection: () => RTCPeerConnection // 在对端离开后创建新的 WebRTC 连接。
  sendOffer: (offer: RTCSessionDescriptionInit) => void // 把本端 offer 通过信令服务器发给对端。
  sendAnswer: (answer: RTCSessionDescriptionInit) => void // 把本端 answer 通过信令服务器发给对端。
  setConnected: (connected: boolean) => void // 更新页面上的 WebRTC 连接状态。
  clearRemoteStream: () => void // 清空页面上的远端音频流。
  setError: (message: string) => void // 更新页面上的错误提示。
  serverErrorMessage: string // 服务端错误消息缺失时使用的兜底文案。
}

// handleWebRTCSignaling 把信令服务器消息转换成具体的 WebRTC 操作和页面状态更新。
export async function handleWebRTCSignaling(
  data: SignalingMessage,
  options: WebRTCSignalingHandlerOptions,
): Promise<void> {
  const pc = options.getPeerConnection()
  if (!pc) return

  switch (data.type) {
    case 'waiting':
      console.log('Waiting for another user to join...')
      options.setConnected(false)
      break

    case 'room_ready': {
      console.log('Room is ready, creating offer...')
      // 后进入房间的一方收到 room_ready 后主动创建 offer，作为本次协商的发起方。
      const offer = await pc.createOffer()
      await pc.setLocalDescription(offer)
      options.sendOffer(offer)
      break
    }

    case 'offer': {
      console.log('Received offer, creating answer...')
      // 先保存对端 offer，再创建自己的 answer 回传，双方的媒体参数才能对齐。
      await pc.setRemoteDescription(new RTCSessionDescription(data.payload as RTCSessionDescriptionInit))
      const answer = await pc.createAnswer()
      await pc.setLocalDescription(answer)
      options.sendAnswer(answer)
      break
    }

    case 'answer':
      console.log('Received answer, completing connection...')
      // 发起方收到 answer 后保存远端描述，至此 SDP 协商完成，随后等待 ICE 连通。
      await pc.setRemoteDescription(new RTCSessionDescription(data.payload as RTCSessionDescriptionInit))
      break

    case 'ice':
      console.log('Received ICE candidate')
      // 收到对端候选地址后交给 RTCPeerConnection，浏览器会自动尝试建立可用链路。
      await pc.addIceCandidate(new RTCIceCandidate(data.payload as RTCIceCandidateInit))
      break

    case 'user_left':
      console.log('Remote user left room')
      // 对方离开后清空远端音频，并重建一个新的 PeerConnection 等待下一位用户加入。
      options.setConnected(false)
      options.clearRemoteStream()
      pc.close()
      options.clearPeerConnection()
      options.createPeerConnection()
      break

    case 'error':
      options.setError((data.payload as SignalingErrorPayload | undefined)?.message || options.serverErrorMessage)
      break
  }
}
