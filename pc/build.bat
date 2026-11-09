@echo off
rem Build USB Display (requires Go 1.24+: https://go.dev/dl/)
cd /d "%~dp0"
go vet . || exit /b 1
go build -trimpath -ldflags "-H windowsgui -s -w" -o USBDisplay.exe . || exit /b 1
echo Done: %~dp0USBDisplay.exe
