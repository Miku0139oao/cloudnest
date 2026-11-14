#Requires -Version 7.0
<#
.SYNOPSIS
    Builds the portable Cloudnest distribution and ZIPs it.

.DESCRIPTION
    Creates dist/Cloudnest containing:
      - Cloudnest.exe (renamed from bin/cloudnest.exe)
      - bin/rclone.exe (official rclone Windows amd64 build, SHA256 verified)
      - README.txt and LICENSES/rclone-COPYING.txt
      - runtime/MicrosoftEdgeWebview2Setup.exe (best effort)
      - README-PORTABLE.txt

    Then compresses the folder to dist/Cloudnest-<Version>-win64.zip.

.PARAMETER Version
    Version label used in the ZIP filename. Defaults to 0.1.0.

.PARAMETER OutDir
    Output directory, relative to the repository root unless absolute. Defaults to dist.

.PARAMETER SkipRclone
    Do not download or bundle rclone.exe. The rclone license is still included.

.PARAMETER SkipBuild
    Do not run wails3 build when bin/cloudnest.exe is missing; fail instead.
#>
[CmdletBinding()]
param(
    [string]$Version = '0.1.0',
    [string]$OutDir = 'dist',
    [switch]$SkipRclone,
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'

$RepoRoot = Split-Path -Parent $PSScriptRoot
$SourceExe = Join-Path $RepoRoot 'bin\cloudnest.exe'
$ReadmePath = Join-Path $RepoRoot 'README.md'

if (-not [System.IO.Path]::IsPathRooted($OutDir)) {
    $OutDir = Join-Path $RepoRoot $OutDir
}
$OutDir = [System.IO.Path]::GetFullPath($OutDir)
$PackageDir = Join-Path $OutDir 'Cloudnest'
$ZipPath = Join-Path $OutDir ("Cloudnest-{0}-win64.zip" -f $Version)

# 1. Ensure the application binary exists.
if (-not (Test-Path -LiteralPath $SourceExe -PathType Leaf)) {
    if ($SkipBuild) {
        throw "Missing $SourceExe. Run 'wails3 build' first or omit -SkipBuild."
    }
    if (-not (Get-Command wails3 -ErrorAction SilentlyContinue)) {
        throw "wails3 was not found on PATH. Install it with: go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0"
    }

    Write-Host 'bin\cloudnest.exe not found; running wails3 build...'
    Push-Location $RepoRoot
    try {
        & wails3 build
        if ($LASTEXITCODE -ne 0) {
            throw "wails3 build failed with exit code $LASTEXITCODE."
        }
    }
    finally {
        Pop-Location
    }

    if (-not (Test-Path -LiteralPath $SourceExe -PathType Leaf)) {
        throw "wails3 build did not produce $SourceExe."
    }
}

# 2. Create a clean staging folder.
if (Test-Path -LiteralPath $PackageDir) {
    Remove-Item -LiteralPath $PackageDir -Recurse -Force
}
New-Item -ItemType Directory -Path $PackageDir -Force | Out-Null
New-Item -ItemType Directory -Path $OutDir -Force | Out-Null

# 3. Copy the executable under its user-facing name.
$TargetExe = Join-Path $PackageDir 'Cloudnest.exe'
Copy-Item -LiteralPath $SourceExe -Destination $TargetExe -Force
Write-Host "Copied application to $TargetExe"

# 4-6. Fetch third-party files into the package. Downloads go to a unique,
# empty temp directory; nothing is executed from there.
$WorkDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cloudnest-portable-" + [System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $WorkDir -Force | Out-Null

try {
    if (-not $SkipRclone) {
        $rcloneZipUrl = 'https://downloads.rclone.org/rclone-current-windows-amd64.zip'
        $rcloneSumsUrl = 'https://downloads.rclone.org/SHA256SUMS'
        $rcloneZipPath = Join-Path $WorkDir 'rclone-windows-amd64.zip'
        $rcloneSumsPath = Join-Path $WorkDir 'SHA256SUMS'

        Write-Host 'Downloading rclone...'
        Invoke-WebRequest -Uri $rcloneZipUrl -OutFile $rcloneZipPath -UseBasicParsing

        try {
            Invoke-WebRequest -Uri $rcloneSumsUrl -OutFile $rcloneSumsPath -UseBasicParsing
        }
        catch {
            # rclone currently publishes checksums per release directory instead
            # of at the download root. Resolve the current version from the
            # official version.txt and use that release's signed SHA256SUMS.
            Write-Warning "No checksum list at $rcloneSumsUrl; falling back to the versioned SHA256SUMS."
            $versionTxtPath = Join-Path $WorkDir 'version.txt'
            try {
                Invoke-WebRequest -Uri 'https://downloads.rclone.org/version.txt' -OutFile $versionTxtPath -UseBasicParsing
                $versionText = Get-Content -LiteralPath $versionTxtPath -Raw
                if ($versionText -notmatch '(?i)\bv(\d+\.\d+\.\d+)\b') {
                    throw "Could not parse the rclone version from: $versionText"
                }
                $versionedSumsUrl = "https://downloads.rclone.org/v$($Matches[1])/SHA256SUMS"
                Invoke-WebRequest -Uri $versionedSumsUrl -OutFile $rcloneSumsPath -UseBasicParsing
                Write-Host "Using rclone checksums from $versionedSumsUrl"
            }
            catch {
                throw "Could not download rclone SHA256SUMS from $rcloneSumsUrl or the versioned fallback: $($_.Exception.Message)"
            }
        }

        $actualHash = (Get-FileHash -LiteralPath $rcloneZipPath -Algorithm SHA256).Hash.ToLowerInvariant()
        $verified = $false
        $matchedName = $null
        $windowsEntrySeen = $false

        foreach ($line in Get-Content -LiteralPath $rcloneSumsPath) {
            if ($line -match '^([0-9a-fA-F]{64})\s+\*?(.+?)\s*$') {
                $expectedHash = $Matches[1].ToLowerInvariant()
                $name = $Matches[2].Trim()
                if ($name -match 'windows-amd64\.zip$') {
                    $windowsEntrySeen = $true
                    if ($expectedHash -eq $actualHash) {
                        $verified = $true
                        $matchedName = $name
                        break
                    }
                }
            }
        }

        # Fail closed: only a matching entry from the official checksum list is accepted.
        if (-not $verified) {
            if (-not $windowsEntrySeen) {
                throw 'rclone SHA256 verification failed: SHA256SUMS has no windows-amd64 archive entry.'
            }
            throw "rclone SHA256 verification failed: no windows-amd64 entry matches $actualHash."
        }
        Write-Host "Verified rclone archive: $matchedName"

        $extractDir = Join-Path $WorkDir 'rclone'
        New-Item -ItemType Directory -Path $extractDir -Force | Out-Null
        Expand-Archive -LiteralPath $rcloneZipPath -DestinationPath $extractDir -Force

        $rcloneExe = Get-ChildItem -LiteralPath $extractDir -Filter 'rclone.exe' -Recurse -File |
            Select-Object -First 1
        if (-not $rcloneExe) {
            throw 'rclone.exe was not found in the downloaded rclone archive.'
        }

        $binDir = Join-Path $PackageDir 'bin'
        New-Item -ItemType Directory -Path $binDir -Force | Out-Null
        Copy-Item -LiteralPath $rcloneExe.FullName -Destination (Join-Path $binDir 'rclone.exe') -Force
        Write-Host 'Bundled rclone at bin\rclone.exe'
    }
    else {
        Write-Warning 'Skipping rclone download because -SkipRclone was specified.'
    }

    # 5. Documentation and rclone's MIT license.
    if (-not (Test-Path -LiteralPath $ReadmePath -PathType Leaf)) {
        throw "README.md not found at $ReadmePath."
    }
    Copy-Item -LiteralPath $ReadmePath -Destination (Join-Path $PackageDir 'README.txt') -Force

    $licenseDir = Join-Path $PackageDir 'LICENSES'
    New-Item -ItemType Directory -Path $licenseDir -Force | Out-Null
    $licensePath = Join-Path $licenseDir 'rclone-COPYING.txt'
    Invoke-WebRequest -Uri 'https://raw.githubusercontent.com/rclone/rclone/master/COPYING' -OutFile $licensePath -UseBasicParsing

    $licenseText = Get-Content -LiteralPath $licensePath -Raw
    if ([string]::IsNullOrWhiteSpace($licenseText) -or $licenseText -notmatch 'MIT') {
        throw 'Downloaded rclone license does not look like the expected MIT license text.'
    }

    # 6. WebView2 Evergreen bootstrapper for Windows 10 machines without the runtime.
    $runtimeDir = Join-Path $PackageDir 'runtime'
    New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null
    $webview2Path = Join-Path $runtimeDir 'MicrosoftEdgeWebview2Setup.exe'
    try {
        Invoke-WebRequest -Uri 'https://go.microsoft.com/fwlink/p/?LinkId=2124703' -OutFile $webview2Path -UseBasicParsing
        if ((Get-Item -LiteralPath $webview2Path).Length -eq 0) {
            throw 'Downloaded WebView2 bootstrapper is empty.'
        }
        Write-Host 'Bundled WebView2 Evergreen bootstrapper.'
    }
    catch {
        if (Test-Path -LiteralPath $webview2Path) {
            Remove-Item -LiteralPath $webview2Path -Force -ErrorAction SilentlyContinue
        }
        Write-Warning "Could not download the WebView2 Evergreen bootstrapper: $($_.Exception.Message)"
    }
}
finally {
    if (Test-Path -LiteralPath $WorkDir) {
        Remove-Item -LiteralPath $WorkDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# 7. Portable usage notes.
$portableReadme = @'
Cloudnest portable build
========================

1. Unzip this folder anywhere you have write access (for example the Desktop or a
   USB drive). Avoid read-only locations such as Program Files.

2. Run Cloudnest.exe.

3. Cloudnest keeps its settings, logs and staging files in the data\ folder next to
   Cloudnest.exe. Nothing is written to the registry, and there is no installer or
   uninstaller. To remove Cloudnest, close it and delete this folder.

4. Cloudnest uses rclone for cloud transfers. If bin\rclone.exe is present, that
   bundled copy is used. Otherwise install rclone 1.70 or newer and make sure
   rclone.exe is on PATH. The rclone MIT license is in LICENSES\rclone-COPYING.txt.

Windows 10 note: Cloudnest uses the Microsoft Edge WebView2 runtime. If the app
does not start (for example on a fresh Windows 10 machine), install the WebView2
Evergreen runtime by running runtime\MicrosoftEdgeWebview2Setup.exe, then start
Cloudnest.exe again. Windows 11 normally includes WebView2 already.
'@
Set-Content -LiteralPath (Join-Path $PackageDir 'README-PORTABLE.txt') -Value $portableReadme -Encoding utf8

# 8. Create the final ZIP.
if (Test-Path -LiteralPath $ZipPath) {
    Remove-Item -LiteralPath $ZipPath -Force
}
Compress-Archive -LiteralPath $PackageDir -DestinationPath $ZipPath -CompressionLevel Optimal

$zipSize = (Get-Item -LiteralPath $ZipPath).Length
Write-Host "Created: $ZipPath"
Write-Host ("Size: {0:N2} MB" -f ($zipSize / 1MB))
