# build.ps1 — one-shot build for shuttle release binaries.
# 一键编译 shuttle 发布包。
#
# Output (in build/):
#   shuttle.exe              Windows client (with embedded linux agents)
#   shuttle_linux_amd64      standalone amd64 agent
#   shuttle_linux_arm64      standalone arm64 agent
#
# Usage: .\build.ps1

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$ldflags = "-s -w"
$outDir = "build"
Remove-Item -Recurse -Force $outDir -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $outDir | Out-Null

# ── Step 1: cross-compile slim agents → embed dir ──
Write-Host "[1/3] Building embedded agents..." -ForegroundColor Cyan

$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -ldflags="$ldflags" -o internal\agent\agents\linux_amd64 .\cmd\shuttle_agent\
Write-Host "  linux/amd64  $( '{0,6:N0} KB' -f ((Get-Item internal\agent\agents\linux_amd64).Length/1KB) )"

$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -ldflags="$ldflags" -o internal\agent\agents\linux_arm64 .\cmd\shuttle_agent\
Write-Host "  linux/arm64  $( '{0,6:N0} KB' -f ((Get-Item internal\agent\agents\linux_arm64).Length/1KB) )"

# ── Step 2: build shuttle.exe (Windows, embeds the agents above) ──
Write-Host "[2/3] Building shuttle.exe..." -ForegroundColor Cyan

$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -ldflags="$ldflags" -o $outDir\shuttle.exe .\cmd\shuttle\
Write-Host "  windows/amd64  $( '{0,6:N0} KB' -f ((Get-Item $outDir\shuttle.exe).Length/1KB) )"

# ── Step 3: standalone agents (for manual deployment / legacy) ──
Write-Host "[3/3] Building standalone agents..." -ForegroundColor Cyan

$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -ldflags="$ldflags" -o $outDir\shuttle_linux_amd64 .\cmd\shuttle_agent\
Write-Host "  shuttle_linux_amd64  $( '{0,6:N0} KB' -f ((Get-Item $outDir\shuttle_linux_amd64).Length/1KB) )"

$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -ldflags="$ldflags" -o $outDir\shuttle_linux_arm64 .\cmd\shuttle_agent\
Write-Host "  shuttle_linux_arm64  $( '{0,6:N0} KB' -f ((Get-Item $outDir\shuttle_linux_arm64).Length/1KB) )"

# ── Done ──
Write-Host "`nDone! Output in $outDir\" -ForegroundColor Green
Get-ChildItem $outDir | ForEach-Object {
    Write-Host "  $($_.Name)  $( '{0,6:N0} KB' -f ($_.Length/1KB) )"
}
