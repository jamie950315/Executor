[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$DashboardRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$RepositoryRoot = Split-Path $DashboardRoot -Parent
$Scripts = @(
  (Join-Path $RepositoryRoot "scripts\deploy-dashboard-from-source.ps1"),
  (Join-Path $RepositoryRoot "scripts\deploy-from-source.ps1"),
  (Join-Path $RepositoryRoot "scripts\enroll-dashboard.ps1"),
  (Join-Path $DashboardRoot "scripts\protected-file.ps1")
)
foreach ($Script in $Scripts) {
  $Tokens = $null
  $Errors = $null
  [void][System.Management.Automation.Language.Parser]::ParseFile($Script, [ref]$Tokens, [ref]$Errors)
  if ($Errors.Count -ne 0) {
    throw "PowerShell parser rejected $Script`: $($Errors | Out-String)"
  }
}

$TemporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("executor-dashboard-windows-test-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TemporaryRoot | Out-Null
try {
  $TokenPath = Join-Path $TemporaryRoot "cloudflare.token"
  [System.IO.File]::WriteAllText($TokenPath, "test-only-dashboard-api-token`n")
  $ProtectedFileScript = Join-Path $DashboardRoot "scripts\protected-file.ps1"
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenPath -Initialize
  if ($LASTEXITCODE -ne 0) { throw "Protected-file initialization failed." }
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenPath
  if ($LASTEXITCODE -ne 0) { throw "Protected-file validation rejected a protected file." }

  $Acl = Get-Acl -LiteralPath $TokenPath
  $UsersSid = [System.Security.Principal.SecurityIdentifier]::new("S-1-5-32-545")
  $UnsafeRule = [System.Security.AccessControl.FileSystemAccessRule]::new(
    $UsersSid,
    [System.Security.AccessControl.FileSystemRights]::Read,
    [System.Security.AccessControl.AccessControlType]::Allow
  )
  [void]$Acl.AddAccessRule($UnsafeRule)
  Set-Acl -LiteralPath $TokenPath -AclObject $Acl
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenPath
  if ($LASTEXITCODE -eq 0) { throw "Protected-file validation accepted a Users-readable token file." }

  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenPath -Initialize
  if ($LASTEXITCODE -ne 0) { throw "Protected-file ACL repair failed." }
  $Deploy = Join-Path $RepositoryRoot "scripts\deploy-dashboard-from-source.ps1"
  $Output = & $Deploy validate `
    -Hostname "dashboard.example.test" `
    -AccountId "0123456789abcdef0123456789abcdef" `
    -ApiTokenFile $TokenPath `
    -AllowedEmail "owner@example.test" 2>&1 | Out-String
  if ($LASTEXITCODE -ne 0 -or $Output -notmatch "no Cloudflare changes were made") {
    throw "PowerShell Dashboard validation failed: $Output"
  }
  if ($Output.Contains("test-only-dashboard-api-token")) {
    throw "PowerShell Dashboard validation exposed token material."
  }

  $CommandLog = Join-Path $TemporaryRoot "executor-commands.log"
  $FakeExecutor = Join-Path $TemporaryRoot "executor.cmd"
  [System.IO.File]::WriteAllText($FakeExecutor, @"
@echo off
echo %*>>"$CommandLog"
if "%1"=="dashboard" if "%2"=="enroll" goto enroll
if "%1"=="dashboard" if "%2"=="status" exit /b 0
if "%1"=="status" exit /b 0
exit /b 2
:enroll
if "%FAIL_ENROLL%"=="1" exit /b 9
:scan
if "%~1"=="" exit /b 2
if "%~1"=="--token-file" goto consume
shift
goto scan
:consume
del /f /q "%~2"
exit /b 0
"@)
  $EnrollmentHelper = Join-Path $RepositoryRoot "scripts\enroll-dashboard.ps1"
  $EnrollmentOutput = & $EnrollmentHelper `
    -Executor $FakeExecutor `
    -Url "https://dashboard.example.test" `
    -TokenFile $TokenPath 2>&1 | Out-String
  if (-not (Test-Path -LiteralPath $TokenPath)) {
    throw "Windows enrollment helper deleted a persistent source token."
  }
  if ($EnrollmentOutput.Contains("test-only-dashboard-api-token")) {
    throw "Windows enrollment helper exposed bearer material."
  }
  $Commands = [System.IO.File]::ReadAllText($CommandLog)
  foreach ($ExpectedCommand in @("dashboard enroll --url", "dashboard status --json", "status --json")) {
    if (-not $Commands.Contains($ExpectedCommand)) {
      throw "Windows enrollment verification missed $ExpectedCommand."
    }
  }

  $TemporaryEnrollment = Join-Path $TemporaryRoot "temporary-enrollment.token"
  [System.IO.File]::WriteAllText($TemporaryEnrollment, "test-only-dashboard-enrollment-bearer`n")
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TemporaryEnrollment -Initialize
  if ($LASTEXITCODE -ne 0) { throw "Temporary enrollment ACL initialization failed." }
  & $EnrollmentHelper -Executor $FakeExecutor -Url "https://dashboard.example.test" -TokenFile $TemporaryEnrollment -TemporaryToken | Out-Null
  if (Test-Path -LiteralPath $TemporaryEnrollment) {
    throw "Windows enrollment helper retained a designated temporary token after success."
  }

  $FailedEnrollment = Join-Path $TemporaryRoot "failed-enrollment.token"
  [System.IO.File]::WriteAllText($FailedEnrollment, "test-only-dashboard-enrollment-bearer`n")
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $FailedEnrollment -Initialize
  if ($LASTEXITCODE -ne 0) { throw "Failed-enrollment ACL initialization failed." }
  $env:FAIL_ENROLL = "1"
  try {
    $Failed = $false
    try {
      & $EnrollmentHelper -Executor $FakeExecutor -Url "https://dashboard.example.test" -TokenFile $FailedEnrollment -TemporaryToken | Out-Null
    } catch {
      $Failed = $true
    }
    if (-not $Failed) { throw "Windows failed enrollment unexpectedly succeeded." }
    if (-not (Test-Path -LiteralPath $FailedEnrollment)) {
      throw "Windows failed enrollment deleted the designated token before success."
    }
  } finally {
    Remove-Item Env:FAIL_ENROLL -ErrorAction SilentlyContinue
  }
} finally {
  Remove-Item -LiteralPath $TemporaryRoot -Recurse -Force -ErrorAction SilentlyContinue
}
