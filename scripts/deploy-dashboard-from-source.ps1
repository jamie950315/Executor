[CmdletBinding()]
param(
  [Parameter(Position = 0, Mandatory = $true)]
  [ValidateSet("deploy", "validate", "rotate-enrollment", "disable-enrollment", "rollback")]
  [string]$Command,
  [string]$Hostname = $env:EXECUTOR_DASHBOARD_HOSTNAME,
  [string]$AccountId = $env:CLOUDFLARE_ACCOUNT_ID,
  [string]$ApiTokenFile = $env:CLOUDFLARE_API_TOKEN_FILE,
  [string]$AllowedEmail = $env:EXECUTOR_DASHBOARD_ALLOWED_EMAIL,
  [string]$AccessTeamDomain = $env:EXECUTOR_DASHBOARD_ACCESS_TEAM_DOMAIN,
  [string]$StateFile = $env:EXECUTOR_DASHBOARD_STATE_FILE,
  [string]$EnrollmentTokenFile = $env:EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_FILE,
  [string]$VersionId = $env:EXECUTOR_DASHBOARD_VERSION_ID
)

$ErrorActionPreference = "Stop"
$RootDir = Split-Path $PSScriptRoot -Parent
$DeployScript = Join-Path $RootDir "dashboard\scripts\deploy.mjs"
$Node = Get-Command "node.exe" -ErrorAction SilentlyContinue
$Npm = Get-Command "npm.cmd" -ErrorAction SilentlyContinue
if ([string]::IsNullOrWhiteSpace($Hostname) -or [string]::IsNullOrWhiteSpace($AccountId) -or `
    [string]::IsNullOrWhiteSpace($ApiTokenFile) -or [string]::IsNullOrWhiteSpace($AllowedEmail)) {
  throw "Dashboard hostname, account ID, API token file, and allowed email are required."
}
if (-not $Node) { throw "Node is required to deploy the Unified Dashboard." }
if (-not $Npm) { throw "npm is required to deploy the Unified Dashboard." }
if (-not (Test-Path -LiteralPath $DeployScript -PathType Leaf)) {
  throw "Dashboard source deployment payload is incomplete."
}

$Arguments = @(
  $DeployScript,
  $Command,
  "--hostname", $Hostname,
  "--account-id", $AccountId,
  "--api-token-file", $ApiTokenFile,
  "--allowed-email", $AllowedEmail
)
if ($StateFile) { $Arguments += @("--state-file", $StateFile) }
if ($AccessTeamDomain) { $Arguments += @("--access-team-domain", $AccessTeamDomain) }
if ($EnrollmentTokenFile) { $Arguments += @("--enrollment-token-file", $EnrollmentTokenFile) }
if ($VersionId) { $Arguments += @("--version-id", $VersionId) }

& $Node.Source @Arguments
if ($LASTEXITCODE -ne 0) {
  throw "Unified Dashboard source deployment failed with exit code $LASTEXITCODE."
}
