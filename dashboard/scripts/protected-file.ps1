[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Path,
  [switch]$Initialize
)

$ErrorActionPreference = "Stop"
$Current = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$CurrentSid = $Current.User.Value
$AdministratorsSid = "S-1-5-32-544"
$SystemSid = "S-1-5-18"
$AllowedSids = @($CurrentSid, $AdministratorsSid, $SystemSid)

function Resolve-Sid {
  param([System.Security.Principal.IdentityReference]$Identity)
  try {
    return $Identity.Translate([System.Security.Principal.SecurityIdentifier]).Value
  } catch {
    return ""
  }
}

if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
  exit 1
}
$Item = Get-Item -LiteralPath $Path -Force
if (($Item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
  exit 1
}

if ($Initialize) {
  $Security = [System.Security.AccessControl.FileSecurity]::new()
  $Security.SetOwner($Current.User)
  $Security.SetAccessRuleProtection($true, $false)
  foreach ($SidValue in $AllowedSids) {
    $Sid = [System.Security.Principal.SecurityIdentifier]::new($SidValue)
    $Rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
      $Sid,
      [System.Security.AccessControl.FileSystemRights]::FullControl,
      [System.Security.AccessControl.AccessControlType]::Allow
    )
    [void]$Security.AddAccessRule($Rule)
  }
  Set-Acl -LiteralPath $Path -AclObject $Security
}

$Acl = Get-Acl -LiteralPath $Path
try {
  $OwnerSid = ([System.Security.Principal.NTAccount]$Acl.Owner).Translate([System.Security.Principal.SecurityIdentifier]).Value
} catch {
  exit 1
}
if ($OwnerSid -ne $CurrentSid -and $OwnerSid -ne $AdministratorsSid) {
  exit 1
}
foreach ($Rule in $Acl.Access) {
  if ($Rule.AccessControlType -ne [System.Security.AccessControl.AccessControlType]::Allow) {
    continue
  }
  $RuleSid = Resolve-Sid -Identity $Rule.IdentityReference
  if (-not $AllowedSids.Contains($RuleSid)) {
    exit 1
  }
}
exit 0
