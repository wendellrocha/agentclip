[CmdletBinding()]
param(
    [string]$Version = $env:AGENTCLIP_VERSION,
    [string]$InstallDir = $(if ($env:AGENTCLIP_INSTALL_DIR) { $env:AGENTCLIP_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\AgentClip\bin" })
)

$ErrorActionPreference = "Stop"
$repository = if ($env:AGENTCLIP_REPOSITORY) { $env:AGENTCLIP_REPOSITORY } else { "wendellrocha/agentclip" }

# Messages are English unless AGENTCLIP_LANG asks for Brazilian Portuguese (pt,
# pt-BR, pt_BR.UTF-8, ...). Errors stay in English on purpose.
$messages = @{
    "verifying_gh"           = @("Verifying authenticity with gh attestation verify...", "Verificando a autenticidade com gh attestation verify...")
    "verifying_api"          = @("Verifying authenticity with the GitHub attestations API...", "Verificando a autenticidade na API de atestados do GitHub...")
    "authentic_gh"           = @("Authenticity confirmed by gh attestation verify.", "Autenticidade confirmada por gh attestation verify.")
    "authentic_api"          = @("Authenticity confirmed by the GitHub attestations API.", "Autenticidade confirmada pela API de atestados do GitHub.")
    "skip_warning"           = @("Warning: attestation check skipped (AGENTCLIP_SKIP_ATTESTATION=1); only the SHA-256 was checked.", "Aviso: verificação de atestado ignorada (AGENTCLIP_SKIP_ATTESTATION=1); apenas o SHA-256 foi conferido.")
    "old_release_warning"    = @("Warning: {0} predates build attestations; only the SHA-256 was checked.", "Aviso: {0} é anterior aos atestados de build; apenas o SHA-256 foi conferido.")
    "full_verification_hint" = @("For full cryptographic verification, install gh and use gh attestation verify.", "Para a verificação criptográfica completa, instale o gh e use gh attestation verify.")
    "looking_up_latest"      = @("Looking up the latest AgentClip version...", "Buscando a versão mais recente do AgentClip...")
    "requested_version"      = @("Requested version: {0}", "Versão solicitada: {0}")
    "found_version"          = @("Version found: {0}", "Versão encontrada: {0}")
    "installed_version"      = @("Installed version found: {0}", "Versão instalada encontrada: {0}")
    "up_to_date"             = @("AgentClip {0} is already up to date. No download needed.", "AgentClip {0} já está atualizado. Nenhum download necessário.")
    "installed_is_newer"     = @("The installed version ({0}) is newer than {1}. Nothing was changed.", "A versão instalada ({0}) é mais nova que {1}. Nenhuma alteração realizada.")
    "update_available"       = @("New version available: {0} (current: {1}).", "Nova versão disponível: {0} (atual: {1}).")
    "no_valid_install"       = @("No valid installation was found at {0}.", "Nenhuma instalação válida foi encontrada em {0}.")
    "downloading"            = @("Downloading AgentClip {0} for windows/{1}...", "Baixando AgentClip {0} para windows/{1}...")
    "updated"                = @("AgentClip updated: {0} → {1}.", "AgentClip atualizado: {0} → {1}.")
    "installed"              = @("AgentClip installed: {0}.", "AgentClip instalado: {0}.")
    "added_to_path"          = @("Added {0} to your user PATH. Open a new terminal after installation.", "{0} foi adicionado ao PATH do seu usuário. Abra um novo terminal depois da instalação.")
    "binary_at"              = @("Binary available at {0}", "Binário disponível em {0}")
}

function Get-AgentClipMessage {
    param([Parameter(Mandatory = $true)][string]$Key, [object[]]$Arguments = @())
    $index = if ("$($env:AGENTCLIP_LANG)".Trim().ToLowerInvariant() -match '^pt([-_.@]|$)') { 1 } else { 0 }
    return [string]::Format($messages[$Key][$index], $Arguments)
}

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

$attestationMinVersion = "v0.7.1-rc.1"
$releaseWorkflowPath = ".github/workflows/release.yml"

# Test-AgentClipStatement succeeds only when an in-toto statement covers the
# archive and was made by the release workflow of this repository on the tag
# being installed.
function Test-AgentClipStatement {
    param(
        [Parameter(Mandatory = $true)][string]$Json,
        [Parameter(Mandatory = $true)][string]$Digest,
        [Parameter(Mandatory = $true)][string]$Repository,
        [Parameter(Mandatory = $true)][string]$Version
    )

    try { $statement = $Json | ConvertFrom-Json } catch { return $false }
    $covered = $false
    foreach ($subject in @($statement.subject)) {
        if ($subject.digest.sha256 -eq $Digest) { $covered = $true }
    }
    if (-not $covered) { return $false }
    $workflow = $statement.predicate.buildDefinition.externalParameters.workflow
    if (-not $workflow) { return $false }
    return ($workflow.repository -ceq "https://github.com/$Repository") -and
        ($workflow.path -ceq $releaseWorkflowPath) -and
        ($workflow.ref -ceq "refs/tags/$Version")
}

# The two verifiers return 0 when the archive is authentic, 1 when it is
# rejected and 2 when the method could not run, so the next one can be tried.
function Test-AgentClipWithGh {
    param(
        [Parameter(Mandatory = $true)][string]$Archive,
        [Parameter(Mandatory = $true)][string]$BaseUrl,
        [Parameter(Mandatory = $true)][string]$Repository,
        [Parameter(Mandatory = $true)][string]$Version
    )

    if (-not (Get-Command gh -ErrorAction SilentlyContinue)) { return 2 }
    # Windows PowerShell 5.1 turns native stderr into a terminating error under
    # "Stop", so native calls run with the default preference.
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & gh attestation verify --help *> $null
        if ($LASTEXITCODE -ne 0) { return 2 }
        $bundle = Join-Path (Split-Path -Parent $Archive) "attestation.jsonl"
        try { Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/attestation.jsonl" -OutFile $bundle } catch { return 2 }
        Write-Host (Get-AgentClipMessage "verifying_gh")
        $identity = "https://github.com/$Repository/.github/workflows/release.yml@refs/tags/$Version"
        $output = & gh attestation verify $Archive --bundle $bundle --repo $Repository --cert-identity $identity 2>&1 | Out-String
        if ($LASTEXITCODE -eq 0) { return 0 }
        Write-Host $output
        return 1
    }
    finally {
        $ErrorActionPreference = $previousPreference
    }
}

function Test-AgentClipWithApi {
    param(
        [Parameter(Mandatory = $true)][string]$Asset,
        [Parameter(Mandatory = $true)][string]$Digest,
        [Parameter(Mandatory = $true)][string]$Repository,
        [Parameter(Mandatory = $true)][string]$Version
    )

    Write-Host (Get-AgentClipMessage "verifying_api")
    try {
        $response = Invoke-RestMethod -Headers @{ "User-Agent" = "agentclip-installer"; "Accept" = "application/vnd.github+json" } `
            -Uri "https://api.github.com/repos/$Repository/attestations/sha256:$Digest"
    }
    catch {
        $status = $null
        if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
        if ($status -eq 404) {
            Write-Host "GitHub has no build attestation for $Asset." -ForegroundColor Red
            return 1
        }
        if ($status) {
            Write-Host "The GitHub attestations API returned HTTP $status." -ForegroundColor Red
        }
        else {
            Write-Host "Could not reach the GitHub attestations API." -ForegroundColor Red
        }
        return 2
    }
    foreach ($attestation in @($response.attestations)) {
        try {
            $json = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($attestation.bundle.dsseEnvelope.payload))
        }
        catch { continue }
        if (Test-AgentClipStatement -Json $json -Digest $Digest -Repository $Repository -Version $Version) { return 0 }
    }
    Write-Host "No GitHub attestation matches $Asset for $Version." -ForegroundColor Red
    return 1
}

function Confirm-AgentClipAuthenticity {
    param(
        [Parameter(Mandatory = $true)][string]$Archive,
        [Parameter(Mandatory = $true)][string]$Asset,
        [Parameter(Mandatory = $true)][string]$BaseUrl,
        [Parameter(Mandatory = $true)][string]$Digest,
        [Parameter(Mandatory = $true)][string]$Repository,
        [Parameter(Mandatory = $true)][string]$Version
    )

    if ($env:AGENTCLIP_SKIP_ATTESTATION -eq "1") {
        Write-Host (Get-AgentClipMessage "skip_warning") -ForegroundColor Yellow
        return $true
    }
    if ((Compare-AgentClipVersion -Candidate $Version -Installed $attestationMinVersion) -lt 0) {
        Write-Host (Get-AgentClipMessage "old_release_warning" @($Version)) -ForegroundColor Yellow
        return $true
    }
    $result = Test-AgentClipWithGh -Archive $Archive -BaseUrl $BaseUrl -Repository $Repository -Version $Version
    if ($result -eq 0) {
        Write-Host (Get-AgentClipMessage "authentic_gh")
        return $true
    }
    if ($result -eq 1) {
        Write-Host "gh attestation verify rejected $Asset." -ForegroundColor Red
        return $false
    }
    $result = Test-AgentClipWithApi -Asset $Asset -Digest $Digest -Repository $Repository -Version $Version
    if ($result -eq 0) {
        Write-Host (Get-AgentClipMessage "authentic_api")
        Write-Host (Get-AgentClipMessage "full_verification_hint")
        return $true
    }
    if ($result -eq 2) {
        Write-Host "Could not verify the build attestation. Set AGENTCLIP_SKIP_ATTESTATION=1 to install with the SHA-256 check only." -ForegroundColor Red
    }
    return $false
}

# Lets the tests load the functions above without installing anything.
if ($env:AGENTCLIP_INSTALLER_LIB -eq "1") { return }

if ($env:OS -ne "Windows_NT") {
    throw "This installer supports Windows only. Use scripts/install.sh on macOS or Linux."
}

if ([string]::IsNullOrWhiteSpace($Version) -or $Version -eq "latest") {
    Write-Host (Get-AgentClipMessage "looking_up_latest")
    $release = Invoke-RestMethod -Headers @{ "User-Agent" = "agentclip-installer" } -Uri "https://api.github.com/repos/$repository/releases/latest"
    $Version = $release.tag_name
}
else {
    Write-Host (Get-AgentClipMessage "requested_version" @($Version))
}

if ($Version -notmatch '^v\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$') {
    throw "Could not resolve a semantic release tag (got '$Version')."
}

Write-Host (Get-AgentClipMessage "found_version" @($Version))
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
    Write-Host (Get-AgentClipMessage "installed_version" @($installedVersion))
    $comparison = Compare-AgentClipVersion -Candidate $Version -Installed $installedVersion
    if ($comparison -eq 0) {
        Write-Host (Get-AgentClipMessage "up_to_date" @($installedVersion))
        exit 0
    }
    if ($comparison -lt 0) {
        Write-Host (Get-AgentClipMessage "installed_is_newer" @($installedVersion, $Version))
        exit 0
    }
    Write-Host (Get-AgentClipMessage "update_available" @($Version, $installedVersion))
}
else {
    Write-Host (Get-AgentClipMessage "no_valid_install" @($installedBinary))
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
    Write-Host (Get-AgentClipMessage "downloading" @($Version, $architecture))
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

    if (-not (Confirm-AgentClipAuthenticity -Archive $archive -Asset $asset -BaseUrl $baseUrl -Digest $actualChecksum -Repository $repository -Version $Version)) {
        throw "Refusing to install ${asset}: its authenticity could not be confirmed."
    }

    Expand-Archive -Path $archive -DestinationPath $temporaryDirectory
    $binary = Join-Path $temporaryDirectory "agentclip_${Version}_windows_${architecture}\agentclip.exe"
    if (-not (Test-Path -Path $binary -PathType Leaf)) {
        throw "Release archive did not contain the expected AgentClip binary."
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Force -Path $binary -Destination (Join-Path $InstallDir "agentclip.exe")
    if ($installedVersion) {
        Write-Host (Get-AgentClipMessage "updated" @($installedVersion, $Version))
    }
    else {
        Write-Host (Get-AgentClipMessage "installed" @($Version))
    }
    Write-Host (Get-AgentClipMessage "binary_at" @((Join-Path $InstallDir 'agentclip.exe')))

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (($userPath -split ';') -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable("Path", (($userPath.TrimEnd(';') + ";" + $InstallDir).TrimStart(';')), "User")
        $env:Path = "$InstallDir;$env:Path"
        Write-Host (Get-AgentClipMessage "added_to_path" @($InstallDir))
    }
}
finally {
    if (Test-Path $temporaryDirectory) {
        Remove-Item -Recurse -Force $temporaryDirectory
    }
}
