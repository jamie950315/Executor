[CmdletBinding()]
param(
  [string]$PrepareOnly = "",
  [string]$DashboardUrl = $env:EXECUTOR_DASHBOARD_URL,
  [string]$DashboardEnrollmentTokenFile = $env:EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_FILE,
  [switch]$DashboardEnrollmentTokenTemporary
)

$ErrorActionPreference = "Stop"
$RootDir = Split-Path $PSScriptRoot -Parent
$TemporaryDashboardEnrollment = $DashboardEnrollmentTokenTemporary.IsPresent -or $env:EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_TEMPORARY -eq "1"

if ([string]::IsNullOrWhiteSpace($DashboardUrl) -xor [string]::IsNullOrWhiteSpace($DashboardEnrollmentTokenFile)) {
  throw "Dashboard URL and enrollment token file must be supplied together."
}
if ($env:EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_TEMPORARY -and $env:EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_TEMPORARY -notin @("0", "1")) {
  throw "EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_TEMPORARY must be 0 or 1."
}

if (-not (Get-Command "go.exe" -ErrorAction SilentlyContinue)) {
  throw "Required command is not installed or not in PATH: go.exe"
}
if (-not (Get-Command "git.exe" -ErrorAction SilentlyContinue)) {
  throw "Required command is not installed or not in PATH: git.exe"
}

$Architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($Architecture) {
  "x64" { $GoArch = "amd64" }
  "arm64" { $GoArch = "arm64" }
  default { throw "Unsupported Windows architecture: $Architecture" }
}

if (-not $PrepareOnly) {
  if (-not (Get-Command "cloudflared.exe" -ErrorAction SilentlyContinue)) {
    throw "Required command is not installed or not in PATH: cloudflared.exe"
  }
  if (-not $env:EXECUTOR_DOMAIN) {
    throw "Set EXECUTOR_DOMAIN to the public hostname."
  }
  $Identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
  $Principal = [System.Security.Principal.WindowsPrincipal]::new($Identity)
  if (-not $Principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Installation requires an elevated Administrator PowerShell."
  }
}

$WorkDir = ""
try {
  if ($PrepareOnly) {
    $BundleDir = [System.IO.Path]::GetFullPath($PrepareOnly)
    New-Item -ItemType Directory -Path $BundleDir -Force | Out-Null
    if (Get-ChildItem -LiteralPath $BundleDir -Force | Select-Object -First 1) {
      throw "PrepareOnly directory must be empty: $BundleDir"
    }
  } else {
    $WorkDir = Join-Path ([System.IO.Path]::GetTempPath()) ("executor-source-" + [System.Guid]::NewGuid().ToString("N"))
    $BundleDir = Join-Path $WorkDir "bundle"
    New-Item -ItemType Directory -Path $BundleDir -Force | Out-Null
  }

  Push-Location $RootDir
  try {
    & go.exe build -o (Join-Path $BundleDir "executor.exe") ./packaging/cmd/executor
    if ($LASTEXITCODE -ne 0) { throw "Building executor.exe failed with exit code $LASTEXITCODE" }
    & go.exe build -o (Join-Path $BundleDir "executor-kill.exe") ./cmd/executor-kill
    if ($LASTEXITCODE -ne 0) { throw "Building executor-kill.exe failed with exit code $LASTEXITCODE" }
  } finally {
    Pop-Location
  }

  $TrackedPayload = & git.exe -C $RootDir ls-files -- scripts docs dashboard
  if ($LASTEXITCODE -ne 0 -or -not $TrackedPayload) { throw "Unable to enumerate the tracked source payload." }
  foreach ($RelativePath in $TrackedPayload) {
    if ($RelativePath -notmatch '^(scripts|docs|dashboard)/' -or $RelativePath.Contains("..")) {
      throw "Tracked source payload contains an unsafe path."
    }
    $SourcePath = Join-Path $RootDir ($RelativePath -replace '/', '\')
    $SourceItem = Get-Item -LiteralPath $SourcePath -Force
    if ($SourceItem.PSIsContainer -or ($SourceItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw "Tracked source payload contains a non-regular file."
    }
    $DestinationPath = Join-Path $BundleDir ($RelativePath -replace '/', '\')
    New-Item -ItemType Directory -Path (Split-Path $DestinationPath -Parent) -Force | Out-Null
    Copy-Item -LiteralPath $SourcePath -Destination $DestinationPath
  }
  Copy-Item -Path (Join-Path $RootDir "THIRD_PARTY_NOTICES.md") -Destination $BundleDir

  if ($PrepareOnly) {
    Write-Output "Deployable Executor bundle prepared at $BundleDir"
    return
  }

  $env:EXECUTOR_BUNDLE_ROOT = $BundleDir
  & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $BundleDir "scripts\bootstrap.ps1")
  if ($LASTEXITCODE -ne 0) { throw "Executor bootstrap failed with exit code $LASTEXITCODE" }

  $DefaultStateDir = if ($env:ProgramData) { Join-Path $env:ProgramData "Executor" } else { "C:\ProgramData\Executor" }
  $StateDir = if ($env:EXECUTOR_STATE_DIR) { $env:EXECUTOR_STATE_DIR } else { $DefaultStateDir }
  $InstallRoot = if ($env:EXECUTOR_INSTALL_ROOT) { $env:EXECUTOR_INSTALL_ROOT } else { Join-Path $StateDir "installed-services" }
  $InstalledExecutor = if ($env:EXECUTOR_INSTALL_BINARY_PATH) { $env:EXECUTOR_INSTALL_BINARY_PATH } else { Join-Path $InstallRoot "executor.exe" }
  & $InstalledExecutor status
  if ($LASTEXITCODE -ne 0) { throw "Executor status failed with exit code $LASTEXITCODE" }
  & $InstalledExecutor doctor --full
  if ($LASTEXITCODE -ne 0) { throw "Executor doctor --full failed with exit code $LASTEXITCODE" }

  if ($DashboardUrl) {
    $EnrollmentScript = Join-Path $BundleDir "scripts\enroll-dashboard.ps1"
    $EnrollmentArguments = @(
      "-NoProfile",
      "-NonInteractive",
      "-ExecutionPolicy", "Bypass",
      "-File", $EnrollmentScript,
      "-Executor", $InstalledExecutor,
      "-Url", $DashboardUrl,
      "-TokenFile", $DashboardEnrollmentTokenFile
    )
    if ($TemporaryDashboardEnrollment) {
      $EnrollmentArguments += "-TemporaryToken"
    }
    & powershell.exe @EnrollmentArguments
    if ($LASTEXITCODE -ne 0) { throw "Unified Dashboard enrollment failed with exit code $LASTEXITCODE" }
  }
} finally {
  if ($WorkDir -and (Test-Path $WorkDir)) {
    Remove-Item -LiteralPath $WorkDir -Recurse -Force
  }
}
