@echo off
setlocal EnableExtensions
cd /d "%~dp0"

echo.
echo   EPHEMERA ^| compiling the signal...
echo.

where go >nul 2>nul
if errorlevel 1 (
    echo [error] Go was not found in PATH.
    echo Install Go 1.22.8 or newer from https://go.dev/dl/
    pause
    exit /b 1
)

if not exist "bin" mkdir "bin"

if not exist "go.sum" (
    echo [setup] Resolving Go modules...
    go mod tidy
    if errorlevel 1 goto :build_failed
)

go build -trimpath -ldflags "-s -w -X main.version=dev" -o "bin\ephemera.exe" ".\cmd\ephemera"
if errorlevel 1 goto :build_failed

echo [ready] Launching Ephemera...
echo.
"bin\ephemera.exe" %*
exit /b %errorlevel%

:build_failed
echo.
echo [error] Ephemera could not be built.
echo Review the compiler output above.
pause
exit /b 1
