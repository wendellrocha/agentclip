[CmdletBinding()]
param(
    [string]$Version = $env:AGENTCLIP_VERSION,
    [string]$InstallDir = $(if ($env:AGENTCLIP_INSTALL_DIR) { $env:AGENTCLIP_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\AgentClip\bin" })
)

$ErrorActionPreference = "Stop"
$repository = if ($env:AGENTCLIP_REPOSITORY) { $env:AGENTCLIP_REPOSITORY } else { "wendellrocha/agentclip" }

if ($env:OS -ne "Windows_NT") {
    throw "This installer supports Windows only. Use scripts/install.sh on macOS or Linux."
}

if ([string]::IsNullOrWhiteSpace($Version) -or $Version -eq "latest") {
    $release = Invoke-RestMethod -Headers @{ "User-Agent" = "agentclip-installer" } -Uri "https://api.github.com/repos/$repository/releases/latest"
    $Version = $release.tag_name
}

if ($Version -notmatch '^v\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$') {
    throw "Could not resolve a semantic release tag (got '$Version')."
}

$architecture = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "Unsupported CPU architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$asset = "agentclip_${Version}_windows_${architecture}.zip"
$baseUrl = "https://github.com/$repository/releases/download/$Version"
$temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ("agentclip-install-" + [guid]::NewGuid())

try {
    New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
    $archive = Join-Path $temporaryDirectory $asset
    $checksums = Join-Path $temporaryDirectory "checksums.txt"
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$asset" -OutFile $archive
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/checksums.txt" -OutFile $checksums

    $checksumLine = Get-Content $checksums | Where-Object { $_ -match ("\s" + [regex]::Escape($asset) + "$") } | Select-Object -First 1
    if (-not $checksumLine) {
        throw "Checksum for $asset was not found in the release."
    }
    $expectedChecksum = ($checksumLine -split '\s+')[0]
    $actualChecksum = (Get-FileHash -Algorithm SHA256 -Path $archive).Hash.ToLowerInvariant()
    if ($expectedChecksum.ToLowerInvariant() -ne $actualChecksum) {
        throw "Checksum mismatch for $asset; refusing to install it."
    }

    Expand-Archive -Path $archive -DestinationPath $temporaryDirectory
    $binary = Join-Path $temporaryDirectory "agentclip_${Version}_windows_${architecture}\agentclip.exe"
    if (-not (Test-Path -Path $binary -PathType Leaf)) {
        throw "Release archive did not contain the expected AgentClip binary."
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Force -Path $binary -Destination (Join-Path $InstallDir "agentclip.exe")
    Write-Host "Installed AgentClip $Version at $(Join-Path $InstallDir 'agentclip.exe')"
    & (Join-Path $InstallDir "agentclip.exe") version

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (($userPath -split ';') -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable("Path", (($userPath.TrimEnd(';') + ";" + $InstallDir).TrimStart(';')), "User")
        $env:Path = "$InstallDir;$env:Path"
        Write-Host "Added $InstallDir to your user PATH. Open a new terminal after installation."
    }
}
finally {
    if (Test-Path $temporaryDirectory) {
        Remove-Item -Recurse -Force $temporaryDirectory
    }
}
