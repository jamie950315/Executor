$ErrorActionPreference = "Stop"

$DefaultStateDir = if ($env:ProgramData) { Join-Path $env:ProgramData "Executor" } else { "C:\ProgramData\Executor" }
$StateDir = if ($env:EXECUTOR_STATE_DIR) { $env:EXECUTOR_STATE_DIR } else { $DefaultStateDir }
$InstallRoot = if ($env:EXECUTOR_INSTALL_ROOT) { $env:EXECUTOR_INSTALL_ROOT } else { Join-Path $StateDir "installed-services" }
$BackupRoot = Join-Path $StateDir "service-backups"
$ManifestPath = Join-Path $StateDir "service-manifest.txt"
$OwnedServicesPath = Join-Path $StateDir "owned-services.txt"
$IsUninstall = $env:EXECUTOR_UNINSTALL -eq "1"
$ServicesToRestart = @()

function Remove-OwnedService {
  param([string]$Name)
  if (-not (Get-Service -Name $Name -ErrorAction SilentlyContinue)) {
    return
  }
  Stop-Service -Name $Name -Force -ErrorAction Stop
  & sc.exe delete $Name | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "service deletion did not complete for $Name (sc.exe exit code $LASTEXITCODE)"
  }
  $DeleteDeadline = [DateTime]::UtcNow.AddSeconds(10)
  while ((Get-Service -Name $Name -ErrorAction SilentlyContinue) -and [DateTime]::UtcNow -lt $DeleteDeadline) {
    Start-Sleep -Milliseconds 100
  }
  if (Get-Service -Name $Name -ErrorAction SilentlyContinue) {
    throw "service deletion did not complete for $Name (service still exists after sc.exe delete)"
  }
}

if (-not (Test-Path $ManifestPath)) {
  Write-Host "no manifest found at $ManifestPath"
  exit 0
}

$ManifestEntries = Get-Content -Path $ManifestPath | Where-Object { $_ -and $_.Contains("|") }
$OwnedServices = if (Test-Path $OwnedServicesPath) { @(Get-Content -Path $OwnedServicesPath | Where-Object { $_ }) } else { @() }
$ManagedServices = @()
foreach ($Entry in $ManifestEntries) {
  $Parts = $Entry.Split("|", 3)
  if ($Parts[0] -eq "service" -and $OwnedServices -contains $Parts[1]) {
    $ManagedServices += $Parts[1]
  }
}
foreach ($service in ($ManagedServices | Select-Object -Unique)) {
  if (Get-Service -Name $service -ErrorAction SilentlyContinue) {
    try {
      Stop-Service -Name $service -Force -ErrorAction Stop
    } catch {
    }
  }
}

foreach ($Entry in $ManifestEntries) {
  $Parts = $Entry.Split("|", 3)
  $Kind = $Parts[0]
  $PathValue = $Parts[1]
  $Mode = $Parts[2]

  switch ($Kind) {
	"service-imagepath" {
	  if (($OwnedServices -contains $PathValue) -and $Mode.StartsWith("restore:") -and (Get-Service -Name $PathValue -ErrorAction SilentlyContinue)) {
		$BackupPath = $Mode.Substring(8)
		if (Test-Path $BackupPath) {
		  $ImagePath = (Get-Content -Path $BackupPath -Raw).Trim()
		  Set-ItemProperty -Path ("HKLM:\SYSTEM\CurrentControlSet\Services\" + $PathValue) -Name ImagePath -Value $ImagePath
		}
	  }
	}
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
      $Owned = $OwnedServices -contains $PathValue
	  $DeleteService = $Owned -and ($Mode -eq "delete" -or $IsUninstall)
      if ($DeleteService) {
		Remove-OwnedService -Name $PathValue
      }
      if ($DeleteService -and (Test-Path $OwnedServicesPath)) {
        $RemainingServices = @(Get-Content -Path $OwnedServicesPath | Where-Object { $_ -and $_ -ne $PathValue })
        Set-Content -Path $OwnedServicesPath -Value $RemainingServices
      }
	  if ($Owned -and -not $DeleteService -and $Mode -eq "keep-running") {
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

foreach ($ServiceName in $ServicesToRestart) {
  if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Start-Service -Name $ServiceName -ErrorAction SilentlyContinue
  }
}

Write-Host "restored managed services from $ManifestPath"
