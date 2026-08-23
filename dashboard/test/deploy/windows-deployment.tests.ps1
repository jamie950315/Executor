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

  $IdentityBefore = (& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenPath -EmitIdentity | Out-String).Trim()
  if ($LASTEXITCODE -ne 0 -or $IdentityBefore -notmatch '^[0-9A-F]{8}:[0-9A-F]{16}$') {
    throw "Protected-file stable identity capture failed."
  }
  $OriginalTokenPath = "$TokenPath.original"
  $env:EXECUTOR_DEPLOY_CORE_URI = ([System.Uri]::new((Join-Path $DashboardRoot "scripts\lib\deploy-core.mjs"))).AbsoluteUri
  $env:EXECUTOR_TEST_TOKEN_PATH = $TokenPath
  $env:EXECUTOR_TEST_ORIGINAL_TOKEN_PATH = $OriginalTokenPath
  $HeldCredentialProgram = @'
const { rename, writeFile } = await import("node:fs/promises");
const { readProtectedCredential } = await import(process.env.EXECUTOR_DEPLOY_CORE_URI);
const token = await readProtectedCredential(process.env.EXECUTOR_TEST_TOKEN_PATH, {
  platform: "win32",
  afterIdentityVerified: async () => {
    await rename(process.env.EXECUTOR_TEST_TOKEN_PATH, process.env.EXECUTOR_TEST_ORIGINAL_TOKEN_PATH);
    await writeFile(process.env.EXECUTOR_TEST_TOKEN_PATH, "test-only-replacement-api-token\n");
  },
});
try {
  if (token.toString("utf8") !== "test-only-dashboard-api-token") throw new Error("held credential changed after path replacement");
} finally {
  token.fill(0);
}
'@
  try {
    & node.exe --input-type=module --eval $HeldCredentialProgram
    if ($LASTEXITCODE -ne 0) { throw "Node held-handle credential identity validation failed." }
    & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenPath -Initialize
    if ($LASTEXITCODE -ne 0) { throw "Replacement API token ACL initialization failed." }
    $IdentityAfter = (& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TokenPath -EmitIdentity | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $IdentityAfter -eq $IdentityBefore) {
      throw "Protected-file stable identity did not detect pathname replacement."
    }
  } finally {
    Remove-Item -LiteralPath $TokenPath -Force -ErrorAction SilentlyContinue
    Move-Item -LiteralPath $OriginalTokenPath -Destination $TokenPath -Force
    Remove-Item Env:EXECUTOR_DEPLOY_CORE_URI -ErrorAction SilentlyContinue
    Remove-Item Env:EXECUTOR_TEST_TOKEN_PATH -ErrorAction SilentlyContinue
    Remove-Item Env:EXECUTOR_TEST_ORIGINAL_TOKEN_PATH -ErrorAction SilentlyContinue
  }

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

  $JunctionTarget = Join-Path $TemporaryRoot "junction-target"
  $JunctionPath = Join-Path $TemporaryRoot "junction-link"
  New-Item -ItemType Directory -Path $JunctionTarget | Out-Null
  $JunctionToken = Join-Path $JunctionTarget "junction.token"
  [System.IO.File]::WriteAllText($JunctionToken, "test-only-junction-token`n")
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $JunctionToken -Initialize
  if ($LASTEXITCODE -ne 0) { throw "Junction target token initialization failed." }
  & cmd.exe /d /c "mklink /J `"$JunctionPath`" `"$JunctionTarget`"" | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "Junction fixture creation failed." }
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path (Join-Path $JunctionPath "junction.token")
  if ($LASTEXITCODE -eq 0) { throw "Protected-file validation accepted a reparse-point ancestor." }

  $Deploy = Join-Path $RepositoryRoot "scripts\deploy-dashboard-from-source.ps1"

  $env:EXECUTOR_DASHBOARD_HOSTNAME = "dashboard.example.test"
  $env:CLOUDFLARE_ACCOUNT_ID = "0123456789abcdef0123456789abcdef"
  $env:CLOUDFLARE_API_TOKEN_FILE = $TokenPath
  $env:EXECUTOR_DASHBOARD_ALLOWED_EMAIL = "owner@example.test"
  $EnvironmentOutput = & $Deploy validate 2>&1 | Out-String
  if ($LASTEXITCODE -ne 0 -or $EnvironmentOutput -notmatch "no Cloudflare changes were made") {
    throw "PowerShell Dashboard environment defaults failed: $EnvironmentOutput"
  }

  $env:EXECUTOR_DASHBOARD_HOSTNAME = "invalid hostname"
  $env:CLOUDFLARE_ACCOUNT_ID = "invalid-account"
  $env:CLOUDFLARE_API_TOKEN_FILE = "C:\missing\token"
  $env:EXECUTOR_DASHBOARD_ALLOWED_EMAIL = "invalid-email"
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
if "%1"=="dashboard" if "%2"=="status" goto dashboardstatus
if "%1"=="status" goto localstatus
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
:dashboardstatus
if not "%REPLACE_TOKEN_PATH%"=="" if not exist "%REPLACE_TOKEN_PATH%.replaced" (
  move /y "%REPLACE_TOKEN_PATH%" "%REPLACE_TOKEN_PATH%.original" >nul
  echo test-only-replacement-bearer>"%REPLACE_TOKEN_PATH%"
  powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%PROTECTED_FILE_SCRIPT%" -Path "%REPLACE_TOKEN_PATH%" -Initialize >nul
  echo replaced>"%REPLACE_TOKEN_PATH%.replaced"
)
if "%RELAY_DOWN%"=="1" (echo {"enrolled":true,"relay":"disconnected"}) else (echo {"enrolled":true,"relay":"connected"})
exit /b 0
:localstatus
echo {"state":"armed","agent":"online","broker":"online","dashboard":"online"}
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

  $ReplacedEnrollment = Join-Path $TemporaryRoot "replaced-enrollment.token"
  [System.IO.File]::WriteAllText($ReplacedEnrollment, "test-only-dashboard-enrollment-bearer`n")
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $ReplacedEnrollment -Initialize
  if ($LASTEXITCODE -ne 0) { throw "Replacement-race enrollment ACL initialization failed." }
  $env:REPLACE_TOKEN_PATH = $ReplacedEnrollment
  $env:PROTECTED_FILE_SCRIPT = $ProtectedFileScript
  try {
    $ReplacementRejected = $false
    try {
      & $EnrollmentHelper -Executor $FakeExecutor -Url "https://dashboard.example.test" -TokenFile $ReplacedEnrollment -TemporaryToken | Out-Null
    } catch {
      $ReplacementRejected = $true
    }
    if (-not $ReplacementRejected) { throw "Windows replacement race unexpectedly passed deletion." }
    if (-not (Test-Path -LiteralPath $ReplacedEnrollment -PathType Leaf)) {
      throw "Windows enrollment helper deleted the replacement token file."
    }
    if ([System.IO.File]::ReadAllText($ReplacedEnrollment) -notmatch "test-only-replacement-bearer") {
      throw "Windows enrollment helper changed the replacement token file."
    }
  } finally {
    Remove-Item Env:REPLACE_TOKEN_PATH -ErrorAction SilentlyContinue
    Remove-Item Env:PROTECTED_FILE_SCRIPT -ErrorAction SilentlyContinue
  }

  $TimedOutEnrollment = Join-Path $TemporaryRoot "timed-out-enrollment.token"
  [System.IO.File]::WriteAllText($TimedOutEnrollment, "test-only-dashboard-enrollment-bearer`n")
  & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ProtectedFileScript -Path $TimedOutEnrollment -Initialize
  if ($LASTEXITCODE -ne 0) { throw "Timed-out enrollment ACL initialization failed." }
  $env:RELAY_DOWN = "1"
  $env:EXECUTOR_DASHBOARD_WAIT_ATTEMPTS = "2"
  $env:EXECUTOR_DASHBOARD_WAIT_DELAY = "0.01"
  try {
    $TimedOut = $false
    try {
      & $EnrollmentHelper -Executor $FakeExecutor -Url "https://dashboard.example.test" -TokenFile $TimedOutEnrollment -TemporaryToken | Out-Null
    } catch {
      $TimedOut = $true
    }
    if (-not $TimedOut) { throw "Windows disconnected relay unexpectedly passed verification." }
    if (-not (Test-Path -LiteralPath $TimedOutEnrollment)) {
      throw "Windows relay timeout deleted the designated token before success."
    }
  } finally {
    Remove-Item Env:RELAY_DOWN -ErrorAction SilentlyContinue
    Remove-Item Env:EXECUTOR_DASHBOARD_WAIT_ATTEMPTS -ErrorAction SilentlyContinue
    Remove-Item Env:EXECUTOR_DASHBOARD_WAIT_DELAY -ErrorAction SilentlyContinue
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

  foreach ($WaitCase in @(
    @{ Name = "zero attempts"; Attempts = "0"; Delay = "1" },
    @{ Name = "negative attempts"; Attempts = "-1"; Delay = "1" },
    @{ Name = "huge attempts"; Attempts = "999999999999"; Delay = "1" },
    @{ Name = "zero delay"; Attempts = "2"; Delay = "0" },
    @{ Name = "negative delay"; Attempts = "2"; Delay = "-1" },
    @{ Name = "huge delay"; Attempts = "2"; Delay = "999999999999" },
    @{ Name = "excessive total wait"; Attempts = "300"; Delay = "2" }
  )) {
    $env:EXECUTOR_DASHBOARD_WAIT_ATTEMPTS = $WaitCase.Attempts
    $env:EXECUTOR_DASHBOARD_WAIT_DELAY = $WaitCase.Delay
    $Rejected = $false
    try {
      & $EnrollmentHelper -Executor $FakeExecutor -Url "https://dashboard.example.test" -TokenFile $TokenPath | Out-Null
    } catch {
      $Rejected = $true
    }
    if (-not $Rejected) { throw "Windows accepted invalid relay wait settings: $($WaitCase.Name)." }
  }
  Remove-Item Env:EXECUTOR_DASHBOARD_WAIT_ATTEMPTS -ErrorAction SilentlyContinue
  Remove-Item Env:EXECUTOR_DASHBOARD_WAIT_DELAY -ErrorAction SilentlyContinue

  $ForbiddenPackagingRoot = Join-Path $RepositoryRoot "scripts\.executor-windows-packaging-test"
  New-Item -ItemType Directory -Path $ForbiddenPackagingRoot -Force | Out-Null
  [System.IO.File]::WriteAllText((Join-Path $ForbiddenPackagingRoot "runtime.token"), "test-only runtime artifact`n")
  $PreparedBundle = Join-Path $TemporaryRoot "prepared-bundle"
  $SourceDeploy = Join-Path $RepositoryRoot "scripts\deploy-from-source.ps1"
  & $SourceDeploy -PrepareOnly $PreparedBundle | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "Windows source bundle preparation failed." }
  if (Test-Path -LiteralPath (Join-Path $PreparedBundle "scripts\.executor-windows-packaging-test\runtime.token")) {
    throw "Windows source bundle included an untracked runtime token."
  }
} finally {
  foreach ($Name in @(
    "EXECUTOR_DASHBOARD_HOSTNAME", "CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_API_TOKEN_FILE", "EXECUTOR_DASHBOARD_ALLOWED_EMAIL",
    "EXECUTOR_DASHBOARD_WAIT_ATTEMPTS", "EXECUTOR_DASHBOARD_WAIT_DELAY", "REPLACE_TOKEN_PATH", "PROTECTED_FILE_SCRIPT",
    "EXECUTOR_DEPLOY_CORE_URI", "EXECUTOR_TEST_TOKEN_PATH", "EXECUTOR_TEST_ORIGINAL_TOKEN_PATH"
  )) {
    Remove-Item "Env:$Name" -ErrorAction SilentlyContinue
  }
  Remove-Item -LiteralPath (Join-Path $RepositoryRoot "scripts\.executor-windows-packaging-test") -Recurse -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $TemporaryRoot -Recurse -Force -ErrorAction SilentlyContinue
}
