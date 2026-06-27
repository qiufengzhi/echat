import argparse
import os
import sys
import threading
from concurrent import futures
from pathlib import Path
from typing import Dict

import grpc
import numpy as np
from funasr import AutoModel

PROJECT_ROOT = Path(__file__).resolve().parents[2]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from asr.pb import asr_pb2, asr_pb2_grpc

DEFAULT_MODEL = os.getenv(
    "ASR_MODEL",
    r"D:\ai\asr\damo\speech_paraformer-large_asr_nat-zh-cn-16k-common-vocab8404-pytorch",
)
DEFAULT_PUNC_MODEL = os.getenv(
    "ASR_PUNC_MODEL",
    r"D:\ai\asr\iic\punc_ct-transformer_zh-cn-common-vocab272727-pytorch",
)


class AsrService(asr_pb2_grpc.AsrServiceServicer):
    """提供双向流式语音识别的 gRPC 服务。"""

    def __init__(self, model_path: str, punc_model_path: str) -> None:
        self._model = AutoModel(
            model=model_path,
            punc_model=punc_model_path,
            disable_update=True,
        )
        self._caches: Dict[str, Dict[str, object]] = {}
        self._lock = threading.Lock()

    def _get_cache(self, session_id: str) -> Dict[str, object]:
        with self._lock:
            if session_id not in self._caches:
                self._caches[session_id] = {}
            return self._caches[session_id]

    def RecognizeAudioStream(self, request_iterator, context):
        """为每个会话持续接收音频块，并把识别结果流式回传。"""
        for request in request_iterator:
            if not request.pcm:
                continue

            pcm = np.frombuffer(request.pcm, dtype=np.int16).astype(np.float32) / 32768.0
            cache = self._get_cache(request.session_id)
            res = self._model.generate(
                input=pcm,
                cache=cache,
                is_final=request.is_last,
            )
            print(f"[asr] res={res} pcm_len={len(request.pcm)} is_last={request.is_last}", flush=True)

            if res and res[0].get("text"):
                yield asr_pb2.TranscriptAudioChunk(
                    session_id=request.session_id,
                    room_id=request.room_id,
                    client_id=request.client_id,
                    text=res[0]["text"],
                    is_final=request.is_last,
                    seq=request.seq,
                )


def serve(host: str = "0.0.0.0", port: int = 50051) -> None:
    """启动 gRPC 服务。"""
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    asr_pb2_grpc.add_AsrServiceServicer_to_server(
        AsrService(DEFAULT_MODEL, DEFAULT_PUNC_MODEL),
        server,
    )
    server.add_insecure_port(f"{host}:{port}")
    server.start()
    print(f"ASR gRPC server listening on {host}:{port}", flush=True)
    server.wait_for_termination()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="启动 FunASR 的 gRPC 流式服务")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=50051)
    args = parser.parse_args()
    serve(args.host, args.port)
