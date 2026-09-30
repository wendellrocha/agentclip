# Tests the authenticity checks of scripts/install.ps1 without touching the
# network: gh, Invoke-RestMethod and Invoke-WebRequest are replaced by
# functions, which PowerShell prefers over the real commands.
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$installer = Join-Path $root "scripts/install.ps1"
$fixtures = Join-Path $root "scripts/testdata"
$work = Join-Path ([System.IO.Path]::GetTempPath()) ("agentclip-test-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null

$script:failures = 0
function Assert-That {
    param([string]$Description, [bool]$Condition)
    if ($Condition) { Write-Output "ok   $Description" }
    else { Write-Output "FAIL $Description"; $script:failures++ }
}

# Load the installer's functions only.
$env:AGENTCLIP_INSTALLER_LIB = "1"
if (-not $env:AGENTCLIP_INSTALL_DIR) { $env:AGENTCLIP_INSTALL_DIR = Join-Path $work "install" }
. $installer
Remove-Item Env:AGENTCLIP_INSTALLER_LIB

# --- 1. Test-AgentClipStatement against the real GitHub response ------------

$realDigest = "5232f1dfbfac70b8919c4dac7681441fa2ec0c05ec608ada2c7d9d9256a1ee71"
$fixture = Get-Content -Raw (Join-Path $fixtures "attestation-v0.7.1-rc.1.json") | ConvertFrom-Json
$realStatement = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($fixture.attestations[0].bundle.dsseEnvelope.payload))

Assert-That "real payload matches its archive, repository and tag" (Test-AgentClipStatement -Json $realStatement -Digest $realDigest -Repository "wendellrocha/agentclip" -Version "v0.7.1-rc.1")
Assert-That "real payload is refused for another digest" (-not (Test-AgentClipStatement -Json $realStatement -Digest ("0" * 64) -Repository "wendellrocha/agentclip" -Version "v0.7.1-rc.1"))
Assert-That "real payload is refused for another tag" (-not (Test-AgentClipStatement -Json $realStatement -Digest $realDigest -Repository "wendellrocha/agentclip" -Version "v0.7.1"))
Assert-That "real payload is refused for another repository" (-not (Test-AgentClipStatement -Json $realStatement -Digest $realDigest -Repository "attacker/agentclip" -Version "v0.7.1-rc.1"))
Assert-That "malformed JSON is refused" (-not (Test-AgentClipStatement -Json "not json" -Digest $realDigest -Repository "wendellrocha/agentclip" -Version "v0.7.1-rc.1"))

# --- 2. Confirm-AgentClipAuthenticity with stubbed gh and GitHub API ----------

$version = "v0.7.1"
$repository = "example/agentclip"
$digest = "a" * 64
$archive = Join-Path $work "agentclip_${version}_windows_amd64.zip"
Set-Content -Path $archive -Value "archive"

$script:haveGh = $true; $script:ghSupports = $true; $script:ghVerifyExit = 0
$script:haveBundle = $true; $script:apiMode = "ok"; $script:apiBody = $null
$script:ghCalls = @(); $script:apiCalls = @()

function Get-Command {
    param([string]$Name, $ErrorAction)
    if ($Name -eq "gh") {
        if ($script:haveGh) { return [pscustomobject]@{ Name = "gh" } }
        return $null
    }
    Microsoft.PowerShell.Core\Get-Command @PSBoundParameters
}
function gh {
    $script:ghCalls += , ($args -join " ")
    if (($args -join " ") -eq "attestation verify --help") { $global:LASTEXITCODE = $(if ($script:ghSupports) { 0 } else { 1 }); return }
    $global:LASTEXITCODE = $script:ghVerifyExit
    if ($script:ghVerifyExit -ne 0) { "verification failed" }
}
function Invoke-WebRequest {
    param($Uri, $OutFile, [switch]$UseBasicParsing)
    if ($Uri -like "*/attestation.jsonl") {
        if (-not $script:haveBundle) { throw "404" }
        Set-Content -Path $OutFile -Value "bundle"
    }
}
function Invoke-RestMethod {
    param($Uri, $Headers)
    $script:apiCalls += , $Uri
    switch ($script:apiMode) {
        "ok" { return ($script:apiBody | ConvertFrom-Json) }
        "unreachable" { throw "connection refused" }
        default {
            $failure = New-Object System.Exception "HTTP $($script:apiMode)"
            Add-Member -InputObject $failure -MemberType NoteProperty -Name Response -Value ([pscustomobject]@{ StatusCode = [int]$script:apiMode }) -Force
            throw $failure
        }
    }
}

function New-ApiBody {
    param([string]$Digest, [string]$Ref, [string]$Repository, [string]$Path)
    $statement = @{
        subject   = @(@{ name = "x"; digest = @{ sha256 = $Digest } })
        predicate = @{ buildDefinition = @{ externalParameters = @{ workflow = [ordered]@{ ref = $Ref; repository = "https://github.com/$Repository"; path = $Path } } } }
    } | ConvertTo-Json -Depth 10 -Compress
    $encoded = [Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($statement))
    return (@{ attestations = @(@{ bundle = @{ dsseEnvelope = @{ payload = $encoded } } }) } | ConvertTo-Json -Depth 10 -Compress)
}
function Good-Body { New-ApiBody -Digest $digest -Ref "refs/tags/$version" -Repository $repository -Path ".github/workflows/release.yml" }

function Invoke-Scenario {
    param([string]$Description, [bool]$Expected, [scriptblock]$Setup)
    $script:haveGh = $true; $script:ghSupports = $true; $script:ghVerifyExit = 0
    $script:haveBundle = $true; $script:apiMode = "ok"; $script:apiBody = Good-Body
    $script:ghCalls = @(); $script:apiCalls = @()
    Remove-Item Env:AGENTCLIP_SKIP_ATTESTATION -ErrorAction SilentlyContinue
    & $Setup
    # Success output is the returned value; Write-Host lines arrive on stream 6.
    $output = Confirm-AgentClipAuthenticity -Archive $archive -Asset (Split-Path -Leaf $archive) -BaseUrl "https://github.com/$repository/releases/download/$version" -Digest $digest -Repository $repository -Version $version 6>&1
    $script:lastMessages = ($output | Where-Object { $_ -isnot [bool] } | Out-String)
    $result = @($output | Where-Object { $_ -is [bool] })[-1]
    Assert-That $Description ($result -eq $Expected)
}
function Verify-Called { ($script:ghCalls | Where-Object { $_ -like "attestation verify *--bundle*" }).Count -gt 0 }

Invoke-Scenario "gh present and accepting: authentic" $true { }
Assert-That "  gh verified the archive with the release bundle" (Verify-Called)
Assert-That "  the API is not consulted when gh succeeded" ($script:apiCalls.Count -eq 0)

Invoke-Scenario "gh rejecting the archive: refused even if the API would accept" $false { $script:ghVerifyExit = 1 }
Assert-That "  no fallback to the weaker API check" ($script:apiCalls.Count -eq 0)

Invoke-Scenario "gh without attestation support: falls back to the API" $true { $script:ghSupports = $false }
Assert-That "  the API was queried for the archive digest" (($script:apiCalls | Where-Object { $_ -like "*sha256:$digest" }).Count -eq 1)

Invoke-Scenario "release without the bundle asset: falls back to the API" $true { $script:haveBundle = $false }
Assert-That "  the API was queried" ($script:apiCalls.Count -eq 1)

Invoke-Scenario "no gh installed and a matching attestation: authentic" $true { $script:haveGh = $false }
Assert-That "  the API was queried for the archive digest" (($script:apiCalls | Where-Object { $_ -like "*sha256:$digest" }).Count -eq 1)

Invoke-Scenario "attestation made from a branch: refused" $false { $script:haveGh = $false; $script:apiBody = New-ApiBody -Digest $digest -Ref "refs/heads/main" -Repository $repository -Path ".github/workflows/release.yml" }
Invoke-Scenario "attestation made for another tag: refused" $false { $script:haveGh = $false; $script:apiBody = New-ApiBody -Digest $digest -Ref "refs/tags/v9.9.9" -Repository $repository -Path ".github/workflows/release.yml" }
Invoke-Scenario "attestation made in another repository: refused" $false { $script:haveGh = $false; $script:apiBody = New-ApiBody -Digest $digest -Ref "refs/tags/$version" -Repository "attacker/agentclip" -Path ".github/workflows/release.yml" }
Invoke-Scenario "attestation made by another workflow: refused" $false { $script:haveGh = $false; $script:apiBody = New-ApiBody -Digest $digest -Ref "refs/tags/$version" -Repository $repository -Path ".github/workflows/evil.yml" }
Invoke-Scenario "attestation covering another file: refused" $false { $script:haveGh = $false; $script:apiBody = New-ApiBody -Digest ("1" * 64) -Ref "refs/tags/$version" -Repository $repository -Path ".github/workflows/release.yml" }

Invoke-Scenario "no attestation (HTTP 404): refused" $false { $script:haveGh = $false; $script:apiMode = "404" }
Invoke-Scenario "API rate limited (HTTP 403): refused" $false { $script:haveGh = $false; $script:apiMode = "403" }
Assert-That "  the message mentions AGENTCLIP_SKIP_ATTESTATION" ($script:lastMessages -like "*AGENTCLIP_SKIP_ATTESTATION*")
Invoke-Scenario "API unreachable: refused" $false { $script:haveGh = $false; $script:apiMode = "unreachable" }

Invoke-Scenario "explicit opt-out is accepted with only the SHA-256 check" $true { $env:AGENTCLIP_SKIP_ATTESTATION = "1"; $script:apiMode = "404" }
Assert-That "  the opt-out was announced" ($script:lastMessages -like "*AGENTCLIP_SKIP_ATTESTATION=1*")
Assert-That "  neither gh nor the API is consulted after the opt-out" (($script:ghCalls.Count + $script:apiCalls.Count) -eq 0)

$version = "v0.7.0"
Invoke-Scenario "release before v0.7.1-rc.1 is accepted with a warning" $true { $script:apiMode = "404" }
Assert-That "  the warning says it predates attestations" ($script:lastMessages -like "*predates build attestations*")

$env:AGENTCLIP_LANG = "pt-BR"
Invoke-Scenario "the same install in Brazilian Portuguese" $true { $script:apiMode = "404" }
Assert-That "  the warning is in Portuguese" ($script:lastMessages -like "*anterior aos atestados*")
Assert-That "  and no English warning is left" (-not ($script:lastMessages -like "*predates build attestations*"))
$env:AGENTCLIP_LANG = "fr"
Invoke-Scenario "an unsupported language is English" $true { $script:apiMode = "404" }
Assert-That "  the warning is English" ($script:lastMessages -like "*predates build attestations*")
Remove-Item Env:AGENTCLIP_LANG

Remove-Item -Recurse -Force $work
Write-Output ""
if ($script:failures -ne 0) { Write-Error "$($script:failures) check(s) failed"; exit 1 }
Write-Output "all installer checks passed"
