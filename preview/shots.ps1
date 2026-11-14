# =============================================================================
# Cloudnest preview — headless Chrome screenshots
# Usage:  powershell -NoProfile -ExecutionPolicy Bypass -File .\shots.ps1
# =============================================================================
$ErrorActionPreference = 'Stop'

$chrome = 'C:\Program Files\Google\Chrome\Application\chrome.exe'
if (-not (Test-Path $chrome)) { throw "Chrome not found at $chrome" }

$root    = Split-Path -Parent $MyInvocation.MyCommand.Path
$chromeProfile = Join-Path $env:TEMP ("cb-shot-" + [guid]::NewGuid().ToString('N'))
$url     = 'file:///' + ((Resolve-Path (Join-Path $root 'index.html')).Path -replace '\\','/')

New-Item -ItemType Directory -Force (Join-Path $root 'shots') | Out-Null

$shots = [ordered]@{
  '01-home-light-zh'      = 'home?lang=zh-Hant&theme=light&static=1'
  '02-home-dark-zh'       = 'home?lang=zh-Hant&theme=dark&static=1'
  '03-home-light-en'      = 'home?lang=en&theme=light&static=1'
  '04-onboarding-zh'      = 'onboarding?lang=zh-Hant&theme=light&static=1'
  '05-wizard-1-zh'        = 'wizard/1?lang=zh-Hant&theme=light&static=1'
  '06-wizard-3-mirror-zh' = 'wizard/3?lang=zh-Hant&theme=light&state=mirror&static=1'
  '07-clouds-add-zh'      = 'clouds/add?lang=zh-Hant&theme=light&static=1'
  '08-files-split-zh'     = 'files?lang=zh-Hant&theme=light&state=selected&static=1'
  '09-restore-dialog-zh'  = 'files/restore?lang=zh-Hant&theme=light&static=1'
  '10-activity-zh'        = 'activity?lang=zh-Hant&theme=light&state=paused&static=1'
  '11-settings-zh'        = 'settings?lang=zh-Hant&theme=light&state=advanced&static=1'
  '12-oauth-wait-zh'      = 'clouds/oauth?lang=zh-Hant&theme=light&static=1'
  '13-activity-split-zh'  = 'activity?lang=zh-Hant&theme=light&state=split&static=1'
}

foreach ($name in $shots.Keys) {
  $out = Join-Path $root ("shots\" + $name + '.png')
  $target = $url + '#' + $shots[$name]
  for ($attempt = 1; $attempt -le 2; $attempt++) {
    Remove-Item $out -ErrorAction SilentlyContinue
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $chrome --headless=new --user-data-dir="$chromeProfile" --no-first-run `
      --no-default-browser-check --disable-gpu --force-color-profile=srgb `
      --force-device-scale-factor=2 --window-size=1440,900 `
      --virtual-time-budget=4000 --run-all-compositor-stages-before-draw `
      --screenshot="$out" "$target" 2>&1 | Out-Null
    $ErrorActionPreference = $prev
    if (Test-Path $out) { break }
    Start-Sleep -Milliseconds 400
  }
  if (-not (Test-Path $out)) { throw "screenshot failed: $name (exit $LASTEXITCODE)" }
  $size = (Get-Item $out).Length / 1KB
  Write-Output ("ok  {0,-24} {1,8:N0} KB" -f $name, $size)
}

Remove-Item -Recurse -Force $chromeProfile -ErrorAction SilentlyContinue
Write-Output ("`n{0} screenshots written to {1}" -f $shots.Count, (Join-Path $root 'shots'))

