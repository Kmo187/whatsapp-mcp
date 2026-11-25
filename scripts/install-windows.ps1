<#
.SYNOPSIS
  One-click installer for WhatsApp MCP on Windows.

.DESCRIPTION
  - installs Git, Go, uv and a C compiler with winget (if missing)
  - downloads or updates the project in %USERPROFILE%\whatsapp-mcp
  - builds the WhatsApp bridge and installs the Python dependencies
  - starts the bridge invisibly at every login
  - registers the MCP server in Claude Desktop (a backup of the config is kept)
  - opens the dashboard so you can scan the QR code

  Run:  powershell -ExecutionPolicy Bypass -File install-windows.ps1
#>
param(
    [string]$Target = (Join-Path $HOME "whatsapp-mcp"),
    [string]$Repo = "https://github.com/Kmo187/whatsapp-mcp.git",
    [switch]$NoAutostart,
    [switch]$NoClaudeConfig
)

$ErrorActionPreference = "Stop"
function Step($msg) { Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Reload-Path {
    $env:Path = [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" + [Environment]::GetEnvironmentVariable("Path", "User")
}

Step "1/6  Checking tools (Git, Go, uv, C compiler)"
if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
    Write-Host "winget is missing. Install 'App Installer' from the Microsoft Store and run this script again." -ForegroundColor Red
    exit 1
}
$packages = @{ git = "Git.Git"; go = "GoLang.Go"; uv = "astral-sh.uv"; gcc = "BrechtSanders.WinLibs.POSIX.UCRT" }
foreach ($cmd in $packages.Keys) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        Write-Host "   installing $($packages[$cmd])"
        winget install --id $packages[$cmd] -e --accept-source-agreements --accept-package-agreements --silent | Out-Null
    }
}
Reload-Path
foreach ($cmd in $packages.Keys) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        Write-Host "'$cmd' was not found. Close PowerShell, open a new window and run the script again." -ForegroundColor Red
        exit 1
    }
}

Step "2/6  Getting the project into $Target"
if (Test-Path (Join-Path $Target ".git")) {
    Push-Location $Target
    git remote set-url origin $Repo
    git fetch origin --quiet
    $dirty = git status --porcelain --untracked-files=no
    if ($dirty) {
        Write-Host "   saving your local changes with 'git stash' (restore with: git stash pop)"
        git stash push -m "install-windows backup $(Get-Date -Format s)" | Out-Null
    }
    git checkout -B main origin/main --quiet
    Pop-Location
} elseif (Test-Path $Target) {
    Write-Host "$Target exists but is not a git checkout. Move it away or pass -Target <folder>." -ForegroundColor Red
    exit 1
} else {
    git clone --quiet $Repo $Target
}

Step "3/6  Building the WhatsApp bridge"
$bridge = Join-Path $Target "whatsapp-bridge"
Push-Location $bridge
$env:CGO_ENABLED = "1"
$version = (git describe --tags --always 2>$null)
go build -trimpath -ldflags "-s -w -X main.Version=$version" -o whatsapp-bridge.exe .
if ($LASTEXITCODE -ne 0) { Write-Host "Build failed." -ForegroundColor Red; exit 1 }
Pop-Location

Step "4/6  Installing the MCP server (Python)"
$server = Join-Path $Target "whatsapp-mcp-server"
Push-Location $server
uv sync --quiet
Pop-Location

Step "5/6  Start scripts and autostart"
$startBat = Join-Path $Target "bridge-starten.bat"
@"
@echo off
title WhatsApp bridge
cd /d "$bridge"
whatsapp-bridge.exe >> bridge.log 2>&1
"@ | Set-Content -Path $startBat -Encoding ASCII
$hidden = Join-Path $Target "bridge-unsichtbar.vbs"
"CreateObject(""WScript.Shell"").Run """"""$startBat"""""", 0, False" | Set-Content -Path $hidden -Encoding ASCII
if (-not $NoAutostart) {
    $lnkPath = Join-Path ([Environment]::GetFolderPath("Startup")) "WhatsApp-Bridge.lnk"
    $sh = New-Object -ComObject WScript.Shell
    $lnk = $sh.CreateShortcut($lnkPath)
    $lnk.TargetPath = "wscript.exe"
    $lnk.Arguments = "`"$hidden`""
    $lnk.WorkingDirectory = $bridge
    $lnk.Save()
    Write-Host "   the bridge now starts invisibly when you log in"
}

if (-not $NoClaudeConfig) {
    $candidates = @(
        (Join-Path $env:APPDATA "Claude\claude_desktop_config.json")
    ) + (Get-ChildItem (Join-Path $env:LOCALAPPDATA "Packages") -Filter "Claude_*" -Directory -ErrorAction SilentlyContinue |
        ForEach-Object { Join-Path $_.FullName "LocalCache\Roaming\Claude\claude_desktop_config.json" })
    $cfgPath = ($candidates | Where-Object { Test-Path $_ } | Select-Object -First 1)
    if (-not $cfgPath) { $cfgPath = $candidates[0]; New-Item -ItemType Directory -Force -Path (Split-Path $cfgPath) | Out-Null; "{}" | Set-Content $cfgPath }
    Copy-Item $cfgPath "$cfgPath.bak-$(Get-Date -Format yyyyMMdd-HHmmss)"
    $cfg = Get-Content $cfgPath -Raw | ConvertFrom-Json
    if (-not $cfg.mcpServers) { $cfg | Add-Member -NotePropertyName mcpServers -NotePropertyValue ([pscustomobject]@{}) }
    $uv = (Get-Command uv).Source
    $entry = [pscustomobject]@{ command = $uv; args = @("--directory", $server, "run", "main.py") }
    $cfg.mcpServers | Add-Member -NotePropertyName whatsapp -NotePropertyValue $entry -Force
    $cfg | ConvertTo-Json -Depth 20 | Set-Content -Path $cfgPath -Encoding UTF8
    Write-Host "   registered in $cfgPath (backup saved next to it)"
}

Step "6/6  Starting the bridge"
Get-Process whatsapp-bridge -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Process wscript.exe -ArgumentList "`"$hidden`""
Start-Sleep -Seconds 3
Start-Process "http://127.0.0.1:8080/"
Write-Host ""
Write-Host "Done. The dashboard opened in your browser:" -ForegroundColor Green
Write-Host "  - if a QR code is shown: WhatsApp on your phone -> Settings -> Linked devices -> Link a device"
Write-Host "  - then quit Claude Desktop completely (tray icon -> Quit) and start it again"
