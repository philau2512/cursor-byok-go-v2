# install-patch.ps1
# Script tự động sao lưu và cài đặt nút Prompt Optimizer vào Cursor Client

$ErrorActionPreference = "Stop"

$cursorAppDir = "$env:LOCALAPPDATA\Programs\cursor\resources\app\out\vs\code\electron-sandbox\workbench"
if (-not (Test-Path $cursorAppDir)) {
    Write-Error "Không tìm thấy thư mục Cursor tại: $cursorAppDir"
    exit 1
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$sourceScript = Join-Path $scriptDir "cursor-prompt-optimizer.js"
if (-not (Test-Path $sourceScript)) {
    Write-Error "Không tìm thấy file nguồn: $sourceScript"
    exit 1
}

$targetScript = Join-Path $cursorAppDir "cursor-prompt-optimizer.js"
$targetHtml = Join-Path $cursorAppDir "workbench.html"
$backupHtml = Join-Path $cursorAppDir "workbench.html.bak"

Write-Host "1. Sao chép cursor-prompt-optimizer.js vào Cursor..." -ForegroundColor Cyan
Copy-Item -Path $sourceScript -Destination $targetScript -Force

Write-Host "2. Kiểm tra và sao lưu workbench.html..." -ForegroundColor Cyan
if (-not (Test-Path $backupHtml)) {
    Copy-Item -Path $targetHtml -Destination $backupHtml -Force
    Write-Host "   Đã tạo bản backup tại: $backupHtml" -ForegroundColor Green
} else {
    Write-Host "   Bản backup đã tồn tại tại: $backupHtml" -ForegroundColor Gray
}

Write-Host "3. Cập nhật thẻ script trong workbench.html..." -ForegroundColor Cyan
$htmlContent = Get-Content -Path $targetHtml -Raw -Encoding UTF8
$scriptTag = '<script src="./cursor-prompt-optimizer.js" type="module"></script>'

if ($htmlContent -notmatch "cursor-prompt-optimizer\.js") {
    $htmlContent = $htmlContent.Replace("</html>", "	$scriptTag`n</html>")
    Set-Content -Path $targetHtml -Value $htmlContent -Encoding UTF8 -NoNewline
    Write-Host "   Đã nhúng script vào workbench.html thành công!" -ForegroundColor Green
} else {
    Write-Host "   workbench.html đã được nhúng script từ trước." -ForegroundColor Gray
}

Write-Host "`nHoàn tất! Hãy khởi động lại Cursor hoặc bấm Ctrl+R (Reload Window) để trải nghiệm." -ForegroundColor Yellow
