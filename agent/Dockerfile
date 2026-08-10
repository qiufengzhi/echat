# =============================================================================
# 构建阶段：gcc 用于编译 grpcio 的原生扩展
# =============================================================================
FROM python:3.12-slim AS builder

RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc \
    && rm -rf /var/lib/apt/lists/*

# 创建独立虚拟环境，方便整体搬运到运行时镜像
RUN python -m venv /opt/venv
ENV PATH="/opt/venv/bin:$PATH"

WORKDIR /build
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

# =============================================================================
# 运行阶段：不含 gcc，镜像更小更安全
# =============================================================================
FROM python:3.12-slim AS runtime

# 从构建阶段搬运完整的虚拟环境
COPY --from=builder /opt/venv /opt/venv
ENV PATH="/opt/venv/bin:$PATH"

WORKDIR /app
COPY . ./agent/

EXPOSE 50053
EXPOSE 40001
CMD ["python", "-m", "agent.cmd.main", "--config", "agent/config.yaml"]
