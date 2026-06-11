@echo off
setlocal EnableExtensions
cd /d "%~dp0"

rem Bubble Tea v2 requires Go 1.25+. Let supported Go installations
rem download/select the required toolchain automatically when necessary.
set "GOTOOLCHAIN=auto"

echo.
echo   EPHEMERA ^| compiling the signal...
echo.

where go >nul 2>nul
if errorlevel 1 (
    echo [error] Go was not found in PATH.
    echo Install Go 1.25.0 or newer from https://go.dev/dl/
    pause
    exit /b 1
)

if not exist "bin" mkdir "bin"
del /q "bin\ephemera-run-*.exe" >nul 2>nul

rem Always refresh go.mod/go.sum. This is important when v6 is extracted over
rem an older Ephemera checkout whose go.sum contains only Bubble Tea v1 sums.
echo [setup] Resolving and verifying Go modules...
go mod tidy
if errorlevel 1 goto :dependency_failed

go mod verify
if errorlevel 1 goto :dependency_failed

set "EXE=bin\ephemera-run-%RANDOM%%RANDOM%.exe"
go build -mod=mod -trimpath -ldflags "-s -w -X main.version=dev" -o "%EXE%" ".\cmd\ephemera"
if errorlevel 1 goto :build_failed

echo [ready] Launching Ephemera...
echo.
"%EXE%" %*
exit /b %errorlevel%

:dependency_failed
echo.
echo [error] Ephemera dependencies could not be resolved.
echo Confirm that this machine can reach proxy.golang.org and sum.golang.org,
echo or configure GOPROXY for your network, then run this script again.
pause
exit /b 1

:build_failed
echo.
echo [error] Ephemera could not be built.
echo Review the compiler output above.
pause
exit /b 1
