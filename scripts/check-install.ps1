# Guards the Windows installer against regressions that break non-admin installs.
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$installer = Join-Path $root "install.ps1"

$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile($installer, [ref]$null, [ref]$errors)
if ($errors) {
    $errors | ForEach-Object { Write-Host "install.ps1:$($_.Extent.StartLineNumber): $($_.Message)" }
    throw "install.ps1 has syntax errors"
}

$text = Get-Content -LiteralPath $installer -Raw

# Defender quarantines unsigned binaries that add HKCU\...\Run entries, which broke installs
# before v0.1.2. Removal of the legacy value is allowed; writing one is not.
if ($text -match '(?m)^\s*(Set|New)-ItemProperty[^\r\n]*CurrentVersion\\Run') {
    throw "install.ps1 must not write HKCU\...\Run; use the Startup folder"
}
if ($text -notmatch 'GetFolderPath\("Startup"\)') {
    throw "install.ps1 must register autostart in the per-user Startup folder"
}

# The daemon locks its own image on Windows, so it has to be stopped before replacement.
if ($text -notmatch "Name -like 'smr\*\.exe'" -or $text.IndexOf("Name -like 'smr*.exe'") -gt $text.IndexOf("Move-Item")) {
    throw "install.ps1 must stop running smr*.exe (including renamed builds) before replacing the binary"
}
if (-not $text.Contains('\bserve(\s+--background)?\s*$')) {
    throw "install.ps1 must match the daemon command exactly, including the --background flag"
}

# A task-registered daemon must be started as a task: schtasks /End cannot stop
# a process that Start-Process launched.
if (-not $text.Contains('Start-ScheduledTask -TaskName $taskName')) {
    throw "install.ps1 must start the registered task with Start-ScheduledTask"
}

# -NoService installs the binary only; waiting for /health would always fail.
$wait = $text.IndexOf('Step "Waiting for SMR to become healthy..."')
$guard = $text.LastIndexOf('if ($Autostart -ne "None") {', $wait)
if ($wait -lt 0 -or $guard -lt 0) {
    throw "install.ps1 must skip the health wait when autostart is disabled (-NoService)"
}

Write-Host "install.ps1 checks passed"
