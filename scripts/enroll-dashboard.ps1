[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Executor,
  [Parameter(Mandatory = $true)]
  [string]$Url,
  [Parameter(Mandatory = $true)]
  [string]$TokenFile,
  [switch]$TemporaryToken
)

$ErrorActionPreference = "Stop"
$WaitAttemptsText = if ($env:EXECUTOR_DASHBOARD_WAIT_ATTEMPTS) { $env:EXECUTOR_DASHBOARD_WAIT_ATTEMPTS } else { "30" }
$WaitDelayText = if ($env:EXECUTOR_DASHBOARD_WAIT_DELAY) { $env:EXECUTOR_DASHBOARD_WAIT_DELAY } else { "1" }
[int]$WaitAttempts = 0
[double]$WaitDelay = 0
$InvariantCulture = [System.Globalization.CultureInfo]::InvariantCulture
$IntegerStyle = [System.Globalization.NumberStyles]::None
$FloatStyle = [System.Globalization.NumberStyles]::AllowDecimalPoint
$WaitSettingsParsed = [int]::TryParse($WaitAttemptsText, $IntegerStyle, $InvariantCulture, [ref]$WaitAttempts) -and `
  [double]::TryParse($WaitDelayText, $FloatStyle, $InvariantCulture, [ref]$WaitDelay)
$ProtectedFileScript = Join-Path (Split-Path $PSScriptRoot -Parent) "dashboard\scripts\protected-file.ps1"
if (-not (Test-Path -LiteralPath $Executor -PathType Leaf)) { throw "Installed executor binary is unavailable." }
$ParsedUrl = $null
if (-not [System.Uri]::TryCreate($Url, [System.UriKind]::Absolute, [ref]$ParsedUrl) `
  -or $ParsedUrl.Scheme -ne "https" `
  -or $ParsedUrl.PathAndQuery -ne "/") {
  throw "Unified Dashboard URL must be an HTTPS origin."
}
if (-not (Test-Path -LiteralPath $ProtectedFileScript -PathType Leaf)) {
  throw "Windows protected-file validator is unavailable."
}
if (-not $WaitSettingsParsed -or $WaitAttempts -lt 1 -or $WaitAttempts -gt 300 -or $WaitDelay -le 0 -or `
    [double]::IsNaN($WaitDelay) -or [double]::IsInfinity($WaitDelay) -or ($WaitAttempts * $WaitDelay) -gt 300) {
  throw "Dashboard relay wait settings must be positive and must not exceed 300 seconds total."
}
& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenFile
if ($LASTEXITCODE -ne 0) { throw "Dashboard enrollment token file ACL is not protected." }
$OriginalTokenIdentity = (& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenFile -EmitIdentity | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $OriginalTokenIdentity -notmatch '^[0-9A-F]{8}:[0-9A-F]{16}$') {
  throw "Dashboard enrollment token file identity could not be captured safely."
}

$OwnedCopy = ""
$TokenForEnrollment = $TokenFile
try {
  $OwnedCopy = Join-Path ([System.IO.Path]::GetTempPath()) ("executor-dashboard-enrollment-" + [System.Guid]::NewGuid().ToString("N") + ".token")
  New-Item -ItemType File -Path $OwnedCopy -ErrorAction Stop | Out-Null
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $OwnedCopy -Initialize
  if ($LASTEXITCODE -ne 0) { throw "Unable to protect temporary Dashboard enrollment material." }
  [System.IO.File]::WriteAllBytes($OwnedCopy, [System.IO.File]::ReadAllBytes($TokenFile))
  $TokenForEnrollment = $OwnedCopy

  & $Executor dashboard enroll --url $Url --token-file $TokenForEnrollment | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "Unified Dashboard enrollment failed with exit code $LASTEXITCODE." }
  $Ready = $false
  for ($Attempt = 1; $Attempt -le $WaitAttempts; $Attempt++) {
    $DashboardJSON = & $Executor dashboard status --json 2>$null | Out-String
    $DashboardExit = $LASTEXITCODE
    $LocalJSON = & $Executor status --json 2>$null | Out-String
    $LocalExit = $LASTEXITCODE
    if ($DashboardExit -eq 0 -and $LocalExit -eq 0) {
      try {
        $DashboardState = $DashboardJSON | ConvertFrom-Json -ErrorAction Stop
        $LocalState = $LocalJSON | ConvertFrom-Json -ErrorAction Stop
        $Ready = $DashboardState.enrolled -eq $true -and $DashboardState.relay -eq "connected" -and `
          $LocalState.state -eq "armed" -and $LocalState.agent -eq "online" -and $LocalState.broker -eq "online"
      } catch {
        $Ready = $false
      }
    }
    if ($Ready) { break }
    if ($Attempt -lt $WaitAttempts -and $WaitDelay -gt 0) { Start-Sleep -Seconds $WaitDelay }
  }
  if (-not $Ready) { throw "Unified Dashboard relay did not become connected while local services were healthy." }
  if ($TemporaryToken) {
    & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript `
      -Path $TokenFile -DeleteIfIdentity $OriginalTokenIdentity
    if ($LASTEXITCODE -ne 0) {
      throw "Designated Dashboard enrollment token was missing or replaced; refusing deletion."
    }
  }
  Write-Output "Executor is enrolled with Unified Dashboard $Url; local services remain healthy."
} finally {
  if ($OwnedCopy -and (Test-Path -LiteralPath $OwnedCopy)) {
    Remove-Item -LiteralPath $OwnedCopy -Force
  }
}
