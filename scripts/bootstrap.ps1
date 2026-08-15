$ErrorActionPreference = "Stop"

$StateDir = if ($env:EXECUTOR_STATE_DIR) { $env:EXECUTOR_STATE_DIR } else { "executor-state" }
$InstallRoot = if ($env:EXECUTOR_INSTALL_ROOT) { $env:EXECUTOR_INSTALL_ROOT } else { Join-Path $StateDir "installed-services" }
$BackupRoot = Join-Path $StateDir "service-backups"
$ManifestPath = Join-Path $StateDir "service-manifest.txt"
$Domain = $env:EXECUTOR_DOMAIN
$ExecutorBin = if ($env:EXECUTOR_BIN) { $env:EXECUTOR_BIN } else { "executor.exe" }
$ConfigPath = if ($env:EXECUTOR_CONFIG_PATH) { $env:EXECUTOR_CONFIG_PATH } else { Join-Path $StateDir "config.json" }
$DataDir = if ($env:EXECUTOR_DATA_DIR) { $env:EXECUTOR_DATA_DIR } else { Join-Path $StateDir "data" }
$LogPath = if ($env:EXECUTOR_LOG_PATH) { $env:EXECUTOR_LOG_PATH } else { Join-Path $StateDir "executor.log" }
$CloudflaredBin = if ($env:CLOUDFLARED_BIN) { $env:CLOUDFLARED_BIN } else { "cloudflared.exe" }
$CloudflaredTokenPath = if ($env:CLOUDFLARED_TOKEN_PATH) { $env:CLOUDFLARED_TOKEN_PATH } else { Join-Path $StateDir "cloudflared\executor.token" }
$CloudflaredLogPath = if ($env:CLOUDFLARED_LOG_PATH) { $env:CLOUDFLARED_LOG_PATH } else { Join-Path $StateDir "cloudflared\cloudflared.log" }
$AgentUser = if ($env:EXECUTOR_AGENT_USER) { $env:EXECUTOR_AGENT_USER } else { "executor-agent" }
$AgentGroup = if ($env:EXECUTOR_AGENT_GROUP) { $env:EXECUTOR_AGENT_GROUP } else { "executor-agent" }
$BrokerUser = if ($env:EXECUTOR_BROKER_USER) { $env:EXECUTOR_BROKER_USER } else { "SYSTEM" }
$BrokerGroup = if ($env:EXECUTOR_BROKER_GROUP) { $env:EXECUTOR_BROKER_GROUP } else { "SYSTEM" }
$WindowsAgentService = if ($env:EXECUTOR_WINDOWS_AGENT_SERVICE) { $env:EXECUTOR_WINDOWS_AGENT_SERVICE } else { "NT SERVICE\ExecutorAgent" }
$DesktopStartup = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\Startup\executor-desktop.cmd"

if (-not $Domain) {
  throw "Set EXECUTOR_DOMAIN."
}

New-Item -ItemType Directory -Path $StateDir -Force | Out-Null
New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
New-Item -ItemType Directory -Path (Split-Path $CloudflaredTokenPath -Parent) -Force | Out-Null
New-Item -ItemType Directory -Path $InstallRoot -Force | Out-Null
New-Item -ItemType Directory -Path $BackupRoot -Force | Out-Null
Set-Content -Path $ManifestPath -Value $null

& $ExecutorBin setup --domain $Domain

$TempBundle = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $TempBundle -Force | Out-Null
& $ExecutorBin render-service-bundle `
  --target windows `
  --output $TempBundle `
  --binary-path $ExecutorBin `
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
  Backup-ManagedPath -PathValue $Destination -Kind "file"
  New-Item -ItemType Directory -Path (Split-Path $Destination -Parent) -Force | Out-Null
  Copy-Item -Path $Source -Destination $Destination -Force
}

function Record-ServiceState {
  param([string]$Name)
  if (-not (Get-Service -Name $Name -ErrorAction SilentlyContinue)) {
    Add-ManifestRecord -Kind "service" -PathValue $Name -Mode "delete"
  }
}

Install-ManagedFile -Source (Join-Path $TempBundle 'windows\install-services.ps1') -Destination (Join-Path $InstallRoot 'windows\install-services.ps1')
Install-ManagedFile -Source (Join-Path $TempBundle 'windows\register-desktop-startup.ps1') -Destination (Join-Path $InstallRoot 'windows\register-desktop-startup.ps1')
Install-ManagedFile -Source (Join-Path $TempBundle 'windows\configure-cloudflared.ps1') -Destination (Join-Path $InstallRoot 'windows\configure-cloudflared.ps1')
Backup-ManagedPath -PathValue $DesktopStartup -Kind "runtime-file"

Record-ServiceState -Name "ExecutorAgent"
Record-ServiceState -Name "ExecutorBroker"
Record-ServiceState -Name "cloudflared"

& powershell -ExecutionPolicy Bypass -File (Join-Path $InstallRoot 'windows\install-services.ps1')
& powershell -ExecutionPolicy Bypass -File (Join-Path $InstallRoot 'windows\register-desktop-startup.ps1')
& powershell -ExecutionPolicy Bypass -File (Join-Path $InstallRoot 'windows\configure-cloudflared.ps1')

Write-Host "service bundle installed to $InstallRoot"
Write-Host "manifest path: $ManifestPath"
