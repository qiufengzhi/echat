# proto 与代码生成

本目录存放 Go 后端与 Python Agent 共用的 proto 定义。

## 生成

```powershell
powershell -File scripts/gen-proto.ps1
```

依赖：

- `protoc`、`protoc-gen-go`、`protoc-gen-go-grpc` 在 PATH 中
- Python 环境装有 `grpcio-tools`（`python -m grpc_tools.protoc` 可用）

## 产物位置

| 侧 | 位置 | 决定因素 |
|---|---|---|
| Go | `backend/proto/<包名>/` | proto 里的 `option go_package` |
| Python | `agent/pb/` | 脚本固定输出目录 |

`go_package` 写 `backend/proto/llm` 这类模块内相对路径，脚本以仓库根为输出目录、`paths=import` 生成，产物落在 `backend/proto/llm/llm.pb.go` 与 `llm_grpc.pb.go`。

Python 侧 `grpc_tools` 生成的是绝对 import（`import llm_pb2 as llm__pb2`），脚本会改成相对 import（`from . import llm_pb2 as llm__pb2`），这样才能按 `agent.pb` 包路径引用。

## 约定

- 新增 proto 时，把文件名加进 `scripts/gen-proto.ps1` 的 `$protos`
- 每个字段、每个枚举成员都要写注释，注释末尾不带标点
- 生成物随代码入库，改动 proto 后必须重新生成并提交
