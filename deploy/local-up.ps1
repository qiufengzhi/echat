# =============================================================================
# eChat 本地一键脚本：构建镜像 + 启动/管理全容器化本地栈（基础设施 + 三业务服务）
#
# 用法（在仓库根目录执行）：
#   .\deploy\local-up.ps1                  # 一键：构建镜像并启动全部容器（首次使用）
#   .\deploy\local-up.ps1 -OnlyStart       # 已构建过时只启动，跳过重建
#   .\deploy\local-up.ps1 -Service backend # 只操作某个服务（配合 -Logs / -Down 使用）
#   .\deploy\local-up.ps1 -Logs            # 跟踪日志（可加 -Service backend）
#   .\deploy\local-up.ps1 -Down            # 停止并移除容器（保留数据卷）
#   .\deploy\local-up.ps1 -Down -Volumes   # 停止并连数据卷一起删除（重置数据库）
#   .\deploy\local-up.ps1 -Status          # 查看当前状态
#
# 端口冲突时，先停掉本机原生 dev 进程，或用环境变量覆盖（示例）：
#   $env:LOCAL_FRONTEND_PORT = 8091; $env:LOCAL_BACKEND_HTTP = 8092; .\deploy\local-up.ps1
#
# 注意：本脚本含中文，需以 UTF-8(BOM) 编码保存，否则 Windows PowerShell 5.1
# 会按 GBK 误读导致语法错误。若改动后报错请重新转换编码。
# =============================================================================
[CmdletBinding()]
param(
    [switch]$OnlyStart,  # 不重建镜像，仅拉起/复用容器
    [switch]$Down,       # 停止并移除容器
    [switch]$Logs,       # 跟踪日志
    [switch]$Status,     # 查看容器状态
    [switch]$Volumes,    # 与 -Down 搭配：同时删除数据卷
    [string]$Service     # 只针对指定服务操作（如 backend / frontend / agent）
)

$ErrorActionPreference = 'Stop'

# 脚本位于 deploy/ 下，仓库根为其上一级
$root = Split-Path -Parent $PSScriptRoot
$baseFile = Join-Path $root 'deploy/docker-compose.base.yml'
$composeFile = Join-Path $root 'deploy/docker-compose.local.yml'

function Invoke-Compose {
    param([string[]]$Cmd)
    Push-Location $root
    try {
        docker compose -f $baseFile -f $composeFile @Cmd
        if ($LASTEXITCODE -ne 0) {
            throw "docker compose $($Cmd -join ' ') failed (exit code: $LASTEXITCODE)"
        }
    }
    finally {
        Pop-Location
    }
}

# ---- 基础检查：Docker 守护进程必须可用 ----
if (-not $Down) {
    docker info *> $null
    if ($LASTEXITCODE -ne 0) {
        Write-Host '未检测到 Docker 守护进程。' -ForegroundColor Red
        Write-Host '请先启动 Docker Desktop（或对应引擎）再重试本脚本。' -ForegroundColor Yellow
        exit 1
    }
}

if ($Down) {
    if ($Service) {
        Invoke-Compose @('rm', '-sf', $Service) # 只删指定服务容器
    }
    else {
        $cmd = @('down')
        if ($Volumes) { $cmd += '-v' }
        $cmd += '--remove-orphans'
        Invoke-Compose $cmd
    }
    Write-Host '本地容器已停止。' -ForegroundColor Green
    if ($Volumes) { Write-Host '数据卷已删除（数据库/事件流/缓存数据已清空）。' -ForegroundColor Yellow }
    exit 0
}

if ($Logs) {
    $cmd = @('logs', '-f', '--tail=100')
    if ($Service) { $cmd += $Service }
    Invoke-Compose $cmd
    exit 0
}

if ($Status) {
    Invoke-Compose @('ps')
    exit 0
}

# ---- 默认动作：拉起全部容器 ----
$upArgs = @('up', '-d')
if (-not $OnlyStart) {
    $upArgs += '--build' # 构建（或增量重建）镜像
}
$upArgs += '--remove-orphans'
if ($Service) { $upArgs += $Service }

Write-Host '>>> 构建镜像并启动 eChat 本地全容器栈 ...' -ForegroundColor Cyan
Invoke-Compose $upArgs

$fePort = if ($env:LOCAL_FRONTEND_PORT) { $env:LOCAL_FRONTEND_PORT } else { '8081' }
$bePort = if ($env:LOCAL_BACKEND_HTTP) { $env:LOCAL_BACKEND_HTTP } else { '8082' }

Write-Host ''
Write-Host '========== 已启动 ==========' -ForegroundColor Green
Write-Host "前端（推荐入口，/api 与 /ws 已反代到后端）: http://localhost:$fePort"
Write-Host "后端直连（调试）: http://localhost:$bePort"
Write-Host '容器状态:'
Invoke-Compose @('ps')

Write-Host ''
Write-Host '常用命令：' -ForegroundColor Cyan
Write-Host '  查看日志:   .\deploy\local-up.ps1 -Logs'
Write-Host '  只启动:     .\deploy\local-up.ps1 -OnlyStart'
Write-Host '  停止:       .\deploy\local-up.ps1 -Down'
Write-Host '  重置数据:   .\deploy\local-up.ps1 -Down -Volumes'
Write-Host '  进数据库:   docker compose -f deploy/docker-compose.base.yml -f deploy/docker-compose.local.yml exec postgres psql -U echat -d echat'
