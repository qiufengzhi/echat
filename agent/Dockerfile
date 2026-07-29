# Agent gRPC 服务（LLM 对话代理）
FROM python:3.12-slim

WORKDIR /app

# 安装系统依赖（grpcio 编译需要）
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    && rm -rf /var/lib/apt/lists/*

# 安装 Python 依赖
COPY agent/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 复制 agent 源码（pb 目录包含预生成的 protobuf 桩代码，无需 proto 编译器）
COPY agent/ agent/

EXPOSE 50053

# 密钥通过环境变量注入（优先级高于 config.yaml），见 agent/cmd/main.py 的 Config 加载顺序
CMD ["python", "-m", "agent.cmd.main", "--config", "agent/config.yaml"]
