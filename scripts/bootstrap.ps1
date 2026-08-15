$ErrorActionPreference = "Stop"

$DefaultStateDir = if ($env:ProgramData) { Join-Path $env:ProgramData "Executor" } else { "C:\ProgramData\Executor" }
$StateDir = if ($env:EXECUTOR_STATE_DIR) { $env:EXECUTOR_STATE_DIR } else { $DefaultStateDir }
$InstallRoot = if ($env:EXECUTOR_INSTALL_ROOT) { $env:EXECUTOR_INSTALL_ROOT } else { Join-Path $StateDir "installed-services" }
$BundleRoot = if ($env:EXECUTOR_BUNDLE_ROOT) { $env:EXECUTOR_BUNDLE_ROOT } else { Split-Path $PSScriptRoot -Parent }
$BackupRoot = Join-Path $StateDir "service-backups"
$ManifestPath = Join-Path $StateDir "service-manifest.txt"
$OwnedServicesPath = Join-Path $StateDir "owned-services.txt"
$Domain = $env:EXECUTOR_DOMAIN
$ExecutorInstallPath = if ($env:EXECUTOR_INSTALL_BINARY_PATH) { $env:EXECUTOR_INSTALL_BINARY_PATH } else { Join-Path $InstallRoot "executor.exe" }
$ExecutorKillInstallPath = if ($env:EXECUTOR_KILL_INSTALL_BINARY_PATH) { $env:EXECUTOR_KILL_INSTALL_BINARY_PATH } else { Join-Path $InstallRoot "executor-kill.exe" }
$BundledExecutorPath = if ($env:EXECUTOR_BUNDLED_BINARY_PATH) { $env:EXECUTOR_BUNDLED_BINARY_PATH } else { Join-Path $BundleRoot "executor.exe" }
$BundledExecutorKillPath = if ($env:EXECUTOR_BUNDLED_KILL_BINARY_PATH) { $env:EXECUTOR_BUNDLED_KILL_BINARY_PATH } else { Join-Path $BundleRoot "executor-kill.exe" }
$ConfigPath = if ($env:EXECUTOR_CONFIG_PATH) { $env:EXECUTOR_CONFIG_PATH } else { Join-Path $StateDir "config.json" }
$DataDir = if ($env:EXECUTOR_DATA_DIR) { $env:EXECUTOR_DATA_DIR } else { Join-Path $StateDir "data" }
$LogPath = if ($env:EXECUTOR_LOG_PATH) { $env:EXECUTOR_LOG_PATH } else { Join-Path $StateDir "executor.log" }
$CloudflaredBin = if ($env:CLOUDFLARED_BIN) { $env:CLOUDFLARED_BIN } else { "cloudflared.exe" }
$CloudflaredTokenPath = if ($env:CLOUDFLARED_TOKEN_PATH) { $env:CLOUDFLARED_TOKEN_PATH } else { Join-Path $StateDir "cloudflared\executor.token" }
$CloudflaredLogPath = if ($env:CLOUDFLARED_LOG_PATH) { $env:CLOUDFLARED_LOG_PATH } else { Join-Path $StateDir "cloudflared\cloudflared.log" }
$LegacyCloudflaredBackupPath = Join-Path (Split-Path $CloudflaredTokenPath -Parent) "cloudflared-service-imagepath.bak"
$BrokerUser = if ($env:EXECUTOR_BROKER_USER) { $env:EXECUTOR_BROKER_USER } else { "SYSTEM" }
$BrokerGroup = if ($env:EXECUTOR_BROKER_GROUP) { $env:EXECUTOR_BROKER_GROUP } else { "SYSTEM" }
$WindowsAgentService = if ($env:EXECUTOR_WINDOWS_AGENT_SERVICE) { $env:EXECUTOR_WINDOWS_AGENT_SERVICE } else { "NT SERVICE\ExecutorAgent" }
$DesktopUser = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
$AgentUser = if ($env:EXECUTOR_AGENT_USER) { $env:EXECUTOR_AGENT_USER } else { $DesktopUser }
$AgentGroup = if ($env:EXECUTOR_AGENT_GROUP) { $env:EXECUTOR_AGENT_GROUP } else { $DesktopUser }
$DesktopStartup = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\Startup\executor-desktop.cmd"
$DesktopTaskName = "ExecutorDesktop"
$CloudflareAPITokenFile = if ($env:CLOUDFLARE_API_TOKEN_FILE) { $env:CLOUDFLARE_API_TOKEN_FILE } elseif ($env:EXECUTOR_CLOUDFLARE_TOKEN_FILE) { $env:EXECUTOR_CLOUDFLARE_TOKEN_FILE } else { "" }
$CloudflareAccountID = if ($env:CLOUDFLARE_ACCOUNT_ID) { $env:CLOUDFLARE_ACCOUNT_ID } elseif ($env:EXECUTOR_CLOUDFLARE_ACCOUNT_ID) { $env:EXECUTOR_CLOUDFLARE_ACCOUNT_ID } else { "" }
$CloudflareZoneID = if ($env:CLOUDFLARE_ZONE_ID) { $env:CLOUDFLARE_ZONE_ID } elseif ($env:EXECUTOR_CLOUDFLARE_ZONE_ID) { $env:EXECUTOR_CLOUDFLARE_ZONE_ID } else { "" }
$CloudflareTunnelName = if ($env:CLOUDFLARE_TUNNEL_NAME) { $env:CLOUDFLARE_TUNNEL_NAME } elseif ($env:EXECUTOR_CLOUDFLARE_TUNNEL_NAME) { $env:EXECUTOR_CLOUDFLARE_TUNNEL_NAME } else { "" }

if (-not $Domain) {
  throw "Set EXECUTOR_DOMAIN."
}

New-Item -ItemType Directory -Path $StateDir -Force | Out-Null
New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
New-Item -ItemType Directory -Path (Split-Path $CloudflaredTokenPath -Parent) -Force | Out-Null
New-Item -ItemType Directory -Path $InstallRoot -Force | Out-Null
New-Item -ItemType Directory -Path $BackupRoot -Force | Out-Null
Set-Content -Path $ManifestPath -Value $null
if (-not (Test-Path $OwnedServicesPath)) {
  Set-Content -Path $OwnedServicesPath -Value $null
}
$OwnedServices = @(Get-Content -Path $OwnedServicesPath | Where-Object { $_ })
$LegacyOwnedCloudflared = $OwnedServices -contains "cloudflared"
$LegacyCloudflaredService = Get-Service -Name "cloudflared" -ErrorAction SilentlyContinue
$LegacyCloudflaredWasRunning = $LegacyCloudflaredService -and $LegacyCloudflaredService.Status -eq "Running"

function Add-ManifestRecord {
  param(
    [string]$Kind,
    [string]$PathValue,
    [string]$Mode
  )
  Add-Content -Path $ManifestPath -Value ($Kind + "|" + $PathValue + "|" + $Mode)
}

function Backup-ManagedPath {
  param(
    [string]$PathValue,
    [string]$Kind
  )
  $SafeName = $PathValue -replace '[:\\\/ ]', '_'
  $BackupPath = Join-Path $BackupRoot $SafeName
  if (Test-Path $PathValue) {
    Copy-Item -Path $PathValue -Destination $BackupPath -Force
    Add-ManifestRecord -Kind $Kind -PathValue $PathValue -Mode ("restore:" + $BackupPath)
  } else {
    Add-ManifestRecord -Kind $Kind -PathValue $PathValue -Mode "remove"
  }
}

function Install-ManagedFile {
  param(
    [string]$Source,
    [string]$Destination
  )
  if (-not (Test-Path $Source)) {
    throw "Missing bundled file: $Source"
  }
  Backup-ManagedPath -PathValue $Destination -Kind "file"
  New-Item -ItemType Directory -Path (Split-Path $Destination -Parent) -Force | Out-Null
  Copy-Item -Path $Source -Destination $Destination -Force
}

Install-ManagedFile -Source $BundledExecutorPath -Destination $ExecutorInstallPath
Install-ManagedFile -Source $BundledExecutorKillPath -Destination $ExecutorKillInstallPath

$SetupArgs = @("setup", "--domain", $Domain)
if ($CloudflareAPITokenFile) {
  $SetupArgs += @("--cloudflare-token-file", $CloudflareAPITokenFile)
}
if ($CloudflareAccountID) {
  $SetupArgs += @("--cloudflare-account-id", $CloudflareAccountID)
}
if ($CloudflareZoneID) {
  $SetupArgs += @("--cloudflare-zone-id", $CloudflareZoneID)
}
if ($CloudflareTunnelName) {
  $SetupArgs += @("--cloudflare-tunnel-name", $CloudflareTunnelName)
}
& $ExecutorInstallPath @SetupArgs

$ConfigJson = Get-Content -Path $ConfigPath -Raw | ConvertFrom-Json
$Cloudflare = $ConfigJson.cloudflare
if (-not $Cloudflare `
  -or -not $Cloudflare.account_id `
  -or -not $Cloudflare.zone_id `
  -or -not $Cloudflare.tunnel_id `
  -or -not $Cloudflare.tunnel_name `
  -or -not $Cloudflare.dns_record_id `
  -or -not $Cloudflare.token_file_path `
  -or -not $Cloudflare.hostname `
  -or $Cloudflare.token_file_path -ne $CloudflaredTokenPath) {
  throw "Cloudflare setup incomplete. Provide CLOUDFLARE_API_TOKEN_FILE or pre-existing completed Cloudflare metadata before installing services."
}

$TempBundle = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $TempBundle -Force | Out-Null
& $ExecutorInstallPath render-service-bundle `
  --target windows `
  --output $TempBundle `
  --binary-path $ExecutorInstallPath `
  --config-path $ConfigPath `
  --data-dir $DataDir `
  --log-path $LogPath `
  --cloudflared-binary-path $CloudflaredBin `
  --cloudflared-token-path $CloudflaredTokenPath `
  --cloudflared-log-path $CloudflaredLogPath `
  --agent-user $AgentUser `
  --agent-group $AgentGroup `
  --broker-user $BrokerUser `
  --broker-group $BrokerGroup `
  --windows-agent-service $WindowsAgentService

function Record-ServiceState {
  param([string]$Name)
  $Existing = Get-Service -Name $Name -ErrorAction SilentlyContinue
  if (-not $Existing) {
    Add-ManifestRecord -Kind "service" -PathValue $Name -Mode "delete"
    if ($OwnedServices -notcontains $Name) {
      Add-Content -Path $OwnedServicesPath -Value $Name
      $script:OwnedServices += $Name
    }
  } elseif ($OwnedServices -contains $Name) {
	$ImagePathBackup = Join-Path $BackupRoot ("service-imagepath-" + $Name + ".txt")
	$ImagePath = (Get-ItemProperty -Path ("HKLM:\SYSTEM\CurrentControlSet\Services\" + $Name) -Name ImagePath).ImagePath
	$ImagePath | Set-Content -Path $ImagePathBackup -Encoding Unicode
	Add-ManifestRecord -Kind "service-imagepath" -PathValue $Name -Mode ("restore:" + $ImagePathBackup)
    $Mode = if ($Existing.Status -eq "Running") { "keep-running" } else { "keep-stopped" }
    Add-ManifestRecord -Kind "service" -PathValue $Name -Mode $Mode
  } else {
	throw "Refusing to replace unmanaged Windows service: $Name"
  }
}

function Record-ScheduledTaskState {
  param([string]$DesktopTaskName)
  $ExistingTask = Get-ScheduledTask -TaskName $DesktopTaskName -ErrorAction SilentlyContinue
  if ($ExistingTask) {
    $BackupPath = Join-Path $BackupRoot ("scheduled-task-" + $DesktopTaskName + ".xml")
    Export-ScheduledTask -TaskName $DesktopTaskName | Set-Content -Path $BackupPath -Encoding UTF8
    Add-ManifestRecord -Kind "scheduled-task" -PathValue $DesktopTaskName -Mode ("restore:" + $BackupPath)
  } else {
    Add-ManifestRecord -Kind "scheduled-task" -PathValue $DesktopTaskName -Mode "delete"
  }
}

Install-ManagedFile -Source (Join-Path $TempBundle 'windows\install-services.ps1') -Destination (Join-Path $InstallRoot 'windows\install-services.ps1')
Install-ManagedFile -Source (Join-Path $TempBundle 'windows\register-desktop-startup.ps1') -Destination (Join-Path $InstallRoot 'windows\register-desktop-startup.ps1')
Install-ManagedFile -Source (Join-Path $TempBundle 'windows\configure-cloudflared.ps1') -Destination (Join-Path $InstallRoot 'windows\configure-cloudflared.ps1')
Backup-ManagedPath -PathValue $DesktopStartup -Kind "runtime-file"
if (Test-Path $DesktopStartup) {
  Remove-Item -Path $DesktopStartup -Force
}
Record-ScheduledTaskState -DesktopTaskName $DesktopTaskName

Record-ServiceState -Name "ExecutorAgent"
Record-ServiceState -Name "ExecutorBroker"
Record-ServiceState -Name "ExecutorDashboard"
Record-ServiceState -Name "ExecutorCloudflared"

& powershell -ExecutionPolicy Bypass -File (Join-Path $InstallRoot 'windows\install-services.ps1')

if (Test-Path $StateDir) {
  & icacls $StateDir /grant:r "${WindowsAgentService}:(OI)(CI)(M)" "${DesktopUser}:(OI)(CI)(RX)" "SYSTEM:(OI)(CI)(F)" | Out-Null
}
if (Test-Path $DataDir) {
  & icacls $DataDir /grant:r "${WindowsAgentService}:(OI)(CI)(M)" "${DesktopUser}:(OI)(CI)(RX)" "SYSTEM:(OI)(CI)(F)" | Out-Null
}
$CloudflaredDir = Split-Path $CloudflaredTokenPath -Parent
if (Test-Path $CloudflaredDir) {
  & icacls $CloudflaredDir /grant:r "${WindowsAgentService}:(OI)(CI)(M)" "${DesktopUser}:(OI)(CI)(RX)" "SYSTEM:(OI)(CI)(F)" | Out-Null
}
if (Test-Path $ConfigPath) {
  & icacls $ConfigPath /inheritance:r /grant:r "${WindowsAgentService}:(R)" "${DesktopUser}:(R)" "SYSTEM:(F)" | Out-Null
}
$SecretsPath = Join-Path $StateDir "secrets.json"
if (Test-Path $SecretsPath) {
  & icacls $SecretsPath /inheritance:r /grant:r "${WindowsAgentService}:(R)" "${DesktopUser}:(R)" "SYSTEM:(F)" | Out-Null
}
if (Test-Path $CloudflaredTokenPath) {
  & icacls $CloudflaredTokenPath /inheritance:r /grant:r "${WindowsAgentService}:(R)" "${DesktopUser}:(R)" "SYSTEM:(F)" | Out-Null
}

function Start-OrRestartService {
  param([string]$Name)
  $Service = Get-Service -Name $Name -ErrorAction Stop
  if ($Service.Status -eq "Running") {
    Restart-Service -Name $Name -Force -ErrorAction Stop
  } else {
    Start-Service -Name $Name -ErrorAction Stop
  }
}

function Remove-LegacyCloudflaredService {
  if (-not (Get-Service -Name "cloudflared" -ErrorAction SilentlyContinue)) {
    return
  }
  Stop-Service -Name "cloudflared" -Force -ErrorAction Stop
  & sc.exe delete cloudflared | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "Legacy cloudflared service deletion did not complete (sc.exe exit code $LASTEXITCODE)"
  }
  $DeleteDeadline = [DateTime]::UtcNow.AddSeconds(10)
  while ((Get-Service -Name "cloudflared" -ErrorAction SilentlyContinue) -and [DateTime]::UtcNow -lt $DeleteDeadline) {
    Start-Sleep -Milliseconds 100
  }
  if (Get-Service -Name "cloudflared" -ErrorAction SilentlyContinue) {
    throw "Legacy cloudflared service deletion did not complete (service still exists after sc.exe delete)"
  }
}

Start-OrRestartService -Name "ExecutorBroker"
Start-OrRestartService -Name "ExecutorDashboard"
Start-OrRestartService -Name "ExecutorAgent"
& powershell -ExecutionPolicy Bypass -File (Join-Path $InstallRoot 'windows\register-desktop-startup.ps1')
& powershell -ExecutionPolicy Bypass -File (Join-Path $InstallRoot 'windows\configure-cloudflared.ps1')

if ((Test-Path $LegacyCloudflaredBackupPath) -and $LegacyCloudflaredService) {
  Stop-Service -Name "cloudflared" -Force -ErrorAction Stop
  $LegacyImagePath = (Get-Content -Path $LegacyCloudflaredBackupPath -Raw).Trim()
  Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\cloudflared" -Name ImagePath -Value $LegacyImagePath
  if ($LegacyCloudflaredWasRunning) {
    Start-Service -Name "cloudflared" -ErrorAction Stop
  }
  Remove-Item -Path $LegacyCloudflaredBackupPath -Force
  $OwnedServices = @($OwnedServices | Where-Object { $_ -ne "cloudflared" })
  Set-Content -Path $OwnedServicesPath -Value $OwnedServices
} elseif ($LegacyOwnedCloudflared) {
  if ($LegacyCloudflaredService) {
    Remove-LegacyCloudflaredService
  }
  $OwnedServices = @($OwnedServices | Where-Object { $_ -ne "cloudflared" })
  Set-Content -Path $OwnedServicesPath -Value $OwnedServices
}

Write-Host "service bundle installed to $InstallRoot"
Write-Host "manifest path: $ManifestPath"
