"""Python 侧 span attribute 常量，集中定义不散写裸字符串"""

from __future__ import annotations

# 跨系统 join key
ATTR_TURN_ID = "echat.turn_id"  # 一轮对话唯一 ID（运行时经 TurnRequest 带入）
ATTR_SESSION_ID = "echat.session_id"  # 会话 ID
ATTR_ROOM_ID = "echat.room_id"  # 房间短码

# GenAI 语义约定（`https://opentelemetry.io/docs/specs/semconv/gen-ai/`，逐字对齐）
ATTR_GEN_AI_REQUEST_MODEL = "gen_ai.request.model"  # 请求使用的模型名
ATTR_GEN_AI_PROVIDER_NAME = "gen_ai.provider.name"  # 提供商名，如 deepseek
ATTR_GEN_AI_REQUEST_MAX_TOKENS = "gen_ai.request.max_tokens"  # 最大 token 数
ATTR_GEN_AI_REQUEST_TEMPERATURE = "gen_ai.request.temperature"  # 采样温度
ATTR_GEN_AI_REQUEST_STREAM = "gen_ai.request.stream"  # 是否流式
ATTR_GEN_AI_RESPONSE_FINISH_REASONS = "gen_ai.response.finish_reasons"  # 结束原因列表
ATTR_GEN_AI_USAGE_INPUT_TOKENS = "gen_ai.usage.input_tokens"  # 输入 token 数
ATTR_GEN_AI_USAGE_OUTPUT_TOKENS = "gen_ai.usage.output_tokens"  # 输出 token 数

# PII 属性：默认不写入，capture_content=true（仅本机 dev）才承载对话内容
ATTR_PII_USER_TEXT = "echat.pii.user_text"
ATTR_PII_MESSAGES = "echat.pii.messages"