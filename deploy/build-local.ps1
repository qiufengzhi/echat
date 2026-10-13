# =============================================================================
# eChat 本地一键「只构建镜像」脚本
#
# 构建 echat-local 拓扑中三个本仓库自定义镜像（不动基础设施容器/镜像）：
#   echat-local-backend:latest    (backend/Dockerfile,  Go 1.26)
#   echat-local-frontend:latest   (frontend/Dockerfile, Node 22 + Nginx)
#   echat-local-agent:latest      (agent/Dockerfile,    Python 3.12)
#
# 用法（在仓库根目录执行）：
#   .\deploy\build-local.ps1                  # 构建全部三个镜像
#   .\deploy\build-local.ps1 -Service backend # 只构建某个服务
#   .\deploy\build-local.ps1 -NoCache         # 禁用构建缓存，全量重建
#   .\deploy\build-local.ps1 -Push            # 顺便推到镜像仓库（见变量 $registry）
#
# 注意：本脚本含中文，需以 UTF-8(BOM) 编码保存，否则 Windows PowerShell 5.1
# 会按 GBK 误读导致语法错误。若改动后报错请重新转换编码。
# =============================================================================
[CmdletBinding()]
param(
    [string]$Service, # 只构建指定服务: backend / frontend / agent
    [switch]$NoCache, # 禁用缓存全量重建
    [switch]$Push      # 构建成功后推送
)

$ErrorActionPreference = 'Stop'

$root  = Split-Path -Parent $PSScriptRoot
$baseFile = Join-Path $root 'deploy/docker-compose.base.yml'
$composeFile = Join-Path $root 'deploy/docker-compose.local.yml'

# 如需推送到远端仓库，改成你自己的镜像仓库前缀（当前脚本只打本地 tag）
$registry = $env:ECHAT_REGISTRY

# ---- 允许构建的服务 ----
$serviceMap = @{
    'backend'  = 'echat-local-backend:latest'
    'frontend' = 'echat-local-frontend:latest'
    'agent'    = 'echat-local-agent:latest'
}

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
docker info *> $null
if ($LASTEXITCODE -ne 0) {
    Write-Host '未检测到 Docker 守护进程，请先启动 Docker Desktop（或对应引擎）。' -ForegroundColor Red
    exit 1
}

# ---- 决定要构建哪些服务 ----
$targets = @()
if ($Service) {
    if (-not $serviceMap.ContainsKey($Service)) {
        Write-Host "未知服务 '$Service'，可选: $($serviceMap.Keys -join ' / ')" -ForegroundColor Red
        exit 1
    }
    $targets += $Service
}
else {
    $targets = @($serviceMap.Keys)
}

Write-Host '========== 开始构建 ==========' -ForegroundColor Cyan
foreach ($svc in $targets) {
    Write-Host ">>> [$svc] -> $($serviceMap[$svc])" -ForegroundColor Cyan
}

$cmd = @('build')
if ($NoCache) { $cmd += '--no-cache' }
$cmd += $targets

Invoke-Compose $cmd

Write-Host ''
Write-Host '========== 构建完成 ==========' -ForegroundColor Green
foreach ($svc in $targets) {
    Write-Host "  $($serviceMap[$svc])  <- $svc"
}

# 校验镜像确实存在
foreach ($svc in $targets) {
    docker image inspect $serviceMap[$svc] *> $null
    if ($LASTEXITCODE -ne 0) {
        Write-Host "警告: 未找到镜像 $($serviceMap[$svc])" -ForegroundColor Yellow
    }
}

# 可选推送
if ($Push) {
    if (-not $registry) {
        Write-Host '推送前请设置环境变量: $env:ECHAT_REGISTRY = "<registry>/<repo>/"' -ForegroundColor Red
        exit 1
    }
    foreach ($svc in $targets) {
        $remote = "$registry$($serviceMap[$svc].Split(':')[0]):$($serviceMap[$svc].Split(':')[1])"
        Write-Host ">>> 推送 $remote" -ForegroundColor Cyan
        docker tag $serviceMap[$svc] $remote
        docker push $remote
    }
}

Write-Host ''
Write-Host '下一步启动容器： .\deploy\local-up.ps1 -OnlyStart' -ForegroundColor Yellow
