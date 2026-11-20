@echo off
setlocal
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -Command "$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User') + ';$HOME\go\bin'; $version = (go run ./scripts/release version -config ./build/config.yml).Trim(); if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; $directory = Join-Path $PWD 'bin'; New-Item -ItemType Directory -Path $directory -Force | Out-Null; $baseName = 'cursor-byok-' + $version + '-macos-arm64'; $destination = Join-Path $directory ($baseName + '.zip'); $index = 1; while (Test-Path -LiteralPath $destination) { $destination = Join-Path $directory ($baseName + '(' + $index + ').zip'); $index++ }; task build:darwin:crosscompile ZIP_PATH=$destination; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; Write-Host ('Created: ' + $destination); Write-Host 'Next: copy the ZIP to the Mac, unzip it, then double-click install.command'"
set "EXIT_CODE=%ERRORLEVEL%"
pause
exit /b %EXIT_CODE%
