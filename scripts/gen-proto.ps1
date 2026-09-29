# 生成 protobuf 与 gRPC 代码，Go 与 Python 两侧一次生成
#
# 用法：powershell -File scripts/gen-proto.ps1
# 依赖：PATH 中有 protoc、protoc-gen-go、protoc-gen-go-grpc；python 装好 grpcio-tools
# 说明：Go 侧靠 proto 里的 go_package 决定输出目录（backend/proto/<包名>），
#       Python 侧统一输出到 agent/pb，并把生成的绝对 import 改成相对 import，
#       以便按 agent.pb 包路径引用
$ErrorActionPreference = "Stop"

# 参与生成的 proto 文件名，新增 proto 时在此追加
$protos = @("llm.proto")

$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    foreach ($p in $protos) {
        Write-Host "[go]     $p"
        protoc -I proto `
            --go_out=. --go_opt=paths=import `
            --go-grpc_out=. --go-grpc_opt=paths=import `
            "proto/$p"
        if ($LASTEXITCODE -ne 0) { throw "protoc 生成 Go 代码失败: $p" }

        Write-Host "[python] $p"
        python -m grpc_tools.protoc -I proto `
            --python_out=agent/pb --grpc_python_out=agent/pb `
            "proto/$p"
        if ($LASTEXITCODE -ne 0) { throw "protoc 生成 Python 代码失败: $p" }
    }

    # grpc_tools 生成的是绝对 import（import llm_pb2 as ...），
    # 本项目按 agent.pb 包路径导入，改成相对 import 才能正常工作
    Get-ChildItem agent/pb/*_pb2_grpc.py | ForEach-Object {
        $text = Get-Content $_.FullName -Raw
        $patched = $text -replace "(?m)^import (\w+_pb2) as (\w+)$", 'from . import $1 as $2'
        if ($patched -ne $text) {
            Set-Content -Path $_.FullName -Value $patched -NoNewline -Encoding utf8
            Write-Host "[patch]  $($_.Name) 相对 import 已修正"
        }
    }

    Write-Host "生成完成"
}
finally {
    Pop-Location
}
