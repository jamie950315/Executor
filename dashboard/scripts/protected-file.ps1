[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Path,
  [switch]$Initialize,
  [switch]$Directory,
  [switch]$AncestorsOnly,
  [switch]$EmitIdentity,
  [string]$DeleteIfIdentity = ""
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

if (($EmitIdentity -or -not [string]::IsNullOrWhiteSpace($DeleteIfIdentity)) -and $Directory) { exit 1 }
if ($EmitIdentity -or -not [string]::IsNullOrWhiteSpace($DeleteIfIdentity)) {
  if (-not ("ExecutorProtectedFile.NativeFile" -as [type])) {
    Add-Type -TypeDefinition @"
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

namespace ExecutorProtectedFile {
  public static class NativeFile {
    [StructLayout(LayoutKind.Sequential)]
    private struct ByHandleFileInformation {
      public uint FileAttributes;
      public System.Runtime.InteropServices.ComTypes.FILETIME CreationTime;
      public System.Runtime.InteropServices.ComTypes.FILETIME LastAccessTime;
      public System.Runtime.InteropServices.ComTypes.FILETIME LastWriteTime;
      public uint VolumeSerialNumber;
      public uint FileSizeHigh;
      public uint FileSizeLow;
      public uint NumberOfLinks;
      public uint FileIndexHigh;
      public uint FileIndexLow;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct FileDispositionInformation {
      [MarshalAs(UnmanagedType.U1)] public bool DeleteFile;
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern SafeFileHandle CreateFileW(
      string fileName, uint desiredAccess, uint shareMode, IntPtr securityAttributes,
      uint creationDisposition, uint flagsAndAttributes, IntPtr templateFile);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetFileInformationByHandle(
      SafeFileHandle file, out ByHandleFileInformation information);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool SetFileInformationByHandle(
      SafeFileHandle file, int informationClass, ref FileDispositionInformation information, uint bufferSize);

    public static string Identity(string path) {
      const uint FileReadAttributes = 0x80;
      const uint ShareRead = 0x1;
      const uint ShareWrite = 0x2;
      const uint ShareDelete = 0x4;
      const uint OpenExisting = 3;
      const uint OpenReparsePoint = 0x00200000;
      SafeFileHandle handle = CreateFileW(
        path,
        FileReadAttributes,
        ShareRead | ShareWrite | ShareDelete,
        IntPtr.Zero,
        OpenExisting,
        OpenReparsePoint,
        IntPtr.Zero);
      if (handle.IsInvalid) {
        throw new Win32Exception(Marshal.GetLastWin32Error());
      }
      try {
        ByHandleFileInformation information;
        if (!GetFileInformationByHandle(handle, out information)) {
          throw new Win32Exception(Marshal.GetLastWin32Error());
        }
        ulong fileIndex = ((ulong)information.FileIndexHigh << 32) | information.FileIndexLow;
        string identity = information.VolumeSerialNumber.ToString("X8") + ":" + fileIndex.ToString("X16");
        return identity;
      } finally {
        handle.Dispose();
      }
    }

    public static bool DeleteIfIdentity(string path, string expectedIdentity) {
      const uint FileReadAttributes = 0x80;
      const uint Delete = 0x10000;
      const uint ShareRead = 0x1;
      const uint ShareWrite = 0x2;
      const uint ShareDelete = 0x4;
      const uint OpenExisting = 3;
      const uint OpenReparsePoint = 0x00200000;
      SafeFileHandle handle = CreateFileW(
        path,
        FileReadAttributes | Delete,
        ShareRead | ShareWrite | ShareDelete,
        IntPtr.Zero,
        OpenExisting,
        OpenReparsePoint,
        IntPtr.Zero);
      if (handle.IsInvalid) {
        throw new Win32Exception(Marshal.GetLastWin32Error());
      }
      try {
        ByHandleFileInformation information;
        if (!GetFileInformationByHandle(handle, out information)) {
          throw new Win32Exception(Marshal.GetLastWin32Error());
        }
        ulong fileIndex = ((ulong)information.FileIndexHigh << 32) | information.FileIndexLow;
        string identity = information.VolumeSerialNumber.ToString("X8") + ":" + fileIndex.ToString("X16");
        if (!String.Equals(identity, expectedIdentity, StringComparison.Ordinal)) {
          return false;
        }
        FileDispositionInformation disposition = new FileDispositionInformation { DeleteFile = true };
        uint size = (uint)Marshal.SizeOf(typeof(FileDispositionInformation));
        if (!SetFileInformationByHandle(handle, 4, ref disposition, size)) {
          throw new Win32Exception(Marshal.GetLastWin32Error());
        }
        return true;
      } finally {
        handle.Dispose();
      }
    }
  }
}
"@
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
if ($EmitIdentity) {
  try {
    Write-Output ([ExecutorProtectedFile.NativeFile]::Identity($FullPath))
  } catch {
    exit 1
  }
}
if (-not [string]::IsNullOrWhiteSpace($DeleteIfIdentity)) {
  try {
    if (-not [ExecutorProtectedFile.NativeFile]::DeleteIfIdentity($FullPath, $DeleteIfIdentity)) { exit 1 }
  } catch {
    exit 1
  }
}
exit 0
