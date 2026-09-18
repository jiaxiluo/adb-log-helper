@echo off
REM Enable delayed expansion so !errorlevel! inside parenthesized
REM blocks reflects the command that just ran (not the value at
REM block-parse time).
setlocal EnableDelayedExpansion
REM ============================================================
REM   Build ADB Tool (Wails v2.11.0 desktop app)
REM   Pure-ASCII batch file to avoid cmd.exe GBK/UTF-8 parse errors.
REM   Every step logs to a .log file; the log is DELETED on success
REM   and KEPT (and printed) only when the step fails.
REM ============================================================
chcp 65001 >nul
cd /d "%~dp0"

set GOTOOLCHAIN=local
set WAILS_VER=v2.11.0

REM Make GOPATH\bin visible in THIS session so a freshly installed
REM wails CLI can be found by later steps.
for /f "delims=" %%i in ('go env GOPATH') do set GOPATH_DIR=%%i
set PATH=%PATH%;%GOPATH_DIR%\bin

echo [INFO] === Environment ===
go env GOVERSION GOTOOLCHAIN GOPROXY GOPATH
echo.

REM ---- 1. Check whether wails CLI is installed ----
where wails >nul 2>&1
if %errorlevel% neq 0 (
    echo [INFO] wails CLI not found, installing %WAILS_VER% ...
    go install github.com/wailsapp/wails/v2/cmd/wails@%WAILS_VER% > wails-install.log 2>&1
    if !errorlevel! neq 0 (
        echo [ERROR] Failed to install wails CLI. Full output:
        echo ------------ wails-install.log ------------
        type wails-install.log
        echo ------------------------------------------
        echo.
        echo Hints:
        echo   - Check network/proxy: GOPROXY should reach goproxy.cn or proxy.golang.org.
        echo   - If log says "requires go >= 1.2X", tell the maintainer to bump GOTOOLCHAIN.
        pause
        exit /b 1
    )
    del wails-install.log >nul 2>&1
    echo [INFO] wails CLI installed.
) else (
    echo [INFO] wails CLI already present.
)

REM ---- 2. Pin wails library version in go.mod ----
echo [INFO] Pinning wails library to %WAILS_VER% ...
go get github.com/wailsapp/wails/v2@%WAILS_VER% > wails-get.log 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] go get wails failed. Full output:
    type wails-get.log
    pause
    exit /b 1
)
del wails-get.log >nul 2>&1

REM ---- 3. Tidy dependencies ----
echo [INFO] Tidying dependencies (go mod tidy) ...
go mod tidy > wails-tidy.log 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] go mod tidy failed. Full output:
    type wails-tidy.log
    pause
    exit /b 1
)
del wails-tidy.log >nul 2>&1

REM ---- 4. Build ----
echo [INFO] Building ...
wails build > wails-build.log 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] Build failed. Full output:
    echo ------------ wails-build.log ------------
    type wails-build.log
    echo ------------------------------------------
    pause
    exit /b 1
)
del wails-build.log >nul 2>&1

REM ---- 5. Publish: keep ONE exe copy in adb-helper\ (project root) ----
REM The project keeps a single program copy in adb-helper\ (next to
REM platform-tools.zip, one level above code\). The build\bin output is
REM replaced each build.
if not exist ..\adb-helper mkdir ..\adb-helper
copy /y build\bin\adb-log-helper.exe ..\adb-helper\adb-log-helper.exe >nul
if %errorlevel% neq 0 (
    echo [ERROR] Failed to copy exe to ..\adb-helper\.
    pause
    exit /b 1
)
del build\bin\adb-log-helper.exe >nul 2>&1

echo.
echo [OK] Build succeeded! Program published to: adb-helper\adb-log-helper.exe
echo.
pause
