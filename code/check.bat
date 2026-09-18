@echo off
REM Enable delayed expansion so !errorlevel! inside parenthesized
REM blocks reflects the command that just ran (not the value at
REM block-parse time).
setlocal EnableDelayedExpansion
REM ============================================================
REM   Full automated check for adb-log-helper (pure ASCII).
REM   Runs: go vet, gofmt, go build, adb live-format checks,
REM   and a full wails build. Prints PASS/FAIL per step.
REM   Step logs are DELETED on success and KEPT only on failure.
REM ============================================================
chcp 65001 >nul
cd /d "%~dp0"
set GOTOOLCHAIN=local
set PASS=0
set FAIL=0

echo ============================================================
echo  [1/5] go vet
echo ============================================================
go vet ./... > check-vet.log 2>&1
if %errorlevel% neq 0 (
    echo [FAIL] go vet found issues:
    type check-vet.log
    set /a FAIL+=1
) else (
    echo [PASS] go vet clean.
    del check-vet.log >nul 2>&1
    set /a PASS+=1
)
echo.

echo ============================================================
echo  [2/5] gofmt (lists files needing formatting)
echo ============================================================
go fmt ./... > check-fmt.log 2>&1
findstr /r "." check-fmt.log >nul 2>&1
if %errorlevel% equ 0 (
    echo [INFO] go fmt reformatted:
    type check-fmt.log
) else (
    echo [PASS] All files already gofmt-compliant.
    del check-fmt.log >nul 2>&1
)
set /a PASS+=1
echo.

echo ============================================================
echo  [3/5] go build (compile check)
echo ============================================================
go build ./... > check-build.log 2>&1
if %errorlevel% neq 0 (
    echo [FAIL] go build failed:
    type check-build.log
    set /a FAIL+=1
) else (
    echo [PASS] go build OK.
    del check-build.log >nul 2>&1
    set /a PASS+=1
)
echo.

echo ============================================================
echo  [4/5] adb live format checks (validates ops.go parsing)
echo ============================================================
where adb >nul 2>&1
if %errorlevel% neq 0 (
    echo [SKIP] adb not on PATH.
) else (
    echo --- adb devices (expect: "List of devices attached" then serial state) ---
    adb devices 2>&1
    echo.
    echo --- adb shell pm list packages (first 3 lines, expect "package:xxx") ---
    adb shell pm list packages 2>&1 | findstr /n "." | findstr "^[1-3]:" 2>nul
    echo.
    echo [INFO] Compare above with ops.go parsing (Devices/ListPackages).
)
echo.

echo ============================================================
echo  [5/5] Full wails build (production exe)
echo ============================================================
where wails >nul 2>&1
if %errorlevel% neq 0 (
    echo [SKIP] wails CLI not installed (run build.bat first).
) else (
    wails build > check-wails.log 2>&1
    if !errorlevel! neq 0 (
        echo [FAIL] wails build failed:
        type check-wails.log
        set /a FAIL+=1
    ) else (
        echo [PASS] wails build OK -^> build\bin\adb-log-helper.exe
        del check-wails.log >nul 2>&1
        set /a PASS+=1
    )
)
echo.

echo ============================================================
echo  SUMMARY: PASS=%PASS% FAIL=%FAIL%
echo ============================================================
pause
