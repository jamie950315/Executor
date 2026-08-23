[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Path,
  [switch]$Initialize,
  [switch]$Directory,
  [switch]$AncestorsOnly
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

$FullPath = [System.IO.Path]::GetFullPath($Path)
$Root = [System.IO.Path]::GetPathRoot($FullPath)
if ([string]::IsNullOrWhiteSpace($Root)) { exit 1 }
$Relative = $FullPath.Substring($Root.Length)
$Segments = $Relative.Split([char[]]@('\', '/'), [System.StringSplitOptions]::RemoveEmptyEntries)
$CurrentPath = $Root
for ($Index = 0; $Index -lt $Segments.Length; $Index++) {
  $CurrentPath = Join-Path $CurrentPath $Segments[$Index]
  if (-not (Test-Path -LiteralPath $CurrentPath)) { continue }
  $Ancestor = Get-Item -LiteralPath $CurrentPath -Force
  if (($Ancestor.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { exit 1 }
  if ($Index -lt $Segments.Length - 1 -and -not $Ancestor.PSIsContainer) { exit 1 }
}

if ($AncestorsOnly) { exit 0 }
if ($Directory) {
  if (-not (Test-Path -LiteralPath $FullPath -PathType Container)) { exit 1 }
} elseif (-not (Test-Path -LiteralPath $FullPath -PathType Leaf)) {
  exit 1
}
$Item = Get-Item -LiteralPath $FullPath -Force
if (($Item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { exit 1 }

if ($Initialize) {
  if ($Directory) {
    $Security = [System.Security.AccessControl.DirectorySecurity]::new()
  } else {
    $Security = [System.Security.AccessControl.FileSecurity]::new()
  }
  $Security.SetOwner($Current.User)
  $Security.SetAccessRuleProtection($true, $false)
  foreach ($SidValue in $AllowedSids) {
    $Sid = [System.Security.Principal.SecurityIdentifier]::new($SidValue)
    if ($Directory) {
      $Rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        $Sid,
        [System.Security.AccessControl.FileSystemRights]::FullControl,
        [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [System.Security.AccessControl.InheritanceFlags]::ObjectInherit,
        [System.Security.AccessControl.PropagationFlags]::None,
        [System.Security.AccessControl.AccessControlType]::Allow
      )
    } else {
      $Rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        $Sid,
        [System.Security.AccessControl.FileSystemRights]::FullControl,
        [System.Security.AccessControl.AccessControlType]::Allow
      )
    }
    [void]$Security.AddAccessRule($Rule)
  }
  Set-Acl -LiteralPath $FullPath -AclObject $Security
}

$Acl = Get-Acl -LiteralPath $FullPath
try {
  try {
    $OwnerSid = [System.Security.Principal.SecurityIdentifier]::new($Acl.Owner).Value
  } catch {
    $OwnerSid = ([System.Security.Principal.NTAccount]$Acl.Owner).Translate([System.Security.Principal.SecurityIdentifier]).Value
  }
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
