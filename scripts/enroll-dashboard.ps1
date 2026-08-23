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
$WaitAttempts = if ($env:EXECUTOR_DASHBOARD_WAIT_ATTEMPTS) { [int]$env:EXECUTOR_DASHBOARD_WAIT_ATTEMPTS } else { 30 }
$WaitDelay = if ($env:EXECUTOR_DASHBOARD_WAIT_DELAY) { [double]$env:EXECUTOR_DASHBOARD_WAIT_DELAY } else { 1 }
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
if ($WaitAttempts -lt 1 -or $WaitAttempts -gt 300 -or $WaitDelay -lt 0) {
  throw "Dashboard relay wait settings are invalid."
}
& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenFile
if ($LASTEXITCODE -ne 0) { throw "Dashboard enrollment token file ACL is not protected." }

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
  if ($TemporaryToken -and (Test-Path -LiteralPath $TokenFile)) {
    Remove-Item -LiteralPath $TokenFile -Force
  }
  Write-Output "Executor is enrolled with Unified Dashboard $Url; local services remain healthy."
} finally {
  if ($OwnedCopy -and (Test-Path -LiteralPath $OwnedCopy)) {
    Remove-Item -LiteralPath $OwnedCopy -Force
  }
}
