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
del /q "bin\ephemera-run-*.exe" >nul 2>nul

if not exist "go.sum" (
    echo [setup] Resolving Go modules...
    go mod tidy
    if errorlevel 1 goto :build_failed
)

set "EXE=bin\ephemera-run-%RANDOM%%RANDOM%.exe"
go build -trimpath -ldflags "-s -w -X main.version=dev" -o "%EXE%" ".\cmd\ephemera"
if errorlevel 1 goto :build_failed

echo [ready] Launching Ephemera...
echo.
"%EXE%" %*
exit /b %errorlevel%

:build_failed
echo.
echo [error] Ephemera could not be built.
echo Review the compiler output above.
pause
exit /b 1
