import argparse
import signal
import sys
import threading
from pathlib import Path
from concurrent import futures

import grpc
import numpy as np
import torch

# silero-vad 本地包路径（从 wheel 提取，避免网络下载）
_VAD_PKG_DIR = Path(__file__).resolve().parent.parent / 'silero_vad_pkg'
if str(_VAD_PKG_DIR) not in sys.path:
    sys.path.insert(0, str(_VAD_PKG_DIR))

from silero_vad.utils_vad import init_jit_model, get_speech_timestamps as _get_speech_ts
from vad.pb import vad_pb2_grpc, vad_pb2


class VadService(vad_pb2_grpc.VadServiceServicer):
    """VAD gRPC 服务实现"""

    def __init__(self) -> None:
        # 加载 silero-vad JIT 模型
        model_path = str(_VAD_PKG_DIR / 'silero_vad' / 'data' / 'silero_vad.jit')
        self._model = init_jit_model(model_path)
        self._get_speech_timestamps = _get_speech_ts

    def VadDetect(self, request, context):
        """语音活动检测：输入 PCM 数据，返回是否含有人声"""
        # 输入校验
        if not request.pcm or len(list(request.pcm)) == 0:
            return vad_pb2.VadResponse(speech=False, score=0.0)

        # PCM 预处理：byte → int16 → float32 [-1, 1]
        audio = np.frombuffer(request.pcm, dtype=np.int16).astype(np.float32) / 32768.0

        # VAD 检测
        with torch.no_grad():
            speech_timestamps = self._get_speech_timestamps(
                torch.tensor(audio, dtype=torch.float32),
                self._model,
                sampling_rate=request.sample_rate,
                threshold=0.5,
                min_speech_duration_ms=250,
                max_speech_duration_s=float('inf'),
                min_silence_duration_ms=100,
                window_size_samples=1536,
                speech_pad_ms=30,
            )

        # 返回结果
        speech = bool(speech_timestamps)
        score = 1.0 if speech else 0.0
        return vad_pb2.VadResponse(speech=speech, score=score)


def serve(host: str = '0.0.0.0', port: int = 50052) -> None:
    """启动 VAD gRPC 服务"""
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    vad_pb2_grpc.add_VadServiceServicer_to_server(VadService(), server)
    server.add_insecure_port(f'{host}:{port}')
    server.start()
    print(f'VAD gRPC server listening on {host}:{port}', flush=True)
    # wait_for_termination() 在某些 grpcio 版本不阻塞，改用 signal 兜底
    done = threading.Event()
    signal.signal(signal.SIGINT, lambda *_: done.set())
    signal.signal(signal.SIGTERM, lambda *_: done.set())
    done.wait()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='启动 silero-vad1 的 gRPC 服务')
    parser.add_argument('--host', default='0.0.0.0', help='绑定地址 (默认: 0.0.0.0)')
    parser.add_argument('--port', type=int, default=50052, help='监听端口 (默认: 50052)')
    args = parser.parse_args()
    serve(args.host, args.port)