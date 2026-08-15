$ErrorActionPreference = "Stop"

$StateDir = if ($env:EXECUTOR_STATE_DIR) { $env:EXECUTOR_STATE_DIR } else { "executor-state" }
& powershell -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot "rollback.ps1")
if (Test-Path $StateDir) {
  Remove-Item -Path $StateDir -Recurse -Force
}
Write-Host "removed $StateDir"
