#Requires -Version 5.1
[CmdletBinding()]
param(
    [string]$Version = $env:SMR_VERSION,
    [switch]$NoSetup,
    [switch]$NoService,
    [switch]$NoPath,
    [ValidateSet("Auto", "Task", "Startup", "None")]
    [string]$Autostart = "Auto"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
} catch {
    # Newer PowerShell versions negotiate TLS automatically.
}

if ($env:SMR_NO_SETUP -eq "1") { $NoSetup = $true }
if ($env:SMR_NO_SERVICE -eq "1") { $NoService = $true }
if ($env:SMR_NO_PATH -eq "1") { $NoPath = $true }
if ($env:SMR_AUTOSTART) { $Autostart = $env:SMR_AUTOSTART }
if ($NoService) { $Autostart = "None" }

function Fail([string]$Message) {
    Write-Error $Message
    exit 1
}

function Step([string]$Message) {
    Write-Host $Message
}

$repo = "acmeai-vn/SMR"
$taskName = "AcmeAI SMR"

try {
    $osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
} catch {
    Fail "Could not detect Windows architecture."
}

switch ($osArch) {
    "X64" { $arch = "amd64" }
    "Arm64" { $arch = "arm64" }
    default { Fail "Unsupported Windows architecture: $osArch" }
}

if ($Version) {
    if ($Version -notmatch '^v[0-9A-Za-z._-]+$') {
        $Version = "v$Version"
    }
    if ($Version -notmatch '^v[0-9A-Za-z._-]+$') {
        Fail "Invalid version: $Version"
    }
    $baseUrl = "https://github.com/$repo/releases/download/$Version"
} else {
    $baseUrl = "https://github.com/$repo/releases/latest/download"
}

if ($env:SMR_BASE_URL) {
    $baseUrl = $env:SMR_BASE_URL
}

if ($env:SMR_INSTALL_DIR) {
    $installDir = $env:SMR_INSTALL_DIR
} else {
    $installDir = Join-Path $env:LOCALAPPDATA "AcmeAI\SMR"
}

$tempDir = Join-Path ([IO.Path]::GetTempPath()) ("smr-install-" + [Guid]::NewGuid().ToString("N"))
$extractDir = Join-Path $tempDir "extract"
New-Item -ItemType Directory -Force -Path $tempDir | Out-Null

try {
    Step "Fetching release manifest..."
    $manifestPath = Join-Path $tempDir "smr-release.json"
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/smr-release.json" -OutFile $manifestPath
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json

    if ($manifest.schema -ne 1) {
        Fail "Unsupported release manifest schema."
    }

    $manifestVersion = [string]$manifest.version
    if (-not $manifestVersion) {
        Fail "Release manifest has no version."
    }
    if ($Version -and ($Version -ne $manifestVersion)) {
        Fail "Manifest version $manifestVersion does not match requested $Version."
    }
    $Version = $manifestVersion

    $platform = "windows-$arch"
    $assetProperty = $manifest.assets.PSObject.Properties[$platform]
    if (-not $assetProperty) {
        Fail "No Windows $arch asset in the release manifest."
    }
    $asset = $assetProperty.Value
    $assetFile = [string]$asset.file
    $expectedHash = [string]$asset.sha256
    if (-not $assetFile -or -not $expectedHash) {
        Fail "Release manifest asset is missing file or sha256."
    }

    Step "Downloading $assetFile..."
    $archivePath = Join-Path $tempDir $assetFile
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$assetFile" -OutFile $archivePath

    $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $expectedHash.ToLowerInvariant()) {
        Fail "Checksum mismatch for $assetFile. Expected $expectedHash, got $actualHash."
    }

    Step "Installing SMR $Version..."
    Expand-Archive -LiteralPath $archivePath -DestinationPath $extractDir -Force
    $sourceBinary = Join-Path $extractDir "smr.exe"
    if (-not (Test-Path -LiteralPath $sourceBinary)) {
        Fail "Archive does not contain smr.exe."
    }

    $existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    if ($existingTask) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    }

    # Releases up to v0.1.1 registered a HKCU Run entry. Defender classifies an
    # unsigned binary that writes one as Behavior:Win32/Persistence.A!ml, so the
    # entry is removed here and is never written again.
    $legacyRunKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
    if (Test-Path -LiteralPath $legacyRunKey) {
        Remove-ItemProperty -LiteralPath $legacyRunKey -Name $taskName -ErrorAction SilentlyContinue
    }

    # A running daemon locks its own image. Stop it before the binary is
    # replaced, otherwise Windows leaves the path in a delete-pending state.
    # Match the daemon exactly: `smr serve` or `smr serve --background`.
    Get-CimInstance Win32_Process -Filter "Name LIKE 'smr%'" -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -like 'smr*.exe' -and $_.CommandLine -match '\bserve(\s+--background)?\s*$' } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        $running = Get-CimInstance Win32_Process -Filter "Name LIKE 'smr%'" -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -like 'smr*.exe' -and $_.CommandLine -match '\bserve(\s+--background)?\s*$' }
        if (-not $running) { break }
        Start-Sleep -Milliseconds 250
    }

    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    $binary = Join-Path $installDir "smr.exe"
    $newBinary = Join-Path $installDir ".smr.new.exe"
    Copy-Item -LiteralPath $sourceBinary -Destination $newBinary -Force
    $replaced = $false
    for ($attempt = 0; $attempt -lt 10; $attempt++) {
        try {
            Move-Item -LiteralPath $newBinary -Destination $binary -Force
            $replaced = $true
            break
        } catch {
            if ($attempt -eq 9) { throw }
            Start-Sleep -Milliseconds 500
        }
    }
    if (-not $replaced) {
        Fail "Could not replace $binary. Stop any running smr.exe process and run the installer again."
    }
    Unblock-File -LiteralPath $binary -ErrorAction SilentlyContinue

    if (-not $NoPath) {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        if (-not $userPath) { $userPath = "" }
        $parts = @()
        foreach ($part in $userPath.Split(";")) {
            if ($part -and ($part.TrimEnd("\") -ne $installDir.TrimEnd("\"))) {
                $parts += $part
            }
        }
        $newUserPath = (@($parts) + $installDir) -join ";"
        [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
        $env:Path = "$installDir;$env:Path"
    }

    $startupShortcut = Join-Path ([Environment]::GetFolderPath("Startup")) "AcmeAI SMR.lnk"
    if (Test-Path -LiteralPath $startupShortcut) {
        Remove-Item -LiteralPath $startupShortcut -Force -ErrorAction SilentlyContinue
    }

    # $registeredTask decides how the daemon starts below: a scheduled task must
    # be started with Start-ScheduledTask, because schtasks /End cannot stop a
    # process that Start-Process launched.
    $registeredTask = $false
    if ($Autostart -ne "None") {
        $registered = $false
        if ($Autostart -eq "Auto" -or $Autostart -eq "Task") {
            Step "Registering the per-user background task..."
            try {
                $action = New-ScheduledTaskAction -Execute $binary -Argument "serve --background" -WorkingDirectory $installDir
                $trigger = New-ScheduledTaskTrigger -AtLogOn
                $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
                Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Description "Smart Model Router local daemon" -Force | Out-Null
                $registered = $true
                $registeredTask = $true
            } catch {
                if ($Autostart -eq "Task") {
                    Fail "Scheduled task registration was denied: $($_.Exception.Message)"
                }
                Write-Verbose "Scheduled task registration was denied: $($_.Exception.Message)"
            }
        }

        if (-not $registered -and ($Autostart -eq "Auto" -or $Autostart -eq "Startup")) {
            # Some machines deny task creation to non-elevated users. SMR still
            # installs without administrator rights, so the shortcut goes into
            # the per-user Startup folder. A Run-key entry is never written:
            # Defender treats it as malware persistence.
            Step "Registering per-user autostart (Startup folder)..."
            $startupDir = Split-Path -Parent $startupShortcut
            if (-not (Test-Path -LiteralPath $startupDir)) {
                New-Item -ItemType Directory -Force -Path $startupDir | Out-Null
            }
            $shell = New-Object -ComObject WScript.Shell
            $shortcut = $shell.CreateShortcut($startupShortcut)
            $shortcut.TargetPath = $binary
            $shortcut.Arguments = "serve --background"
            $shortcut.WorkingDirectory = $installDir
            $shortcut.WindowStyle = 7
            $shortcut.Description = "Smart Model Router local daemon"
            $shortcut.Save()
            $registered = $true
        }

        if (-not $registered) {
            Write-Warning "Autostart is disabled. Start the daemon with: smr serve"
        }
    } else {
        Step "Autostart disabled (-NoService). Start SMR when needed with: smr serve"
    }

    # -NoService installs the binary only, so there is nothing to wait for.
    if ($Autostart -ne "None") {
        Step "Waiting for SMR to become healthy..."
        $healthUrl = "http://127.0.0.1:8787/health"
        $ready = $false
        for ($i = 0; $i -lt 30; $i++) {
            try {
                $response = Invoke-WebRequest -UseBasicParsing -Uri $healthUrl -TimeoutSec 2
                if ($response.StatusCode -eq 200) {
                    $ready = $true
                    break
                }
            } catch {
            }

            if ($i -eq 0) {
                # A daemon registered as a task must be started as a task; schtasks
                # cannot stop a process that Start-Process launched.
                if ($registeredTask) {
                    Start-ScheduledTask -TaskName $taskName
                } elseif (Test-Path -LiteralPath $binary) {
                    Start-Process -FilePath $binary -ArgumentList "serve", "--background" -WindowStyle Hidden
                }
            }
            Start-Sleep -Milliseconds 1000
        }

        if (-not $ready) {
            $hit = $null
            try {
                $hit = Get-MpThreatDetection -ErrorAction Stop |
                    Where-Object { ($_.Resources -join ";") -like "*$installDir*" } |
                    Select-Object -First 1
            } catch {
            }
            if ($hit) {
                Write-Warning "Antivirus flagged this install: $($hit.ThreatName)"
            }
            if (-not (Test-Path -LiteralPath $binary)) {
                Write-Warning "Antivirus removed $binary right after installation."
            }
            Write-Warning "Allow $installDir in your antivirus, then install again."
            Fail "SMR did not answer /health at 127.0.0.1:8787. See $installDir\smr.log and docs/TROUBLESHOOTING-WINDOWS.md."
        }
    }

    Step "Installed SMR $Version at $binary"

    if (-not $NoSetup -and $Autostart -ne "None") {
        & $binary setup
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "Setup did not complete. Run: $binary setup"
        }
    }
}
finally {
    if (Test-Path -LiteralPath $tempDir) {
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
        $tempFull = [IO.Path]::GetFullPath($tempDir)
        if ($tempFull.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}
