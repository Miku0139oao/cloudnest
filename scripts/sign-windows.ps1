#Requires -Version 5.1
<#
.SYNOPSIS
    Signs Windows PE files with Microsoft Trusted Signing (Azure Code Signing).

.DESCRIPTION
    Uses signtool.exe together with Azure.CodeSigning.Dlib.dll and metadata.json
    (see scripts/setup-trusted-signing.ps1). Azure credentials are picked up by
    the dlib from the regular environment: run this from a machine where you are
    logged in, or set AZURE_TENANT_ID / AZURE_CLIENT_ID / AZURE_CLIENT_SECRET.

    When the tooling is absent the script only warns, so ordinary builds keep
    working, unless -Required is set.

    Every signature is verified with Get-AuthenticodeSignature afterwards, so a
    "success" here means the file is actually trusted by Windows.

.PARAMETER Files
    Files to sign. A missing path is an error.

.PARAMETER Required
    Fail when Trusted Signing is not configured or signing fails.

.EXAMPLE
    pwsh scripts/sign-windows.ps1 -Files dist/Cloudnest/Cloudnest.exe
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string[]]$Files,
    [switch]$Required,
    [string]$SignTool = $env:CLOUDNEST_SIGNTOOL,
    [string]$Dlib = $env:CLOUDNEST_SIGN_DLIB,
    [string]$Metadata = $env:CLOUDNEST_SIGN_METADATA
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($env:CLOUDNEST_SIGN_SKIP -eq '1') {
    Write-Host 'Skipping Authenticode signing because CLOUDNEST_SIGN_SKIP=1'
    if ($Required) {
        throw 'CLOUDNEST_SIGN_SKIP=1 cannot be combined with -Required'
    }
    return
}

function Resolve-ExistingFile {
    param([string[]]$Candidates)
    foreach ($candidate in $Candidates) {
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and (Test-Path -LiteralPath $candidate -PathType Leaf)) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }
    return $null
}

$homeDir = [Environment]::GetFolderPath('UserProfile')
$kitRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
$kitSignTool = $null
if (Test-Path -LiteralPath $kitRoot) {
    $kitSignTool = Get-ChildItem -LiteralPath $kitRoot -Recurse -Filter 'signtool.exe' -ErrorAction SilentlyContinue |
        Where-Object { $_.Directory.Name -eq 'x64' } |
        Sort-Object FullName -Descending |
        Select-Object -First 1 -ExpandProperty FullName
}

$signTool = Resolve-ExistingFile @(
    $SignTool,
    # Layout produced by `nuget install Microsoft.Windows.SDK.BuildTools`.
    (Join-Path $homeDir 'lib\Microsoft.Windows.SDK.BuildTools\bin\10.0.26100.0\x64\signtool.exe'),
    $kitSignTool
)
$dlib = Resolve-ExistingFile @(
    $Dlib,
    (Join-Path $homeDir 'lib\Microsoft.Trusted.Signing.Client\bin\x64\Azure.CodeSigning.Dlib.dll')
)
$metadata = Resolve-ExistingFile @(
    $Metadata,
    (Join-Path $homeDir 'lib\Microsoft.Trusted.Signing.Client\bin\x64\metadata.json')
)

if (-not $signTool -or -not $dlib -or -not $metadata) {
    $missing = @()
    if (-not $signTool) { $missing += 'signtool.exe' }
    if (-not $dlib) { $missing += 'Azure.CodeSigning.Dlib.dll' }
    if (-not $metadata) { $missing += 'metadata.json' }
    $message = "Microsoft Trusted Signing is not configured (missing $($missing -join ', '))"
    if ($Required) {
        throw $message
    }
    Write-Warning "$message; shipping unsigned binaries. Run scripts/setup-trusted-signing.ps1 once, or sign later with scripts/sign-windows.ps1 -Required."
    return
}

foreach ($file in $Files) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) {
        throw "Cannot sign missing file: $file"
    }
}

Write-Host '== Authenticode (Microsoft Trusted Signing) =='
Write-Host "signtool  $signTool"
Write-Host "dlib      $dlib"
Write-Host "metadata  $metadata"

foreach ($file in $Files) {
    $path = (Resolve-Path -LiteralPath $file).Path
    Write-Host "Signing $path"
    & $signTool sign `
        /fd sha256 `
        /td sha256 `
        /tr 'http://timestamp.acs.microsoft.com' `
        /dlib $dlib `
        /dmdf $metadata `
        $path
    if ($LASTEXITCODE -ne 0) {
        throw "signtool failed for $path with exit code $LASTEXITCODE"
    }
    $signature = Get-AuthenticodeSignature -FilePath $path
    if ($signature.Status -ne 'Valid') {
        throw "signature for $path is $($signature.Status)"
    }
    Write-Host ("Signed {0} as {1}" -f $path, $signature.SignerCertificate.Subject)
}
