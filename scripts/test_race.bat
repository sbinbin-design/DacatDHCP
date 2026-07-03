@echo off
REM DacatDHCP Race Test Script
REM Runs go test -race to detect data races
REM Requires: Go 1.26.4 with CGO enabled and a C compiler (gcc or clang)
REM This script is for concurrency validation only, does not affect normal single-EXE release build
REM Script does not depend on current working directory, uses ROOT to locate project root

setlocal EnableExtensions EnableDelayedExpansion
set "EXITCODE=0"

REM Locate project root (script is in scripts\, root is %~dp0..)
set "ROOT=%~dp0.."
pushd "%ROOT%" || ( set "EXITCODE=1" & goto :cleanup )

echo === DacatDHCP Race Test ===

REM Validate Go version
for /f "tokens=3" %%v in ('go version 2^>nul') do set GOVER=%%v
if "!GOVER!"=="" (
    echo ERROR: Go is not installed or not in PATH.
    set "EXITCODE=1"
    goto :cleanup
)
set GOVER=!GOVER:go=!
if not "!GOVER!"=="1.26.4" (
    echo ERROR: Go version must be exactly 1.26.4, but found !GOVER!.
    set "EXITCODE=1"
    goto :cleanup
)

REM Check for available C compiler (gcc or clang)
where gcc >nul 2>nul
if !errorlevel! equ 0 (
    set CC=gcc
    goto :cc_found
)
where clang >nul 2>nul
if !errorlevel! equ 0 (
    set CC=clang
    goto :cc_found
)
echo ERROR: No C compiler found (gcc or clang).
echo        go test -race requires CGO support. Install MinGW-w64 or TDM-GCC.
echo        Download: https://www.mingw-w64.org/
set "EXITCODE=1"
goto :cleanup

:cc_found
echo Using C compiler: !CC!

REM Explicitly set CGO_ENABLED=1
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

echo Running go test -race ./... -count=1
go test -race ./... -count=1
if errorlevel 1 (
    echo ERROR: go test -race failed
    set "EXITCODE=!ERRORLEVEL!"
    if "!EXITCODE!"=="0" set "EXITCODE=1"
    goto :cleanup
)

echo === Race Test Passed ===

:cleanup
popd
endlocal & exit /b %EXITCODE%
