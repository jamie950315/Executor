$ErrorActionPreference = "Stop"

$StateDir = if ($env:EXECUTOR_STATE_DIR) { $env:EXECUTOR_STATE_DIR } else { "executor-state" }
$InstallRoot = if ($env:EXECUTOR_INSTALL_ROOT) { $env:EXECUTOR_INSTALL_ROOT } else { Join-Path $StateDir "installed-services" }
$BackupRoot = Join-Path $StateDir "service-backups"
$ManifestPath = Join-Path $StateDir "service-manifest.txt"
$CloudflaredTokenPath = if ($env:CLOUDFLARED_TOKEN_PATH) { $env:CLOUDFLARED_TOKEN_PATH } else { Join-Path $StateDir "cloudflared\executor.token" }
$CloudflaredBackupPath = Join-Path (Split-Path $CloudflaredTokenPath -Parent) "cloudflared-service-imagepath.bak"

if (-not (Test-Path $ManifestPath)) {
  Write-Host "no manifest found at $ManifestPath"
  exit 0
}

foreach ($service in @("ExecutorAgent", "ExecutorBroker", "cloudflared")) {
  if (Get-Service -Name $service -ErrorAction SilentlyContinue) {
    try {
      Stop-Service -Name $service -Force -ErrorAction Stop
    } catch {
    }
  }
}

$ManifestEntries = Get-Content -Path $ManifestPath | Where-Object { $_ -and $_.Contains("|") }
foreach ($Entry in $ManifestEntries) {
  $Parts = $Entry.Split("|", 3)
  $Kind = $Parts[0]
  $PathValue = $Parts[1]
  $Mode = $Parts[2]

  switch ($Kind) {
    "service" {
      if ($Mode -eq "delete" -and (Get-Service -Name $PathValue -ErrorAction SilentlyContinue)) {
        & sc.exe delete $PathValue | Out-Null
      }
    }
    "file" {
      if ($Mode.StartsWith("restore:")) {
        $BackupPath = $Mode.Substring(8)
        if (Test-Path $BackupPath) {
          New-Item -ItemType Directory -Path (Split-Path $PathValue -Parent) -Force | Out-Null
          Copy-Item -Path $BackupPath -Destination $PathValue -Force
        }
      } elseif (Test-Path $PathValue) {
        Remove-Item -Path $PathValue -Force
      }
    }
    "runtime-file" {
      if ($Mode.StartsWith("restore:")) {
        $BackupPath = $Mode.Substring(8)
        if (Test-Path $BackupPath) {
          New-Item -ItemType Directory -Path (Split-Path $PathValue -Parent) -Force | Out-Null
          Copy-Item -Path $BackupPath -Destination $PathValue -Force
        }
      } elseif (Test-Path $PathValue) {
        Remove-Item -Path $PathValue -Force
      }
    }
  }
}

if (Test-Path $CloudflaredBackupPath) {
  $ImagePath = Get-Content -Path $CloudflaredBackupPath -Raw
  Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\cloudflared" -Name ImagePath -Value $ImagePath.Trim()
}

Write-Host "restored managed services from $ManifestPath"
