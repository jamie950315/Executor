$ErrorActionPreference = "Stop"

$DefaultStateDir = if ($env:ProgramData) { Join-Path $env:ProgramData "Executor" } else { "C:\ProgramData\Executor" }
$StateDir = if ($env:EXECUTOR_STATE_DIR) { $env:EXECUTOR_STATE_DIR } else { $DefaultStateDir }
$env:EXECUTOR_UNINSTALL = "1"
& powershell -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot "rollback.ps1")
if (Test-Path $StateDir) {
  Remove-Item -Path $StateDir -Recurse -Force
}
Write-Host "removed $StateDir"
