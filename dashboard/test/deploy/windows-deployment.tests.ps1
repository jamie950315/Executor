[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$DashboardRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$RepositoryRoot = Split-Path $DashboardRoot -Parent
$Scripts = @(
  (Join-Path $RepositoryRoot "scripts\deploy-dashboard-from-source.ps1"),
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
} finally {
  Remove-Item -LiteralPath $TemporaryRoot -Recurse -Force -ErrorAction SilentlyContinue
}
