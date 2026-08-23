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

  Copy-Item -Recurse -Path (Join-Path $RootDir "scripts") -Destination (Join-Path $BundleDir "scripts")
  Copy-Item -Recurse -Path (Join-Path $RootDir "docs") -Destination (Join-Path $BundleDir "docs")
  Copy-Item -Path (Join-Path $RootDir "THIRD_PARTY_NOTICES.md") -Destination $BundleDir
  $DashboardDestination = Join-Path $BundleDir "dashboard"
  New-Item -ItemType Directory -Path $DashboardDestination -Force | Out-Null
  foreach ($DashboardFile in @(
    ".gitignore", "eslint.config.js", "index.html", "package.json", "package-lock.json", "tsconfig.json",
    "vite.config.ts", "vitest.config.ts", "vitest.ui.config.ts", "vitest.unit.config.ts",
    "worker-configuration.d.ts", "wrangler.jsonc", "wrangler.test.jsonc", "wrangler.deploy.template.jsonc"
  )) {
    Copy-Item -LiteralPath (Join-Path $RootDir "dashboard\$DashboardFile") -Destination $DashboardDestination
  }
  foreach ($DashboardDirectory in @("migrations", "scripts", "src", "test")) {
    Copy-Item -Recurse -LiteralPath (Join-Path $RootDir "dashboard\$DashboardDirectory") -Destination (Join-Path $DashboardDestination $DashboardDirectory)
  }

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
