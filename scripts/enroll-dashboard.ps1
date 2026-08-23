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
& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenFile
if ($LASTEXITCODE -ne 0) { throw "Dashboard enrollment token file ACL is not protected." }

$OwnedCopy = ""
$TokenForEnrollment = $TokenFile
try {
  if (-not $TemporaryToken) {
    $OwnedCopy = Join-Path ([System.IO.Path]::GetTempPath()) ("executor-dashboard-enrollment-" + [System.Guid]::NewGuid().ToString("N") + ".token")
    New-Item -ItemType File -Path $OwnedCopy -ErrorAction Stop | Out-Null
    & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $OwnedCopy -Initialize
    if ($LASTEXITCODE -ne 0) { throw "Unable to protect temporary Dashboard enrollment material." }
    [System.IO.File]::WriteAllBytes($OwnedCopy, [System.IO.File]::ReadAllBytes($TokenFile))
    $TokenForEnrollment = $OwnedCopy
  }

  & $Executor dashboard enroll --url $Url --token-file $TokenForEnrollment | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "Unified Dashboard enrollment failed with exit code $LASTEXITCODE." }
  if (Test-Path -LiteralPath $TokenForEnrollment) {
    Remove-Item -LiteralPath $TokenForEnrollment -Force
  }
  & $Executor dashboard status --json | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "Unified Dashboard enrollment state verification failed." }
  & $Executor status --json | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "Executor service verification failed after Dashboard enrollment." }
  Write-Output "Executor is enrolled with Unified Dashboard $Url; local services remain healthy."
} finally {
  if ($OwnedCopy -and (Test-Path -LiteralPath $OwnedCopy)) {
    Remove-Item -LiteralPath $OwnedCopy -Force
  }
}
