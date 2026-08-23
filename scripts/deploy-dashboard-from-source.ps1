[CmdletBinding()]
param(
  [Parameter(Position = 0, Mandatory = $true)]
  [ValidateSet("deploy", "validate", "rotate-enrollment", "disable-enrollment", "rollback")]
  [string]$Command,
  [Parameter(Mandatory = $true)]
  [string]$Hostname,
  [Parameter(Mandatory = $true)]
  [string]$AccountId,
  [Parameter(Mandatory = $true)]
  [string]$ApiTokenFile,
  [Parameter(Mandatory = $true)]
  [string]$AllowedEmail,
  [string]$StateFile = "",
  [string]$EnrollmentTokenFile = "",
  [string]$VersionId = ""
)

$ErrorActionPreference = "Stop"
$RootDir = Split-Path $PSScriptRoot -Parent
$DeployScript = Join-Path $RootDir "dashboard\scripts\deploy.mjs"
$Node = Get-Command "node.exe" -ErrorAction SilentlyContinue
$Npm = Get-Command "npm.cmd" -ErrorAction SilentlyContinue
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
if ($EnrollmentTokenFile) { $Arguments += @("--enrollment-token-file", $EnrollmentTokenFile) }
if ($VersionId) { $Arguments += @("--version-id", $VersionId) }

& $Node.Source @Arguments
if ($LASTEXITCODE -ne 0) {
  throw "Unified Dashboard source deployment failed with exit code $LASTEXITCODE."
}
