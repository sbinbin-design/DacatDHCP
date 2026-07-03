@echo off
REM DacatDHCP Resource Generator
REM Uses goversioninfo v1.4.0 to embed icon + VERSIONINFO into PE resource (.syso)
REM Version source: internal\version\versioninfo.json (read by both Go code and this tool)
REM Run this script when version info, copyright, or icon changes.
REM After running, commit rsrc_windows_amd64.syso to version control.
REM Script does not depend on current working directory, uses ROOT to locate project root
REM goversioninfo is fixed to .tools\goversioninfo-v1.4.0\goversioninfo.exe (not PATH or GOBIN)

setlocal EnableExtensions EnableDelayedExpansion
set "EXITCODE=0"

REM Locate project root (script is in scripts\, root is %~dp0..)
set "ROOT=%~dp0.."
pushd "%ROOT%" || ( set "EXITCODE=1" & goto :cleanup )

echo === Generating PE Resource (icon + VERSIONINFO) ===

REM 0. Validate Go version is 1.26.4 (consistent with build.bat)
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

REM 1. Check version info JSON (single version source) exists
set VERINFO=internal\version\versioninfo.json
if not exist "!VERINFO!" (
    echo ERROR: !VERINFO! not found.
    set "EXITCODE=1"
    goto :cleanup
)

REM 2. Check icon file exists
if not exist "web\dhcp.ico" (
    echo ERROR: web\dhcp.ico not found.
    set "EXITCODE=1"
    goto :cleanup
)

REM 3. Check target directory exists
if not exist "cmd\dacatdhcp" (
    echo ERROR: cmd\dacatdhcp directory not found.
    set "EXITCODE=1"
    goto :cleanup
)

REM 4. Resolve goversioninfo tool path (fixed to project-local .tools directory)
set "TOOLS_DIR=.tools\goversioninfo-v1.4.0"
set "GOVINFO_EXE=!TOOLS_DIR!\goversioninfo.exe"

if not exist "!GOVINFO_EXE!" (
    echo goversioninfo v1.4.0 not found at !GOVINFO_EXE!
    echo Installing to project-local .tools directory...
    if not exist "!TOOLS_DIR!" mkdir "!TOOLS_DIR!"
    REM Set GOBIN temporarily so go install outputs to the project-local directory
    set "GOBIN=%ROOT%\.tools\goversioninfo-v1.4.0"
    go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.0
    if errorlevel 1 (
        echo ERROR: Failed to install goversioninfo v1.4.0.
        set "EXITCODE=!ERRORLEVEL!"
        if "!EXITCODE!"=="0" set "EXITCODE=1"
        goto :cleanup
    )
    if not exist "!GOVINFO_EXE!" (
        echo ERROR: goversioninfo.exe not found at !GOVINFO_EXE! after install.
        set "EXITCODE=1"
        goto :cleanup
    )
)

echo Using goversioninfo: !GOVINFO_EXE!

REM 5. Generate .syso (icon + VERSIONINFO from single JSON source)
REM -64 flag generates amd64 COFF format required by Go 1.26.4 linker
echo Generating rsrc_windows_amd64.syso from !VERINFO!...
"!GOVINFO_EXE!" -64 -icon "web\dhcp.ico" -o "cmd\dacatdhcp\rsrc_windows_amd64.syso" "!VERINFO!"
if errorlevel 1 (
    echo ERROR: goversioninfo failed.
    set "EXITCODE=!ERRORLEVEL!"
    if "!EXITCODE!"=="0" set "EXITCODE=1"
    goto :cleanup
)

REM 6. Clean up .res file if generated (keep only .syso)
if exist "cmd\dacatdhcp\rsrc_windows_amd64.res" del /q "cmd\dacatdhcp\rsrc_windows_amd64.res" 2>nul

REM 7. Verify output file exists
if not exist "cmd\dacatdhcp\rsrc_windows_amd64.syso" (
    echo ERROR: cmd\dacatdhcp\rsrc_windows_amd64.syso was not generated.
    set "EXITCODE=1"
    goto :cleanup
)

echo === Resource Generated: cmd\dacatdhcp\rsrc_windows_amd64.syso ===
echo This file should be committed to version control.
echo Run scripts\build.bat to build the EXE.

:cleanup
popd
endlocal & exit /b %EXITCODE%
