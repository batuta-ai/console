#requires -Version 5.1
[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$Manifest)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Run only as the already available elevated SSH actor, after ordinary preparation exits.
# This script never enables privileges and never launches a command supplied by a fixture.
$ExpectedSid = 'S-1-5-21-3534651229-1508634349-1091504176-1001'
$Identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$Principal = New-Object Security.Principal.WindowsPrincipal($Identity)
if ($Identity.User.Value -cne $ExpectedSid -or -not $Principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Requires the existing elevated producer with the pinned ordinary SID.'
}
if ($Manifest -cne [IO.Path]::GetFullPath($Manifest) -or [IO.Path]::GetFileName($Manifest) -cne 'manifest.json') {
    throw 'Requires a canonical manifest.json path.'
}
$Root = [IO.Path]::GetDirectoryName($Manifest)
if ([IO.Path]::GetFileName($Root) -cnotmatch '^task43-symlinks-[0-9a-f]{32}$' -or $Root -cnotmatch '^[A-Za-z]:\\') {
    throw 'Unbound private fixture root.'
}

Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Text;
using System.Collections.Generic;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.AccessControl;
using System.Security.Cryptography;
using Microsoft.Win32.SafeHandles;

public sealed class Task43Producer : IDisposable {
    const string Sid = "S-1-5-21-3534651229-1508634349-1091504176-1001";
    readonly List<SafeFileHandle> held = new List<SafeFileHandle>();
    readonly string root;
    [StructLayout(LayoutKind.Sequential)] struct Info {
        public uint Attributes; public System.Runtime.InteropServices.ComTypes.FILETIME Creation, Access, Write;
        public uint Volume, SizeHigh, SizeLow, Links, IndexHigh, IndexLow;
    }
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern SafeFileHandle CreateFile(string p, uint a, uint s, IntPtr sa, uint d, uint f, IntPtr t);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetFileInformationByHandle(SafeFileHandle h, out Info i);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern uint GetFinalPathNameByHandle(SafeFileHandle h, StringBuilder b, uint n, uint f);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] [return: MarshalAs(UnmanagedType.I1)] static extern bool CreateSymbolicLink(string p, string t, uint f);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool DeviceIoControl(SafeFileHandle h, uint c, IntPtr input, uint ins, byte[] output, uint outs, out uint returned, IntPtr overlapped);
    [DllImport("advapi32.dll")] static extern uint GetSecurityInfo(SafeFileHandle h, uint type, uint flags, out IntPtr owner, out IntPtr group, out IntPtr dacl, out IntPtr sacl, out IntPtr sd);
    [DllImport("advapi32.dll")] static extern uint GetSecurityDescriptorLength(IntPtr sd);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool ConvertStringSecurityDescriptorToSecurityDescriptor(string s, uint rev, out IntPtr sd, out uint size);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool GetSecurityDescriptorOwner(IntPtr sd, out IntPtr owner, out bool defaulted);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool GetSecurityDescriptorDacl(IntPtr sd, out bool present, out IntPtr dacl, out bool defaulted);
    [DllImport("advapi32.dll")] static extern uint SetSecurityInfo(SafeFileHandle h, uint type, uint flags, IntPtr owner, IntPtr group, IntPtr dacl, IntPtr sacl);
    [DllImport("kernel32.dll")] static extern IntPtr LocalFree(IntPtr p);
    static void Check(bool ok) { if (!ok) throw new Win32Exception(Marshal.GetLastWin32Error()); }
    static void Status(uint code) { if (code != 0) throw new Win32Exception((int)code); }
    static Info Stat(SafeFileHandle h) { Info i; Check(GetFileInformationByHandle(h, out i)); return i; }
    static string ID(SafeFileHandle h) { Info i=Stat(h); return String.Format("{0:x8}:{1:x8}:{2:x8}",i.Volume,i.IndexHigh,i.IndexLow); }
    static void Private(SafeFileHandle h) {
        IntPtr o,g,d,s,sd; Status(GetSecurityInfo(h,1,5,out o,out g,out d,out s,out sd));
        try {
            byte[] bytes=new byte[GetSecurityDescriptorLength(sd)]; Marshal.Copy(sd,bytes,0,bytes.Length);
            RawSecurityDescriptor raw=new RawSecurityDescriptor(bytes,0);
            if (raw.Owner == null || raw.Owner.Value != Sid || (raw.ControlFlags & ControlFlags.DiscretionaryAclProtected)==0 || raw.DiscretionaryAcl==null || raw.DiscretionaryAcl.Count!=1) throw new InvalidOperationException("Nonprivate fixture descriptor");
            CommonAce ace=raw.DiscretionaryAcl[0] as CommonAce;
            if (ace==null || ace.IsCallback || ace.AceQualifier!=AceQualifier.AccessAllowed || ace.SecurityIdentifier.Value!=Sid || ace.AccessMask!=0x1f01ff || (ace.AceFlags & AceFlags.InheritOnly)!=0) throw new InvalidOperationException("Nonprivate fixture ACE");
            if ((Stat(h).Attributes & 0x10)!=0 && (ace.AceFlags & (AceFlags.ObjectInherit|AceFlags.ContainerInherit))!=(AceFlags.ObjectInherit|AceFlags.ContainerInherit)) throw new InvalidOperationException("Noninheritable directory grant");
        } finally { LocalFree(sd); }
    }
    SafeFileHandle Pin(string path, bool leafLink, bool writable) {
        string volume=Path.GetPathRoot(path);
        if (volume.Length!=3 || path!=Path.GetFullPath(path)) throw new InvalidOperationException("Nonlocal/noncanonical path");
        string current=volume;
        string[] parts=path.Substring(volume.Length).Split('\\');
        for (int n=-1;n<parts.Length;n++) {
            if(n>=0) current=Path.Combine(current,parts[n]);
            bool leaf=n==parts.Length-1;
            uint access=0x20080;
            if(leaf && writable) access|=0xc0000;
            SafeFileHandle h=CreateFile(current,access,3,IntPtr.Zero,3,0x02200000,IntPtr.Zero);
            if(h.IsInvalid) { h.Dispose(); throw new Win32Exception(Marshal.GetLastWin32Error()); }
            held.Add(h);
            Info i=Stat(h);
            if((i.Attributes & 0x400)!=0 && !(leaf && leafLink)) throw new InvalidOperationException("Reparse ancestor/target");
            if(!leaf && (i.Attributes & 0x10)==0) throw new InvalidOperationException("Nondirectory ancestor");
            if(!(leaf && leafLink)) {
                StringBuilder final=new StringBuilder(32768);
                uint len=GetFinalPathNameByHandle(h,final,(uint)final.Capacity,0);
                if(len==0 || len>=final.Capacity || !String.Equals(final.ToString(),@"\\?\"+current,StringComparison.OrdinalIgnoreCase)) throw new InvalidOperationException("Path rebound/drive alias");
            }
            if(current.Equals(root,StringComparison.OrdinalIgnoreCase) || current.StartsWith(root+"\\",StringComparison.OrdinalIgnoreCase)) {
                if(!(leaf && leafLink && writable)) Private(h);
            }
            if(leaf) return h;
        }
        throw new InvalidOperationException("Missing leaf");
    }
    public Task43Producer(string root) { this.root=root; try { Pin(root,false,false); } catch { Dispose(); throw; } }
    public string RootIdentity() { return ID(Pin(root,false,false)); }
    public string ReadManifest() {
        string path=Path.Combine(root,"manifest.json"); Pin(path,false,false);
        // Keep a non-write-sharing read handle until all aliases have been made.
        SafeFileHandle h=CreateFile(path,0x80020000,1,IntPtr.Zero,3,0x00200000,IntPtr.Zero);
        if(h.IsInvalid) { h.Dispose(); throw new Win32Exception(Marshal.GetLastWin32Error()); }
        held.Add(h); Private(h);
        if((Stat(h).Attributes & 0x410)!=0 || Stat(h).Links!=1) throw new InvalidOperationException("Manifest is not a regular single-linked file");
        byte[] bytes=File.ReadAllBytes(path);
        if(bytes.Length>32768) throw new InvalidOperationException("Manifest too large");
        return new UTF8Encoding(false,true).GetString(bytes);
    }
    public void Validate(string alias, string target, bool directory, string hash) {
        string a=Path.Combine(root,alias.Replace('/','\\')), t=Path.Combine(root,target.Replace('/','\\'));
        Pin(Path.GetDirectoryName(a),false,false);
        Info info=Stat(Pin(t,false,false));
        if(((info.Attributes & 0x10)!=0)!=directory || (!directory && info.Links!=1)) throw new InvalidOperationException("Wrong target type");
        if(File.Exists(a) || Directory.Exists(a)) throw new InvalidOperationException("Alias already exists");
        byte[] data;
        if(directory && target=="targets/create_parent") {
            if(Directory.GetFileSystemEntries(t).Length!=0) throw new InvalidOperationException("Create target is not empty");
            data=new byte[0];
        } else {
            if(directory) { t=Path.Combine(t,"profile.md"); Pin(t,false,false); }
            data=File.ReadAllBytes(t);
        }
        using(SHA256 sha=SHA256.Create()) {
            string actual=BitConverter.ToString(sha.ComputeHash(data)).Replace("-","").ToLowerInvariant();
            if(actual!=hash) throw new InvalidOperationException("Synthetic target hash mismatch");
        }
    }
    public void Make(string alias, string target, bool directory) {
        string a=Path.Combine(root,alias.Replace('/','\\')), t=Path.Combine(root,target.Replace('/','\\'));
        // Flags deliberately omit ALLOW_UNPRIVILEGED_CREATE; no privilege is granted or enabled here.
        Check(CreateSymbolicLink(a,t,directory?1u:0u));
        SafeFileHandle h=Pin(a,true,true);
        Info info=Stat(h); byte[] tag=new byte[16384]; uint size;
        Check(DeviceIoControl(h,0x900a8,IntPtr.Zero,0,tag,(uint)tag.Length,out size,IntPtr.Zero));
        if(size<8 || BitConverter.ToUInt32(tag,0)!=0xa000000c || ((info.Attributes&0x10)!=0)!=directory || info.Links!=1) throw new InvalidOperationException("Not an actual expected symbolic link");
        IntPtr sd; uint length;
        Check(ConvertStringSecurityDescriptorToSecurityDescriptor("O:"+Sid+"D:P(A;OICI;FA;;;"+Sid+")",1,out sd,out length));
        try {
            IntPtr owner,dacl; bool defaulted,present;
            Check(GetSecurityDescriptorOwner(sd,out owner,out defaulted));
            Check(GetSecurityDescriptorDacl(sd,out present,out dacl,out defaulted));
            if(!present) throw new InvalidOperationException("Missing private DACL");
            Status(SetSecurityInfo(h,1,0x80000005,owner,IntPtr.Zero,dacl,IntPtr.Zero));
        } finally { LocalFree(sd); }
        Private(h);
        Console.WriteLine("PRODUCED " + alias + " identity=" + ID(h) + " owner=" + Sid + " tag=SYMLINK");
    }
    public void Dispose() { for(int n=held.Count-1;n>=0;n--) held[n].Dispose(); held.Clear(); }
}
'@

function Assert-Fields($Object, [string[]]$Names) {
    $Actual = @($Object.PSObject.Properties.Name | Sort-Object)
    $Expected = @($Names | Sort-Object)
    if (($Actual -join '|') -cne ($Expected -join '|')) { throw 'Unexpected manifest fields.' }
}
function Hash-Body([byte[]]$Body) {
    $Hash = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($Hash.ComputeHash($Body))).Replace('-', '').ToLowerInvariant() }
    finally { $Hash.Dispose() }
}
$Rows = @(
    @('setup_file', 'cases/setup_file/.batuta/profile.md', 'targets/setup_file', $false, "synthetic setup profile`r`n"),
    @('setup_parent', 'cases/setup_parent/.batuta', 'targets/setup_parent', $true, "synthetic setup profile`r`n"),
    @('create_file', 'cases/create_file/existing', 'targets/create_file', $false, "synthetic unchanged private target`0`n"),
    @('create_parent', 'cases/create_parent/parent', 'targets/create_parent', $true, ''),
    @('auth_file', 'cases/auth_file/auth.secret', 'targets/auth_file', $false, ('0123456789abcdef' * 4)),
    @('daemon_lock', 'cases/daemon_lock/consoled.lock', 'targets/daemon_lock', $false, '{"pid":123,"addr":"127.0.0.1:12345","home":"synthetic"}'),
    @('auth_lock', 'cases/auth_lock/auth.secret.lock', 'targets/auth_lock', $false, '{"pid":123,"addr":"127.0.0.1:12345","home":"synthetic"}'),
    @('read_lock', 'cases/read_lock/consoled.lock', 'targets/read_lock', $false, '{"pid":123,"addr":"127.0.0.1:12345","home":"synthetic"}')
)
$Producer = $null
try {
    $Producer = New-Object Task43Producer($Root)
    $M = $Producer.ReadManifest() | ConvertFrom-Json
    Assert-Fields $M @('schema', 'sid', 'root_identity', 'token', 'cases')
    Assert-Fields $M.token @('sid', 'groups', 'privileges', 'type', 'elevation')
    if ($M.schema -cne 'batuta-task43-symlinks-v1' -or $M.sid -cne $ExpectedSid -or $M.root_identity -cne $Producer.RootIdentity() -or $M.token.sid -cne $ExpectedSid -or $M.token.type -ne 1 -or $M.token.elevation -ne 3 -or @($M.cases).Count -ne 8) { throw 'Invalid manifest binding.' }
    for ($I = 0; $I -lt 8; $I++) {
        $C = $M.cases[$I]; $R = $Rows[$I]
        Assert-Fields $C @('id', 'alias', 'target', 'directory', 'sha256')
        $Hash = Hash-Body ([Text.Encoding]::UTF8.GetBytes($R[4]))
        if ($C.id -cne $R[0] -or $C.alias -cne $R[1] -or $C.target -cne $R[2] -or $C.directory -isnot [bool] -or $C.directory -ne $R[3] -or $C.sha256 -cne $Hash) { throw 'Manifest differs from fixed eight-case contract.' }
        $Producer.Validate($R[1], $R[2], $R[3], $Hash)
    }
    foreach ($R in $Rows) { $Producer.Make($R[1], $R[2], $R[3]) }
    Write-Output 'TASK43_PRODUCED=8'
} catch {
    Write-Error "Producer failed; retain fixture for reconciliation: $Root ; $_"
    exit 1
} finally {
    if ($null -ne $Producer) { $Producer.Dispose() }
    $Identity.Dispose()
}
