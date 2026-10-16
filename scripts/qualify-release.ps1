#Requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Archive,
    [Parameter(Mandatory)][string]$Checksums,
    [Parameter(Mandatory)][string]$ExpectedVersion,
    [Parameter(Mandatory)][string]$ExpectedRevision,
    [Parameter(Mandatory)][string]$Evidence
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $IsWindows -or [Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture -ne 'X64') {
    throw 'Native Windows x64 PowerShell 7.4+ required; native proof pending on other hosts'
}
if ($ExpectedVersion -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._+-]{0,79}$' -or $ExpectedRevision -cnotmatch '^[a-f0-9]{40}$') {
    throw 'Explicit release version and full lowercase revision required'
}

# Handles, not subsequent PID lookups, authorize observations and termination.
Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Text;
using System.Diagnostics;
using System.Threading;
using System.Threading.Tasks;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
public static class BundleNative {
    [DllImport("kernel32.dll", SetLastError=true)]
    static extern uint WaitForSingleObject(SafeProcessHandle handle, uint milliseconds);
    [DllImport("kernel32.dll", SetLastError=true)]
    static extern bool TerminateProcess(SafeProcessHandle handle, uint code);
    [DllImport("kernel32.dll", SetLastError=true)]
    static extern bool GetProcessTimes(SafeProcessHandle handle, out long created, out long exited, out long kernel, out long user);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    static extern bool QueryFullProcessImageName(SafeProcessHandle handle, uint flags, StringBuilder name, ref int size);
    [DllImport("ntdll.dll")]
    static extern int NtQueryInformationProcess(SafeProcessHandle handle, int kind, IntPtr buffer, int size, out int needed);
    [DllImport("shell32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    static extern IntPtr CommandLineToArgvW(string command, out int count);
    [DllImport("kernel32.dll")]
    static extern IntPtr LocalFree(IntPtr memory);
    [StructLayout(LayoutKind.Sequential)]
    struct FileInfo {
        public uint Attributes, CreationLow, CreationHigh, AccessLow, AccessHigh;
        public uint WriteLow, WriteHigh, Volume, SizeHigh, SizeLow, Links, IndexHigh, IndexLow;
    }
    [DllImport("kernel32.dll", SetLastError=true)]
    static extern bool GetFileInformationByHandle(SafeFileHandle handle, out FileInfo info);
    public static FileStream OpenRead(string path, FileShare share) {
        var stream = new FileStream(path, FileMode.Open, FileAccess.Read, share, 8192, true);
        FileInfo info;
        if (!GetFileInformationByHandle(stream.SafeFileHandle, out info) || info.Links != 1 || (info.Attributes & 0x410) != 0) {
            stream.Dispose();
            throw new InvalidOperationException("Single-linked plain file required");
        }
        return stream;
    }
    public static async Task Extract(Stream input, Stream output, long length, CancellationToken token) {
        var buffer = new byte[81920];
        long total = 0;
        int count;
        while ((count = await input.ReadAsync(buffer, 0, buffer.Length, token).ConfigureAwait(false)) != 0) {
            total += count;
            if (total > length) throw new InvalidOperationException("Expanded archive limit exceeded");
            await output.WriteAsync(buffer, 0, count, token).ConfigureAwait(false);
        }
        if (total != length) throw new InvalidOperationException("Archive length differs");
    }
    public static bool Alive(Process process) {
        uint result = WaitForSingleObject(process.SafeHandle, 0);
        if (result != 0 && result != 258) throw new InvalidOperationException("Process wait failed");
        return result == 258;
    }
    public static long[] Times(Process process) {
        long created, exited, kernel, user;
        if (!GetProcessTimes(process.SafeHandle, out created, out exited, out kernel, out user))
            throw new InvalidOperationException("Process times unavailable");
        return new long[] { created, exited };
    }
    public static string Image(Process process) {
        int size = 32768;
        var name = new StringBuilder(size);
        if (!QueryFullProcessImageName(process.SafeHandle, 0, name, ref size))
            throw new InvalidOperationException("Process image unavailable");
        return name.ToString();
    }
    public static long Parent(Process process) {
        int size = IntPtr.Size * 6, needed;
        IntPtr buffer = Marshal.AllocHGlobal(size);
        try {
            if (NtQueryInformationProcess(process.SafeHandle, 0, buffer, size, out needed) != 0 || needed != size)
                throw new InvalidOperationException("Native parent query unsupported");
            return Marshal.ReadIntPtr(buffer, IntPtr.Size * 5).ToInt64();
        } finally { Marshal.FreeHGlobal(buffer); }
    }
    public static string[] Arguments(Process process) {
        // ProcessCommandLineInformation is an NT contract: unsupported hosts fail closed.
        const int size = 65536;
        int needed;
        IntPtr buffer = Marshal.AllocHGlobal(size);
        try {
            if (NtQueryInformationProcess(process.SafeHandle, 60, buffer, size, out needed) != 0)
                throw new InvalidOperationException("Native command-line query unsupported");
            int length = (ushort)Marshal.ReadInt16(buffer);
            IntPtr value = Marshal.ReadIntPtr(buffer, IntPtr.Size);
            long offset = value.ToInt64() - buffer.ToInt64();
            if (length == 0 || length % 2 != 0 || offset < 16 || offset + length > size)
                throw new InvalidOperationException("Invalid native command line");
            int count;
            IntPtr argv = CommandLineToArgvW(Marshal.PtrToStringUni(value, length / 2), out count);
            if (argv == IntPtr.Zero) throw new InvalidOperationException("Argument decoding failed");
            try {
                if (count < 1 || count > 16) throw new InvalidOperationException("Unexpected argument count");
                var result = new string[count];
                for (int i = 0; i < count; i++) result[i] = Marshal.PtrToStringUni(Marshal.ReadIntPtr(argv, i * IntPtr.Size));
                return result;
            } finally { LocalFree(argv); }
        } finally { Marshal.FreeHGlobal(buffer); }
    }
    public static void Stop(Process process, int milliseconds) {
        if (Alive(process) && !TerminateProcess(process.SafeHandle, 71) && WaitForSingleObject(process.SafeHandle, 0) != 0)
            throw new InvalidOperationException("Owned termination failed");
        if (WaitForSingleObject(process.SafeHandle, (uint)milliseconds) != 0)
            throw new TimeoutException("Owned termination did not join");
    }
    public static async Task<byte[]> Read(Stream stream, int limit, CancellationToken token) {
        using (var data = new MemoryStream()) {
            var buffer = new byte[8192];
            int count;
            while ((count = await stream.ReadAsync(buffer, 0, Math.Min(buffer.Length, limit + 1 - (int)data.Length), token).ConfigureAwait(false)) != 0) {
                if (data.Length + count > limit) throw new InvalidOperationException("Stream size limit exceeded");
                data.Write(buffer, 0, count);
            }
            return data.ToArray();
        }
    }
}
'@

$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
$trusted = @($sid.Value, 'S-1-5-18', 'S-1-5-32-544')
$trustedOwners = $trusted + 'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464'
$clock = [Diagnostics.Stopwatch]::StartNew()
$cancel = [Threading.CancellationTokenSource]::new(180000)
$operationCancel = $cancel
$deadlineMs = 180000L
$actors = [Collections.Generic.List[object]]::new()
$daemons = [Collections.Generic.List[object]]::new()
$files = [Collections.Generic.List[object]]::new()
$events = [Collections.Generic.List[object]]::new()
$cleanup = [Collections.Generic.List[string]]::new()
$primary = $null
$stage = 'paths'
$readyEvidence = $false
$daemon = $null
$client = $null
$lease = $null
$hashes = @{}
$token = $null

function Remaining([int]$Maximum = 10000) {
    $left = $deadlineMs - $clock.ElapsedMilliseconds
    if ($left -le 0) { throw 'Current phase deadline exceeded' }
    return [int][Math]::Min($left, $Maximum)
}
function Await($Task, [int]$Maximum = 10000) {
    if (-not $Task.Wait((Remaining $Maximum))) { throw 'Operation deadline exceeded' }
    return ,$Task.GetAwaiter().GetResult()
}
function Assert-Path([string]$Path, [bool]$New = $false) {
    if ($Path -cnotmatch '^[A-Za-z]:\\' -or $Path.Length -gt 200 -or $Path.Substring(3) -match '[:*?"<>|/\x00-\x1f]' -or
        $Path -match '\\($|\.|\.\.)(\\|$)|[ .](\\|$)' -or $Path -match '(?i)\\(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|\\|$)' -or [IO.Path]::GetFullPath($Path) -cne $Path) {
        throw 'Canonical local drive path required; no device, UNC, ADS or relative paths'
    }
    if ($New -and (Test-Path -LiteralPath $Path)) { throw 'Evidence target must not exist' }
    if (([IO.DriveInfo]::new([IO.Path]::GetPathRoot($Path))).DriveType -ne 'Fixed') { throw 'Fixed local drive required' }
    $cursor = $Path
    if ($New) { $cursor = Split-Path -Parent $cursor }
    while ($cursor) {
        $item = Get-Item -LiteralPath $cursor -Force
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse path rejected' }
        $acl = Get-Acl -LiteralPath $cursor
        $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
        if ($owner -notin $trustedOwners -or $acl.GetSecurityDescriptorSddlForm('Access') -match 'NO_ACCESS_CONTROL') { throw 'Untrusted path owner or null DACL' }
        foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
            # Modification rights, including replacement/deletion and DACL/owner changes.
            $mask = 0xD0150
            if (-not $item.PSIsContainer) { $mask = 0xD0156 }
            if ($rule.AccessControlType -eq 'Allow' -and -not ($rule.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly) -and
                ($rule.FileSystemRights -band $mask) -and $rule.IdentityReference.Value -notin ($trusted + $owner)) {
                throw 'Path writable by an untrusted principal'
            }
        }
        $parent = Split-Path -Parent $cursor
        if ($parent -eq $cursor) { break }
        $cursor = $parent
    }
}
function New-PrivateDirectory([string]$Path) {
    if (Test-Path -LiteralPath $Path) { throw 'Fresh owned directory required' }
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true, $false)
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', 'ContainerInherit, ObjectInherit', 'None', 'Allow'))
    [IO.FileSystemAclExtensions]::Create([IO.DirectoryInfo]::new($Path), $acl)
    $actual = Get-Acl -LiteralPath $Path
    $rules = @($actual.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    if ($actual.GetOwner([Security.Principal.SecurityIdentifier]).Value -cne $sid.Value -or -not $actual.AreAccessRulesProtected -or
        $rules.Count -ne 1 -or $rules[0].IdentityReference.Value -cne $sid.Value -or $rules[0].AccessControlType -ne 'Allow' -or
        $rules[0].FileSystemRights -ne [Security.AccessControl.FileSystemRights]::FullControl) { throw 'Private directory ACL differs' }
}
function Hash-Bytes([byte[]]$Bytes) {
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant()
}
function Hash-File([string]$Path) {
    $stream = [BundleNative]::OpenRead($Path, 'Read')
    try { return [Convert]::ToHexString([byte[]](Await ([Security.Cryptography.SHA256]::HashDataAsync($stream, $cancel.Token).AsTask()) 30000)).ToLowerInvariant() }
    finally { $stream.Dispose() }
}
function Assert-WindowsImage([string]$Path) {
    $reader = [IO.BinaryReader]::new([BundleNative]::OpenRead($Path, 'Read'))
    try {
        if ($reader.ReadUInt16() -ne 0x5A4D) { throw 'Missing DOS executable header' }
        $reader.BaseStream.Position = 0x3C
        $offset = $reader.ReadInt32()
        if ($offset -lt 64 -or $offset -gt 1048576 -or $offset + 6 -gt $reader.BaseStream.Length) { throw 'Invalid PE header offset' }
        $reader.BaseStream.Position = $offset
        if ($reader.ReadUInt32() -ne 0x4550 -or $reader.ReadUInt16() -ne 0x8664) { throw 'Native Windows amd64 PE required' }
    } finally { $reader.Dispose() }
}
function Read-File([string]$Path, [int]$Limit) {
    Assert-Path $Path
    $stream = [BundleNative]::OpenRead($Path, 'ReadWrite')
    try { return [Text.Encoding]::UTF8.GetString((Await ([BundleNative]::Read($stream, $Limit, $cancel.Token)))) }
    finally { $stream.Dispose() }
}
function Start-ConsoleProcess([string]$Executable, [string[]]$Arguments) {
    $start = [Diagnostics.ProcessStartInfo]::new($Executable)
    $start.UseShellExecute = $false
    $start.WorkingDirectory = $cwd
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $start.Environment.Clear()
    foreach ($name in @('HOME','USERPROFILE','APPDATA','LOCALAPPDATA','XDG_CONFIG_HOME','XDG_CACHE_HOME','TEMP','TMP')) { $start.Environment[$name] = $isolatedProfile }
    $start.Environment['SystemRoot'] = [Environment]::GetFolderPath('Windows')
    $start.Environment['WINDIR'] = $start.Environment['SystemRoot']
    $start.Environment['PATH'] = $emptyPath
    $start.Environment['PATHEXT'] = '.EXE'
    $start.Environment['TERM'] = 'dumb'
    $start.Environment['LANG'] = 'en_US.UTF-8'
    $actor = @{ Process = [Diagnostics.Process]::new(); Started = $false; Out = $null; Err = $null }
    $actors.Add($actor)
    $actor.Process.StartInfo = $start
    $actor.Started = $actor.Process.Start()
    if (-not $actor.Started) { throw 'Packaged process failed to start' }
    $null = $actor.Process.SafeHandle
    $actor.Out = [BundleNative]::Read($actor.Process.StandardOutput.BaseStream, 262144, $cancel.Token)
    $actor.Err = [BundleNative]::Read($actor.Process.StandardError.BaseStream, 65536, $cancel.Token)
    $actor.Process.StandardInput.Close()
    $events.Add(@{ event = 'launch'; pid = $actor.Process.Id; created = [BundleNative]::Times($actor.Process)[0]; requestedImage = $Executable; argv = $Arguments })
    return $actor
}
function Finish-ConsoleProcess($Actor, [bool]$Snapshot = $false) {
    if (-not $Actor.Process.WaitForExit((Remaining 15000))) { throw 'Packaged process deadline exceeded' }
    $stdout = [byte[]](Await $Actor.Out)
    $stderr = [byte[]](Await $Actor.Err)
    $events.Add(@{ event = 'exit'; pid = $Actor.Process.Id; exit = $Actor.Process.ExitCode; stdoutBytes = $stdout.Length; stderrBytes = $stderr.Length })
    if ($Actor.Process.ExitCode -ne 0 -or $stderr.Length -ne 0) { throw 'Packaged process failed or wrote stderr' }
    $text = [Text.Encoding]::UTF8.GetString($stdout)
    if ($Snapshot -and ($text -notmatch 'BATUTA\s+workspace\s+\[Home\]' -or $text -notmatch 'Missions.*Squads.*Accounts.*Activity' -or $text -notmatch 'no workspaces yet' -or $text.Length -lt 200)) {
        throw 'Expected rendered Console home snapshot missing'
    }
    if ($Snapshot) {
        $secret = Read-File (Join-Path $runtimeHome 'auth.secret') 128
        if ($secret -cnotmatch '^[a-f0-9]{64}$' -or $text.Contains($secret)) { throw 'Snapshot secret check failed' }
    }
    $events.Add(@{ event = 'validated-output'; pid = $Actor.Process.Id; snapshot = $Snapshot; stdoutSHA256 = (Hash-Bytes $stdout) })
    return $text
}
function Assert-Provenance($Info, [string]$Binary) {
    if ($Info.binary -cne $Binary -or $Info.release.version -cne $ExpectedVersion -or $Info.release.revision -cne $ExpectedRevision -or
        $Info.release.authority -cne 'linker_injected' -or $Info.observed.revision -cne $ExpectedRevision -or
        $Info.observed.modified -isnot [bool] -or $Info.observed.modified -or $Info.observed.authority -cne 'go_build_info' -or
        $Info.go_version -cne 'go1.27.2' -or $Info.go_authority -cne 'runtime.Version' -or
        $Info.core.authority -cne 'go_build_info' -or $Info.core.replacement_version -cne 'none') { throw 'Unexpected binary provenance' }
    if ($Binary -ceq 'consoled' -or $Info.core.status -cne 'missing') {
        if ($Info.core.status -cne 'linked' -or $Info.core.version -cne 'v1.1.0-beta.55') { throw 'Unexpected linked Core provenance' }
    } elseif ($Info.core.version -cne 'unknown') { throw 'Unexpected absent Core provenance' }
}
function Bind-Daemon($Parent) {
    $lock = Read-File (Join-Path $runtimeHome 'consoled.lock') 4096 | ConvertFrom-Json
    if ($lock.home -cne $runtimeHome -or $lock.addr -cne $listen -or $lock.pid -le 0) { throw 'Unexpected daemon lock' }
    $process = [Diagnostics.Process]::GetProcessById($lock.pid)
    $bound = $false
    try {
        $null = $process.SafeHandle
        $created = [BundleNative]::Times($process)[0]
        $parentTimes = [BundleNative]::Times($Parent.Process)
        $image = [BundleNative]::Image($process)
        $argv = [BundleNative]::Arguments($process)
        $expected = @($daemonExe, '-home', $runtimeHome, '-listen', $listen)
        if (-not [BundleNative]::Alive($process) -or [BundleNative]::Parent($process) -ne $Parent.Process.Id -or
            $created -lt $parentTimes[0] -or ($parentTimes[1] -ne 0 -and $created -gt $parentTimes[1]) -or
            -not [StringComparer]::OrdinalIgnoreCase.Equals($image, $daemonExe) -or
            ($argv | ConvertTo-Json -Compress) -cne ($expected | ConvertTo-Json -Compress) -or
            (Hash-File $image) -cne $hashes['consoled.exe'] -or -not [BundleNative]::Alive($process)) { throw 'Daemon ownership binding failed' }
        $daemons.Add($process)
        $bound = $true
        $events.Add(@{ event = 'bound'; pid = $process.Id; created = $created; parentPID = $Parent.Process.Id; parentCreated = $parentTimes[0]; parentExited = $parentTimes[1]; image = $image; imageSHA256 = $hashes['consoled.exe']; argv = $argv })
        return $process
    } finally { if (-not $bound) { $process.Dispose() } }
}
function Assert-SameDaemon($Process) {
    if ((Get-Item -LiteralPath (Join-Path $runtimeHome 'consoled.log')).Length -gt 65536) { throw 'Private daemon log exceeded observation limit' }
    if (-not [BundleNative]::Alive($Process)) { throw 'Original daemon object exited' }
    $lock = Read-File (Join-Path $runtimeHome 'consoled.lock') 4096 | ConvertFrom-Json
    if ($lock.pid -ne $Process.Id -or $lock.home -cne $runtimeHome -or $lock.addr -cne $listen) { throw 'Live daemon lock changed' }
    if (-not [BundleNative]::Alive($Process)) { throw 'Original daemon object no longer live' }
}
function Request([string]$Path, [string]$Token = '') {
    if ($Path -cnotmatch '^/(v0/(capabilities|workspaces)|assets/[A-Za-z0-9_-]+\.(js|css))?$') { throw 'Nonlocal request path rejected' }
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Get, "http://$listen$Path")
    $response = $null
    $budget = [Threading.CancellationTokenSource]::CreateLinkedTokenSource($cancel.Token)
    $budget.CancelAfter((Remaining 5000))
    try {
        if ($Token) { $request.Headers.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $Token) }
        $response = Await ($client.SendAsync($request, [Net.Http.HttpCompletionOption]::ResponseHeadersRead, $budget.Token)) 5000
        $stream = Await ($response.Content.ReadAsStreamAsync($budget.Token)) 5000
        $bytes = [byte[]](Await ([BundleNative]::Read($stream, 4194304, $budget.Token)) 5000)
        $text = [Text.Encoding]::UTF8.GetString($bytes)
        if ($script:token -and $text.Contains($script:token)) { throw 'Secret appeared in response; evidence rejected' }
        $status = [int]$response.StatusCode
        $type = [string]$response.Content.Headers.ContentType
        $events.Add(@{ event = 'http'; path = $Path; authenticated = [bool]$Token; status = $status; bytes = $bytes.Length; sha256 = (Hash-Bytes $bytes); contentType = $type })
        return @{ Status = $status; Text = $text; Type = $type }
    } finally { if ($null -ne $response) { $response.Dispose() }; $request.Dispose(); $budget.Dispose() }
}
function Drain-DaemonLog {
    $log = [BundleNative]::OpenRead((Join-Path $runtimeHome 'consoled.log'), 'None')
    try {
        if ($log.Length -gt 65536) { throw 'Private daemon log exceeded observation limit' }
        $events.Add(@{ event = 'daemon-log-closed'; bytes = $log.Length; exclusiveOpen = $true })
    } finally { $log.Dispose() }
}
function Probe-Daemon {
    Assert-SameDaemon $daemon
    foreach ($path in @('/v0/capabilities','/v0/workspaces')) {
        foreach ($credential in @('', 'deliberately-invalid')) {
            if ((Request $path $credential).Status -ne 401) { throw 'Missing/wrong token was not unauthorized' }
        }
    }
    $cap = Request '/v0/capabilities' $token
    if ($cap.Status -ne 200 -or $cap.Type -notmatch '^application/json') { throw 'Authenticated readiness failed' }
    $info = $cap.Text | ConvertFrom-Json
    if ($info.product -cne 'batuta-console' -or $info.api -cne 'v0' -or -not $info.features.skills_catalog -or
        $info.runtime.os -cne 'windows' -or $info.runtime.arch -cne 'amd64' -or $info.runtime.go_version -cne 'go1.27.2') { throw 'Unexpected capabilities' }
    foreach ($provider in $info.providers) {
        if ($provider.installation.status -cne 'missing') { throw 'Provider unexpectedly available on isolated PATH' }
    }
    $info.provenance | Add-Member -NotePropertyName binary -NotePropertyValue consoled
    Assert-Provenance $info.provenance 'consoled'
    $catalog = Request '/v0/workspaces' $token
    if ($catalog.Status -ne 200 -or $catalog.Type -notmatch '^application/json' -or $catalog.Text.Trim() -cne '[]') { throw 'Fresh empty catalog differs' }
    $html = Request '/'
    if ($html.Status -ne 200 -or $html.Type -notmatch '^text/html' -or $html.Text -notmatch '<title>batuta / console</title>' -or
        $html.Text -notmatch '<div id="root"></div>' -or $html.Text -match '(?i)placeholder|/src/main\.tsx') { throw 'Embedded production HTML missing' }
    $assets = @([regex]::Matches($html.Text, '(?:src|href)="(/assets/[A-Za-z0-9_-]+\.(?:js|css))"') | ForEach-Object { $_.Groups[1].Value } | Sort-Object -Unique)
    if ($assets.Count -lt 2 -or $assets.Count -gt 8 -or -not ($assets -match '\.js$') -or -not ($assets -match '\.css$')) { throw 'Production asset references missing or excessive' }
    foreach ($path in $assets) {
        $asset = Request $path
        if ($asset.Status -ne 200 -or $asset.Text.Length -lt 100 -or $asset.Text -match '^\s*<!doctype|^\s*<html') { throw 'Embedded asset missing' }
        if ($path.EndsWith('.js') -and ($asset.Type -notmatch 'javascript' -or $asset.Text -notmatch 'createElement|react')) { throw 'Production JavaScript missing' }
        if ($path.EndsWith('.css') -and ($asset.Type -notmatch '^text/css' -or $asset.Text -notmatch '\{[^}]+\}')) { throw 'Production CSS missing' }
    }
    Assert-SameDaemon $daemon
}

try {
    Assert-Path $Archive
    Assert-Path $Checksums
    Assert-Path $Evidence $true
    if ([IO.Path]::GetExtension($Archive) -cne '.zip' -or [IO.Path]::GetFileName($Archive) -cnotmatch '^[A-Za-z0-9_.+-]+\.zip$') { throw 'ZIP input required' }
    New-PrivateDirectory $Evidence
    $readyEvidence = $true
    $package = Join-Path $Evidence 'package with spaces'
    $cwd = Join-Path $Evidence 'unrelated cwd'
    $runtimeHome = Join-Path $Evidence 'runtime home'
    $isolatedProfile = Join-Path $Evidence 'empty profile'
    $emptyPath = Join-Path $Evidence 'empty path'
    foreach ($directory in @($package,$cwd,$runtimeHome,$isolatedProfile,$emptyPath)) { New-PrivateDirectory $directory }
    $events.Add(@{ event = 'isolation'; sid = $sid.Value; privateDACL = (Get-Acl -LiteralPath $Evidence).Sddl; cwd = $cwd; runtimeHome = $runtimeHome; profile = $isolatedProfile; path = $emptyPath; inheritedEnvironment = $false })
    $stage = 'checksum'
    $archiveStream = [BundleNative]::OpenRead($Archive, 'Read')
    $files.Add($archiveStream)
    if ($archiveStream.Length -gt 536870912) { throw 'Archive size limit exceeded' }
    $checksumStream = [BundleNative]::OpenRead($Checksums, 'Read')
    $files.Add($checksumStream)
    $checksumBytes = [byte[]](Await ([BundleNative]::Read($checksumStream, 65536, $cancel.Token)))
    $records = [Text.Encoding]::UTF8.GetString($checksumBytes).TrimEnd("`r", "`n") -split '\r?\n'
    $names = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $expectedHash = $null
    foreach ($record in $records) {
        if ($record -cnotmatch '^([a-f0-9]{64})\s+([A-Za-z0-9_.+-]+)$' -or -not $names.Add($Matches[2])) { throw 'Malformed or duplicate checksum record' }
        if ($Matches[2] -ceq [IO.Path]::GetFileName($Archive)) { $expectedHash = $Matches[1] }
    }
    $archiveHash = [Convert]::ToHexString((Await ([Security.Cryptography.SHA256]::HashDataAsync($archiveStream, $cancel.Token).AsTask()) 30000)).ToLowerInvariant()
    if (-not $expectedHash -or $archiveHash -cne $expectedHash) { throw 'Archive checksum mismatch' }
    $hashes['archive'] = $archiveHash
    $hashes['checksums'] = Hash-Bytes $checksumBytes
    $hashes['qualifier'] = Hash-File $PSCommandPath
    $stage = 'archive'
    $archiveStream.Position = 0
    $zip = [IO.Compression.ZipArchive]::new($archiveStream, 'Read', $true)
    try {
        $expectedMembers = @('console.exe','consoled.exe','LICENSE','README.md','THIRD_PARTY_NOTICES')
        if ($zip.Entries.Count -ne 5) { throw 'Archive must contain exactly five members' }
        $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
        $expanded = 0L
        foreach ($entry in $zip.Entries) {
            $expanded += $entry.Length
            if ($expanded -gt 536870912) { throw 'Total expanded archive size limit exceeded' }
            $kind = ($entry.ExternalAttributes -shr 16) -band 0xF000
            if ($entry.FullName -cnotin $expectedMembers -or -not $seen.Add($entry.FullName) -or $kind -notin @(0,0x8000) -or
                ($entry.ExternalAttributes -band 0x410) -ne 0 -or $entry.Length -le 0 -or $entry.Length -gt 268435456) { throw 'Wrong, duplicate or unsafe archive member' }
        }
        foreach ($entry in $zip.Entries) {
            $memberStream = $entry.Open()
            $output = [IO.File]::Open((Join-Path $package $entry.FullName), 'CreateNew', 'Write', 'None')
            try { $null = Await ([BundleNative]::Extract($memberStream, $output, $entry.Length, $cancel.Token)) 30000 }
            finally { $memberStream.Dispose(); $output.Dispose() }
            if ((Get-Item -LiteralPath (Join-Path $package $entry.FullName)).Length -ne $entry.Length) { throw 'Extracted length differs' }
        }
    } finally { $zip.Dispose() }
    foreach ($name in $expectedMembers) {
        $path = Join-Path $package $name
        $files.Add([BundleNative]::OpenRead($path, 'Read'))
        $hashes[$name] = Hash-File $path
    }
    $consoleExe = Join-Path $package 'console.exe'
    $daemonExe = Join-Path $package 'consoled.exe'
    $stage = 'provenance'
    foreach ($name in @('console','consoled')) {
        Assert-WindowsImage (Join-Path $package "$name.exe")
        $actor = Start-ConsoleProcess (Join-Path $package "$name.exe") @('-version')
        $info = (Finish-ConsoleProcess $actor) | ConvertFrom-Json
        Assert-Provenance $info $name
        $events.Add(@{ event = 'provenance'; binary = $name; diagnostic = $info })
    }
    $stage = 'launch'
    $lease = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $lease.ExclusiveAddressUse = $true
    $lease.Start()
    $listen = "127.0.0.1:$($lease.LocalEndpoint.Port)"
    $lease.Stop()
    $handler = [Net.Http.HttpClientHandler]::new()
    $handler.UseProxy = $false
    $handler.AllowAutoRedirect = $false
    $handler.UseCookies = $false
    $handler.MaxResponseHeadersLength = 16
    $client = [Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds(5)
    $actor = Start-ConsoleProcess $consoleExe @('-home', $runtimeHome, '-listen', $listen)
    $snapshot = Finish-ConsoleProcess $actor $true
    $daemon = Bind-Daemon $actor
    $token = Read-File (Join-Path $runtimeHome 'auth.secret') 128
    if ($token -cnotmatch '^[a-f0-9]{64}$' -or $snapshot.Contains($token)) { throw 'Invalid or exposed runtime token' }
    $stage = 'readiness'
    Probe-Daemon
    $stage = 'reuse'
    $again = Start-ConsoleProcess $consoleExe @('-home', $runtimeHome, '-listen', $listen)
    $null = Finish-ConsoleProcess $again $true
    Assert-SameDaemon $daemon
    $events.Add(@{ event = 'reuse'; pid = $daemon.Id; created = [BundleNative]::Times($daemon)[0]; originalHandleAlive = $true })
    $stage = 'crash-restart'
    [BundleNative]::Stop($daemon, (Remaining 5000))
    Drain-DaemonLog
    $events.Add(@{ event = 'direct-crash-termination'; pid = $daemon.Id; created = [BundleNative]::Times($daemon)[0]; exited = [BundleNative]::Times($daemon)[1]; exit = $daemon.ExitCode; joined = $true; graceful = $false })
    $restart = Start-ConsoleProcess $consoleExe @('-home', $runtimeHome, '-listen', $listen)
    $null = Finish-ConsoleProcess $restart $true
    $previous = $daemon
    $daemon = Bind-Daemon $restart
    if ([BundleNative]::Times($daemon)[0] -le [BundleNative]::Times($previous)[0] -or [BundleNative]::Alive($previous)) { throw 'New daemon object required' }
    if ((Read-File (Join-Path $runtimeHome 'auth.secret') 128) -cne $token) { throw 'Restart changed auth secret' }
    Probe-Daemon
    foreach ($name in $expectedMembers) {
        if ((Hash-File (Join-Path $package $name)) -cne $hashes[$name]) { throw 'Extracted artifact changed during operation' }
    }
    $stage = 'complete'
} catch {
    # Exception payloads can contain captured process/HTTP data; export only location/type.
    $primary = @{ stage = $stage; type = $_.Exception.GetType().FullName; line = $_.InvocationInfo.ScriptLineNumber }
} finally {
    $operationElapsedMs = $clock.ElapsedMilliseconds
    $cancel = [Threading.CancellationTokenSource]::new(20000)
    $deadlineMs = $clock.ElapsedMilliseconds + 20000
    # First retire Console launchers, then bind any still-unbound child using the
    # retained original creator handles. A lock PID alone never grants kill authority.
    foreach ($actor in $actors) {
        if (-not $actor.Started) { continue }
        try {
            [BundleNative]::Stop($actor.Process, [int][Math]::Max(0, [Math]::Min(5000, $deadlineMs - $clock.ElapsedMilliseconds)))
            $events.Add(@{ event = 'console-cleanup'; pid = $actor.Process.Id; exit = $actor.Process.ExitCode; originalObjectJoined = $true })
        }
        catch { $cleanup.Add("console join: $($_.Exception.GetType().Name)") }
    }
    try {
        if ($readyEvidence -and (Test-Path -LiteralPath (Join-Path $runtimeHome 'consoled.lock'))) {
            $boundLive = @($daemons | Where-Object { [BundleNative]::Alive($_) })
            if ($boundLive.Count -eq 0 -and $stage -ne 'complete') {
                $recovered = $false
                $recoveryErrors = [Collections.Generic.List[string]]::new()
                for ($i = $actors.Count - 1; $i -ge 0; $i--) {
                    $actor = $actors[$i]
                    if (-not $actor.Started -or $actor.Process.StartInfo.ArgumentList.Count -ne 4) { continue }
                    try { $daemon = Bind-Daemon $actor; $recovered = $true; break }
                    catch { $recoveryErrors.Add("creator ${i}: $($_.Exception.GetType().Name)") }
                }
                if (-not $recovered) {
                    $cleanup.Add("no child bound for cleanup; lock may be stale; inspection required; eligible bindings failed ($($recoveryErrors.Count)): $($recoveryErrors -join '; ')")
                }
            }
        }
    } catch { $cleanup.Add("child recovery: $($_.Exception.GetType().Name)") }
    foreach ($process in $daemons) {
        try {
            [BundleNative]::Stop($process, [int][Math]::Max(0, [Math]::Min(5000, $deadlineMs - $clock.ElapsedMilliseconds)))
            $events.Add(@{ event = 'cleanup'; pid = $process.Id; created = [BundleNative]::Times($process)[0]; exited = [BundleNative]::Times($process)[1]; exit = $process.ExitCode; originalObjectJoined = $true })
        } catch { $cleanup.Add("daemon join: $($_.Exception.GetType().Name)") }
    }
    if ($daemons.Count -gt 0) {
        try { Drain-DaemonLog } catch { $cleanup.Add("daemon log drain: $($_.Exception.GetType().Name)") }
    }
    foreach ($actor in $actors) {
        if ($actor.Started) {
            foreach ($task in @($actor.Out, $actor.Err)) {
                if ($null -eq $task) { continue }
                try { if (-not $task.Wait((Remaining 1000))) { throw 'Stream drain deadline' }; $null = $task.GetAwaiter().GetResult() }
                catch { $cleanup.Add("stream drain: $($_.Exception.GetType().Name)") }
            }
        }
        try { $actor.Process.Dispose() } catch { $cleanup.Add("console handle close: $($_.Exception.GetType().Name)") }
    }
    foreach ($resource in @($daemons.ToArray()) + @($files.ToArray()) + @($client, $operationCancel, $cancel)) {
        if ($null -ne $resource) {
            try { $resource.Dispose() } catch { $cleanup.Add("resource close: $($_.Exception.GetType().Name)") }
        }
    }
    if ($null -ne $lease) {
        try { $lease.Stop() } catch { $cleanup.Add("port lease close: $($_.Exception.GetType().Name)") }
    }
    $token = $null
    if ($readyEvidence) {
        try {
            $events.ToArray() | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $Evidence 'observations.json')
            $receipt = @{ native = $(if ($null -eq $primary -and $cleanup.Count -eq 0) { 'passed' } else { 'failed' }); stage = $stage;
                scope = 'artifact-only nonTTY, authenticated web, direct crash-restart; not graceful/interactive/provider/Task43';
                version = $ExpectedVersion; revision = $ExpectedRevision; hashes = $hashes; primary = $primary; cleanup = $cleanup.ToArray();
                elapsedMs = $clock.ElapsedMilliseconds; operationElapsedMs = $operationElapsedMs; cleanupElapsedMs = $clock.ElapsedMilliseconds - $operationElapsedMs;
                operationBudgetMs = 180000; cleanupBudgetMs = 20000; observations = 'observations.json'; privateRuntimeRetained = $true } | ConvertTo-Json -Depth 5
            if ([Text.Encoding]::UTF8.GetByteCount($receipt) -gt 4096) { throw 'Receipt exceeds 4 KiB' }
            $receipt | Set-Content -LiteralPath (Join-Path $Evidence 'receipt.json')
        } catch { $cleanup.Add("evidence write: $($_.Exception.GetType().Name)") }
    }
}
foreach ($failure in $cleanup) { [Console]::Error.WriteLine("Qualification cleanup: $failure") }
if ($null -ne $primary) { throw "Artifact qualification failed at $($primary.stage), line $($primary.line); native qualification incomplete" }
if ($cleanup.Count) { throw 'Artifact qualification cleanup failed; native qualification incomplete' }
Write-Output 'PASS: packaged Console nonTTY discovery, authenticated embedded web and owned crash-restart; no interactive/provider/Task43 claim'
