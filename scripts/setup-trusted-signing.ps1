#Requires -Version 5.1
<#
.SYNOPSIS
    Downloads the Microsoft Trusted Signing tooling into a local folder.

.DESCRIPTION
    Fetches the two NuGet packages signtool needs to talk to Azure Trusted
    Signing, writes metadata.json from the supplied account details, and prints
    where everything landed. Nothing here needs administrator rights and nothing
    is written outside -OutDir.

    This only prepares the tools. The actual Azure identity comes from the
    environment at signing time (see scripts/sign-windows.ps1).

.PARAMETER OutDir
    Folder that will hold the tooling. Reused when it already exists.

.PARAMETER Endpoint
    Trusted Signing endpoint, e.g. https://eus.codesigning.azure.net/.

.PARAMETER Account
    Trusted Signing account name.

.PARAMETER Profile
    Certificate profile name.

.PARAMETER CorrelationId
    Optional correlation id passed through to the dlib.

.PARAMETER Force
    Re-download the packages even when they are already present.

.PARAMETER SkipSigntool
    Do not download the Windows SDK BuildTools; use one already on the machine.

.PARAMETER WriteGitHubEnv
    Append CLOUDNEST_SIGNTOOL / CLOUDNEST_SIGN_DLIB / CLOUDNEST_SIGN_METADATA to
    $env:GITHUB_ENV so later workflow steps pick them up automatically.

.EXAMPLE
    pwsh scripts/setup-trusted-signing.ps1 -OutDir "$HOME\lib" -Endpoint https://eus.codesigning.azure.net/ -Account MyAccount -Profile MyProfile
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$OutDir,
    [string]$Endpoint = $env:TRUSTED_SIGNING_ENDPOINT,
    [string]$Account = $env:TRUSTED_SIGNING_ACCOUNT,
    [string]$Profile = $env:TRUSTED_SIGNING_PROFILE,
    [string]$CorrelationId = $env:TRUSTED_SIGNING_CORRELATION_ID,
    [switch]$Force,
    [switch]$SkipSigntool,
    [switch]$WriteGitHubEnv
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($Endpoint) -or
    [string]::IsNullOrWhiteSpace($Account) -or
    [string]::IsNullOrWhiteSpace($Profile)) {
    throw 'Trusted Signing account details are missing. Set -Endpoint, -Account and -Profile (or TRUSTED_SIGNING_*).'
}
if ($Endpoint -notmatch '^https://') {
    throw "The Trusted Signing endpoint must be an https URL: $Endpoint"
}

$OutDir = [System.IO.Path]::GetFullPath($OutDir)
New-Item -ItemType Directory -Path $OutDir -Force | Out-Null

$clientDir = Join-Path $OutDir 'Microsoft.Trusted.Signing.Client'
$dlib = Join-Path $clientDir 'bin\x64\Azure.CodeSigning.Dlib.dll'
$metadataPath = Join-Path $clientDir 'bin\x64\metadata.json'

function Install-NuGetPackage {
    param([string]$Id, [string]$FolderName)
    $lower = $Id.ToLowerInvariant()
    $index = Invoke-RestMethod -Uri "https://api.nuget.org/v3-flatcontainer/$lower/index.json" -UseBasicParsing
    # Stable versions only; NuGet lists them oldest to newest.
    $version = @($index.versions | Where-Object { $_ -notmatch '-' } | Select-Object -Last 1)
    if ($version.Count -eq 0) {
        throw "No stable version of $Id was found on nuget.org."
    }
    $version = $version[0]
    $url = "https://api.nuget.org/v3-flatcontainer/$lower/$version/$lower.$version.nupkg"
    $download = Join-Path $OutDir "$lower.$version.zip"
    Write-Host "Downloading $Id $version..."
    Invoke-WebRequest -Uri $url -OutFile $download -UseBasicParsing

    $dest = Join-Path $OutDir $FolderName
    if (Test-Path -LiteralPath $dest) {
        Remove-Item -LiteralPath $dest -Recurse -Force
    }
    New-Item -ItemType Directory -Path $dest -Force | Out-Null
    # A .nupkg is a plain ZIP archive.
    Expand-Archive -LiteralPath $download -DestinationPath $dest -Force
    Remove-Item -LiteralPath $download -Force -ErrorAction SilentlyContinue
    Write-Host "Installed $Id $version to $dest"
}

if ($Force -or -not (Test-Path -LiteralPath $dlib -PathType Leaf)) {
    Install-NuGetPackage -Id 'Microsoft.Trusted.Signing.Client' -FolderName 'Microsoft.Trusted.Signing.Client'
}
if (-not (Test-Path -LiteralPath $dlib -PathType Leaf)) {
    throw "Azure.CodeSigning.Dlib.dll was not found at $dlib after installing the package."
}

# metadata.json is small and account specific, so it is rewritten every run.
$metadata = [ordered]@{
    Endpoint                = $Endpoint
    CodeSigningAccountName  = $Account
    CertificateProfileName  = $Profile
    CorrelationId           = if ([string]::IsNullOrWhiteSpace($CorrelationId)) { $Account } else { $CorrelationId }
}
$metadata | ConvertTo-Json | Set-Content -LiteralPath $metadataPath -Encoding utf8

# signtool lives in the Windows SDK, but the BuildTools NuGet package is enough
# and avoids needing the full SDK on a CI runner. Prefer anything already on
# the machine before downloading ~hundreds of MB.
function Find-SignTool {
    param([string]$Root)
    if (-not $Root -or -not (Test-Path -LiteralPath $Root)) { return $null }
    return Get-ChildItem -LiteralPath $Root -Recurse -Filter 'signtool.exe' -ErrorAction SilentlyContinue |
        Where-Object { $_.Directory.Name -eq 'x64' } |
        Sort-Object FullName -Descending |
        Select-Object -First 1 -ExpandProperty FullName
}

$signTool = $null
if (-not $SkipSigntool) {
    $signTool = Find-SignTool -Root (Join-Path $OutDir 'Microsoft.Windows.SDK.BuildTools\bin')
    if (-not $signTool) {
        $signTool = Find-SignTool -Root (Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin')
    }
    if (-not $signTool) {
        Install-NuGetPackage -Id 'Microsoft.Windows.SDK.BuildTools' -FolderName 'Microsoft.Windows.SDK.BuildTools'
        $signTool = Find-SignTool -Root (Join-Path $OutDir 'Microsoft.Windows.SDK.BuildTools\bin')
    }
}
else {
    $signTool = Find-SignTool -Root (Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin')
}
if (-not $signTool) {
    throw 'No signtool.exe found. Install the Windows SDK or drop -SkipSigntool.'
}

Write-Host 'Trusted Signing tooling is ready:'
Write-Host "  CLOUDNEST_SIGNTOOL=$signTool"
Write-Host "  CLOUDNEST_SIGN_DLIB=$dlib"
Write-Host "  CLOUDNEST_SIGN_METADATA=$metadataPath"

if ($WriteGitHubEnv) {
    if ([string]::IsNullOrWhiteSpace($env:GITHUB_ENV)) {
        throw '-WriteGitHubEnv was requested but GITHUB_ENV is not set.'
    }
    @(
        "CLOUDNEST_SIGNTOOL=$signTool",
        "CLOUDNEST_SIGN_DLIB=$dlib",
        "CLOUDNEST_SIGN_METADATA=$metadataPath",
        'CLOUDNEST_REQUIRE_SIGN=1'
    ) | Out-File -FilePath $env:GITHUB_ENV -Encoding utf8 -Append
}
