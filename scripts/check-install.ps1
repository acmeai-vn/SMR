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
if ($text -notmatch "Name='smr\.exe'" -or $text.IndexOf("Name='smr.exe'") -gt $text.IndexOf("Move-Item")) {
    throw "install.ps1 must stop running smr.exe before replacing the binary"
}

Write-Host "install.ps1 checks passed"
