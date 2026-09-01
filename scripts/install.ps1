[CmdletBinding()]
param(
    [string]$Version = $env:AGENTCLIP_VERSION,
    [string]$InstallDir = $(if ($env:AGENTCLIP_INSTALL_DIR) { $env:AGENTCLIP_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\AgentClip\bin" })
)

$ErrorActionPreference = "Stop"
$repository = if ($env:AGENTCLIP_REPOSITORY) { $env:AGENTCLIP_REPOSITORY } else { "wendellrocha/agentclip" }

function Get-AgentClipVersionParts {
    param([Parameter(Mandatory = $true)][string]$Value)

    $match = [regex]::Match($Value.Trim(), '^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$')
    if (-not $match.Success) {
        throw "Could not parse semantic version '$Value'."
    }
    return [PSCustomObject]@{
        Major = [int]$match.Groups[1].Value
        Minor = [int]$match.Groups[2].Value
        Patch = [int]$match.Groups[3].Value
        PreRelease = $match.Groups[4].Value
    }
}

function Compare-AgentClipVersion {
    param(
        [Parameter(Mandatory = $true)][string]$Candidate,
        [Parameter(Mandatory = $true)][string]$Installed
    )

    $left = Get-AgentClipVersionParts $Candidate
    $right = Get-AgentClipVersionParts $Installed
    foreach ($part in @("Major", "Minor", "Patch")) {
        if ($left.$part -gt $right.$part) { return 1 }
        if ($left.$part -lt $right.$part) { return -1 }
    }
    if ([string]::IsNullOrEmpty($left.PreRelease) -and [string]::IsNullOrEmpty($right.PreRelease)) { return 0 }
    if ([string]::IsNullOrEmpty($left.PreRelease)) { return 1 }
    if ([string]::IsNullOrEmpty($right.PreRelease)) { return -1 }

    $leftIdentifiers = $left.PreRelease -split '\.'
    $rightIdentifiers = $right.PreRelease -split '\.'
    $length = [Math]::Max($leftIdentifiers.Count, $rightIdentifiers.Count)
    for ($index = 0; $index -lt $length; $index++) {
        if ($index -ge $leftIdentifiers.Count) { return -1 }
        if ($index -ge $rightIdentifiers.Count) { return 1 }
        $leftIsNumber = $leftIdentifiers[$index] -match '^\d+$'
        $rightIsNumber = $rightIdentifiers[$index] -match '^\d+$'
        if ($leftIsNumber -and $rightIsNumber) {
            if ([int64]$leftIdentifiers[$index] -gt [int64]$rightIdentifiers[$index]) { return 1 }
            if ([int64]$leftIdentifiers[$index] -lt [int64]$rightIdentifiers[$index]) { return -1 }
        }
        elseif ($leftIsNumber) { return -1 }
        elseif ($rightIsNumber) { return 1 }
        else {
            $comparison = [string]::CompareOrdinal($leftIdentifiers[$index], $rightIdentifiers[$index])
            if ($comparison -gt 0) { return 1 }
            if ($comparison -lt 0) { return -1 }
        }
    }
    return 0
}

if ($env:OS -ne "Windows_NT") {
    throw "This installer supports Windows only. Use scripts/install.sh on macOS or Linux."
}

if ([string]::IsNullOrWhiteSpace($Version) -or $Version -eq "latest") {
    Write-Host "Buscando a versão mais recente do AgentClip..."
    $release = Invoke-RestMethod -Headers @{ "User-Agent" = "agentclip-installer" } -Uri "https://api.github.com/repos/$repository/releases/latest"
    $Version = $release.tag_name
}
else {
    Write-Host "Versão solicitada: $Version"
}

if ($Version -notmatch '^v\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$') {
    throw "Could not resolve a semantic release tag (got '$Version')."
}

Write-Host "Versão encontrada: $Version"
$installedBinary = Join-Path $InstallDir "agentclip.exe"
$installedVersion = $null
if (Test-Path -Path $installedBinary -PathType Leaf) {
    try {
        $rawVersion = (& $installedBinary version 2>$null | Select-Object -First 1).Trim()
        if ($rawVersion -match '^v?\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$') {
            $installedVersion = if ($rawVersion.StartsWith("v")) { $rawVersion } else { "v$rawVersion" }
        }
    }
    catch {
        $installedVersion = $null
    }
}

if ($installedVersion) {
    Write-Host "Versão instalada encontrada: $installedVersion"
    $comparison = Compare-AgentClipVersion -Candidate $Version -Installed $installedVersion
    if ($comparison -eq 0) {
        Write-Host "AgentClip $installedVersion já está atualizado. Nenhum download necessário."
        exit 0
    }
    if ($comparison -lt 0) {
        Write-Host "A versão instalada ($installedVersion) é mais nova que $Version. Nenhuma alteração realizada."
        exit 0
    }
    Write-Host "Nova versão disponível: $Version (atual: $installedVersion)."
}
else {
    Write-Host "Nenhuma instalação válida foi encontrada em $installedBinary."
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
    Write-Host "Baixando AgentClip $Version para windows/$architecture..."
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
    if ($installedVersion) {
        Write-Host "AgentClip atualizado: $installedVersion → $Version."
    }
    else {
        Write-Host "AgentClip instalado: $Version."
    }
    Write-Host "Binário disponível em $(Join-Path $InstallDir 'agentclip.exe')"

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
