@echo off
REM DacatDHCP V1 构建脚本
REM 依赖: Go 1.20.14, rsrc (go install github.com/akavel/rsrc@latest)
REM 生成: DacatDHCP.exe (包含 PE 图标资源)

echo === DacatDHCP V1 Build ===

REM 1. 生成 Windows PE 资源文件 (.syso)
echo [1/3] Generating PE resource...
rsrc -ico web\dhcp.ico -o rsrc_windows_amd64.syso
if errorlevel 1 (
    echo ERROR: rsrc failed. Install: go install github.com/akavel/rsrc@latest
    exit /b 1
)

REM 2. 运行测试
echo [2/3] Running tests...
go vet ./...
if errorlevel 1 (
    echo ERROR: go vet failed
    exit /b 1
)
go test ./... -count=1
if errorlevel 1 (
    echo ERROR: go test failed
    exit /b 1
)

REM 3. 构建最终 EXE
echo [3/3] Building DacatDHCP.exe...
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
go build -o DacatDHCP.exe ./cmd/dacatdhcp/
if errorlevel 1 (
    echo ERROR: go build failed
    exit /b 1
)

echo === Build Complete: DacatDHCP.exe ===
