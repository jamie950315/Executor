$ErrorActionPreference = "Stop"

$DefaultStateDir = if ($env:ProgramData) { Join-Path $env:ProgramData "Executor" } else { "C:\ProgramData\Executor" }
$StateDir = if ($env:EXECUTOR_STATE_DIR) { $env:EXECUTOR_STATE_DIR } else { $DefaultStateDir }
$InstallRoot = if ($env:EXECUTOR_INSTALL_ROOT) { $env:EXECUTOR_INSTALL_ROOT } else { Join-Path $StateDir "installed-services" }
$BackupRoot = Join-Path $StateDir "service-backups"
$ManifestPath = Join-Path $StateDir "service-manifest.txt"
$OwnedServicesPath = Join-Path $StateDir "owned-services.txt"
$CloudflaredTokenPath = if ($env:CLOUDFLARED_TOKEN_PATH) { $env:CLOUDFLARED_TOKEN_PATH } else { Join-Path $StateDir "cloudflared\executor.token" }
$CloudflaredBackupPath = Join-Path (Split-Path $CloudflaredTokenPath -Parent) "cloudflared-service-imagepath.bak"
$IsUninstall = $env:EXECUTOR_UNINSTALL -eq "1"
$ServicesToRestart = @()

if (-not (Test-Path $ManifestPath)) {
  Write-Host "no manifest found at $ManifestPath"
  exit 0
}

foreach ($service in @("ExecutorAgent", "ExecutorBroker", "ExecutorDashboard", "cloudflared")) {
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
    "scheduled-task" {
      if (Get-ScheduledTask -TaskName $PathValue -ErrorAction SilentlyContinue) {
        try {
          Stop-ScheduledTask -TaskName $PathValue -ErrorAction Stop
        } catch {
        }
        Unregister-ScheduledTask -TaskName $PathValue -Confirm:$false -ErrorAction SilentlyContinue
      }
      if ($Mode.StartsWith("restore:")) {
        $BackupPath = $Mode.Substring(8)
        if (Test-Path $BackupPath) {
          $TaskXML = Get-Content -Path $BackupPath -Raw
          Register-ScheduledTask -TaskName $PathValue -Xml $TaskXML -Force | Out-Null
        }
      }
    }
    "service" {
      $Owned = (Test-Path $OwnedServicesPath) -and ((Get-Content -Path $OwnedServicesPath) -contains $PathValue)
      $DeleteService = $Mode -eq "delete" -or ($IsUninstall -and $Owned)
      if ($DeleteService -and (Get-Service -Name $PathValue -ErrorAction SilentlyContinue)) {
        & sc.exe delete $PathValue | Out-Null
      }
      if ($DeleteService -and (Test-Path $OwnedServicesPath)) {
        $RemainingServices = @(Get-Content -Path $OwnedServicesPath | Where-Object { $_ -and $_ -ne $PathValue })
        Set-Content -Path $OwnedServicesPath -Value $RemainingServices
      }
      if (-not $DeleteService -and $Mode -eq "keep-running") {
        $ServicesToRestart += $PathValue
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

if ((Test-Path $CloudflaredBackupPath) -and (Get-Service -Name "cloudflared" -ErrorAction SilentlyContinue)) {
  $ImagePath = Get-Content -Path $CloudflaredBackupPath -Raw
  Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\cloudflared" -Name ImagePath -Value $ImagePath.Trim()
}

foreach ($ServiceName in $ServicesToRestart) {
  if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Start-Service -Name $ServiceName -ErrorAction SilentlyContinue
  }
}

Write-Host "restored managed services from $ManifestPath"
