#Requires -Version 5.1
[CmdletBinding()]
param(
    [string]$Version = $env:SMR_VERSION,
    [switch]$NoSetup,
    [switch]$NoService,
    [switch]$NoPath
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

    if (-not $NoService) {
        Step "Registering the per-user background task..."
        $action = New-ScheduledTaskAction -Execute $binary -Argument "serve" -WorkingDirectory $installDir
        $trigger = New-ScheduledTaskTrigger -AtLogOn
        $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
        Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Description "Smart Model Router local daemon" -Force | Out-Null
        Start-ScheduledTask -TaskName $taskName

        Step "Waiting for SMR to become healthy..."
        $ready = $false
        for ($i = 0; $i -lt 30; $i++) {
            try {
                $response = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:8787/health" -TimeoutSec 2
                if ($response.StatusCode -eq 200) {
                    $ready = $true
                    break
                }
            } catch {
                Start-Sleep -Milliseconds 1000
            }
        }
        if (-not $ready) {
            Fail "SMR did not answer /health at 127.0.0.1:8787. Check Task Scheduler and the SMR log."
        }
    }

    Step "Installed SMR $Version at $binary"

    if (-not $NoSetup -and -not $NoService) {
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
