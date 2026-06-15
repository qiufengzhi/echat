import type { SignalingMessage } from '../types/signaling'

// WebRTCSignalingHandlerOptions 描述 SFU 信令处理所需的回调集合。
// 与 P2P 架构不同，SFU 模式下：
//   - 前端只需一条与 SFU 服务端的 PeerConnection
//   - 客户端发起 SDP Offer，服务端回复 Answer
//   - 远端音轨通过 ontrack 按用户分组，每个用户对应一个 MediaStream
//   - 用户离开时只移除该用户的远端流，不影响其他成员
export interface WebRTCSignalingHandlerOptions {
  getPeerConnection: () => RTCPeerConnection | null // 获取当前与 SFU 服务端的 WebRTC 连接。
  setRemoteStream: (userId: string, stream: MediaStream) => void // 添加或更新某用户的远端音频流。
  removeRemoteStream: (userId: string) => void // 移除某用户的远端音频流（该用户已离开）。
  sendSFUOffer: (offer: RTCSessionDescriptionInit) => void // 把本端 SDP Offer 发给 SFU 服务端。
  sendSFUIce: (candidate: RTCIceCandidate) => void // 把 ICE Candidate 发给 SFU 服务端。
  setConnected: (connected: boolean) => void // 更新页面上的连接状态。
  setError: (message: string) => void // 更新页面上的错误提示。
  serverErrorMessage: string // 服务端错误消息缺失时使用的兜底文案。
}

// handleWebRTCSignaling 把 SFU 信令消息转换成具体的 WebRTC 操作。
//
// 信令流程：
//   1. 客户端创建 Offer → 通过 sfu_offer 发送给服务端
//   2. 服务端返回 sfu_answer  → 前端设为远端描述，完成 SDP 协商
//   3. 双向 sfu_ice         → 添加到 RTCPeerConnection
//   4. 浏览器触发 ontrack   → 解析 track label 获取用户 ID，存储远端流
//   5. 用户离开 user_left   → 用 payload.user_id 移除对应的远端流和席位
export async function handleWebRTCSignaling(
  data: SignalingMessage,
  options: WebRTCSignalingHandlerOptions,
): Promise<void> {
  const pc = options.getPeerConnection()

  switch (data.type) {
    case 'sfu_answer': {
      console.log('[sfu] 收到 SFU Answer，正在完成 SDP 协商...')
      if (!pc) {
        console.warn('[sfu] 没有可用的 PeerConnection 处理 sfu_answer')
        return
      }
      // SFU 服务端对客户端 Offer 回复的 Answer，设为远端描述后 SDP 协商完成。
      const answerPayload = data.payload as { sdp: string; type: string }
      await pc.setRemoteDescription(new RTCSessionDescription({ sdp: answerPayload.sdp, type: 'answer' }))
      break
    }

    case 'sfu_ice': {
      console.log('[sfu] 收到来自 SFU 的 ICE Candidate')
      if (!pc) {
        console.warn('[sfu] 没有可用的 PeerConnection 处理 sfu_ice')
        return
      }
      // SFU 服务端发来的 ICE Candidate，加入本端连接以完成链路建立。
      const icePayload = data.payload as RTCIceCandidateInit
      try {
        await pc.addIceCandidate(new RTCIceCandidate(icePayload))
      } catch (err) {
        // ICE candidate 添加失败通常不影响整体连接，浏览器会自动尝试其他候选。
        console.warn('[sfu] 添加 ICE candidate 失败:', err)
      }
      break
    }

    case 'user_left': {
      // 远端用户离开时，移出该用户的远端音频流，其他用户的流不受影响。
      const userId = (data.payload as { user_id?: string } | undefined)?.user_id
      if (userId) {
        console.log('[sfu] 远端用户离开，移除音频流:', userId)
        options.removeRemoteStream(userId)
      }
      break
    }

    case 'error':
      options.setError((data.payload as { message?: string } | undefined)?.message || options.serverErrorMessage)
      break
  }
}
