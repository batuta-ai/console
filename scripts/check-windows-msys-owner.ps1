param(
    [Parameter(Mandatory)][string]$Evidence,
    [Parameter(Mandatory)][string]$FixtureBinary,
    [Parameter(Mandatory)][string]$Source,
    [Parameter(Mandatory)][string]$Go,
    [Parameter(Mandatory)][string]$Git,
    [object[]]$ExpectedSources,
    [switch]$FullSuite,
    [scriptblock]$Operation
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$PSNativeCommandUseErrorActionPreference = $false
if (-not $IsWindows) { throw 'Native Windows required' }
if ($FullSuite -and $Operation) { throw 'Exactly one caller operation required' }
if (-not $FullSuite -and -not $Operation) { throw 'Ordinary caller operation required' }

# Safe handles belong to the original creation objects; never reopen a PID.
Add-Type -TypeDefinition @'
using System;
using System.Diagnostics;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
public static class MsysOwnerNative {
    [StructLayout(LayoutKind.Sequential)]
    struct FileInfo {
        public uint Attributes, CreationLow, CreationHigh, AccessLow, AccessHigh;
        public uint WriteLow, WriteHigh, Volume, SizeHigh, SizeLow, Links, IndexHigh, IndexLow;
    }
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    static extern SafeFileHandle CreateFile(string path, uint access, uint share, IntPtr security, uint creation, uint flags, IntPtr template);
    [DllImport("kernel32.dll", SetLastError=true)]
    static extern bool GetFileInformationByHandle(SafeFileHandle file, out FileInfo info);
    [DllImport("kernel32.dll", SetLastError=true)]
    static extern uint WaitForSingleObject(SafeProcessHandle process, uint timeout);
    [DllImport("advapi32.dll", SetLastError=true)]
    static extern bool OpenProcessToken(SafeProcessHandle process, uint access, out SafeAccessTokenHandle token);
    [DllImport("advapi32.dll", SetLastError=true)]
    static extern bool GetTokenInformation(SafeAccessTokenHandle token, int kind, out int value, int size, out int needed);
    public static string DirectoryIdentity(string path) {
        using (var file = CreateFile(path, 0x80, 7, IntPtr.Zero, 3, 0x02200000, IntPtr.Zero)) {
            if (file.IsInvalid) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            FileInfo info;
            if (!GetFileInformationByHandle(file, out info)) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            if ((info.Attributes & 0x10) == 0 || (info.Attributes & 0x400) != 0) throw new InvalidOperationException("Plain native directory required");
            return String.Format("{0:x8}:{1:x8}{2:x8}", info.Volume, info.IndexHigh, info.IndexLow);
        }
    }
    public static bool Alive(Process process) {
        uint result = WaitForSingleObject(process.SafeHandle, 0);
        if (result != 0 && result != 258) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        return result == 258;
    }
    public static int TokenInt(Process process, int kind) {
        SafeAccessTokenHandle token;
        if (!OpenProcessToken(process.SafeHandle, 8, out token)) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        using (token) {
            int value, needed;
            if (!GetTokenInformation(token, kind, out value, 4, out needed) || needed != 4)
                throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            return value;
        }
    }
}
'@

$owned = [Collections.Generic.List[object]]::new()
$roots = [Collections.Generic.List[string]]::new()
$observations = [Collections.Generic.List[object]]::new()
$cleanupErrors = [Collections.Generic.List[string]]::new()
$primary = $null
$suiteExit = $null
$keeper = $null
$beforeHashes = $null
$consoleEncoding = [Console]::OutputEncoding
$stable = $env:TEMP
$shell = Join-Path (Split-Path (Split-Path $Git)) 'usr/bin/sh.exe'
$utilities = Split-Path $shell
$nonce = [guid]::NewGuid().ToString('N')

function Save-Json([string]$Name, $Value) {
    $Value | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $Evidence $Name)
}
function Await-Task($Task, [int]$Milliseconds = 10000) {
    if (-not $Task.Wait($Milliseconds)) { throw 'Owned stream deadline exceeded' }
    # Task<VoidTaskResult> must never escape onto the caller's evidence stream.
    $null = $Task.GetAwaiter().GetResult()
}
function Assert-Alive($Actor) {
    if (-not [MsysOwnerNative]::Alive($Actor.Process)) { throw "Original process exited prematurely: $($Actor.Name)" }
}
function Start-Owned([string]$Name, [string]$Executable, [string[]]$Arguments, [string]$Temp, [bool]$Interactive = $false) {
    $start = [Diagnostics.ProcessStartInfo]::new($Executable)
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $start.UseShellExecute = $false
    $start.WorkingDirectory = $Source
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.Environment['TEMP'] = $Temp
    $start.Environment['TMP'] = $Temp
    if ($Interactive) {
        $start.Environment['MSYS_OWNER_NONCE'] = $nonce
        $start.Environment['MSYS_OWNER_CYGPATH'] = (Join-Path $utilities 'cygpath.exe').Replace('\', '/')
    }
    $actor = @{ Name = $Name; Temp = $Temp; TempIdentity = [MsysOwnerNative]::DirectoryIdentity($Temp); Process = [Diagnostics.Process]::new(); Started = $false; Joined = $false;
        Drained = $false; Closed = $false; Interactive = $Interactive; Input = $null; Out = $null; Err = $null;
        OutTask = $null; ErrTask = $null; ReadTask = $null; Exit = $null; Forced = $false }
    $owned.Add($actor)
    $actor.Out = [IO.File]::Create((Join-Path $Evidence "$Name.stdout"))
    $actor.Err = [IO.File]::Create((Join-Path $Evidence "$Name.stderr"))
    $actor.Process.StartInfo = $start
    $actor.Started = $actor.Process.Start()
    if (-not $actor.Started) { throw "Owned process failed to start: $Name" }
    $null = $actor.Process.SafeHandle
    $actor.ErrTask = $actor.Process.StandardError.BaseStream.CopyToAsync($actor.Err)
    if (-not $Interactive) { $actor.OutTask = $actor.Process.StandardOutput.BaseStream.CopyToAsync($actor.Out) }
    $observations.Add(@{ event = 'creation'; name = $Name; pid = $actor.Process.Id; originalHandle = $actor.Process.SafeHandle.DangerousGetHandle().ToInt64();
        executable = $Executable; arguments = $Arguments; temp = $Temp })
    return $actor
}
function Read-ActorLine($Actor) {
    $Actor.ReadTask = $Actor.Process.StandardOutput.ReadLineAsync()
    Await-Task $Actor.ReadTask
    $line = $Actor.ReadTask.Result
    $Actor.ReadTask = $null
    if ($null -eq $line) { throw "Protocol EOF: $($Actor.Name)" }
    $bytes = [Text.Encoding]::UTF8.GetBytes($line + "`n")
    $Actor.Out.Write($bytes, 0, $bytes.Length)
    return $line
}
function Send-Actor($Actor, [string]$Request) {
    Assert-Alive $Actor
    Await-Task ($Actor.Process.StandardInput.WriteAsync($Request + "`n"))
    Await-Task ($Actor.Process.StandardInput.FlushAsync())
}
function Observe-Actor($Actor, [string]$Phase, [string]$ExpectedPath, [string]$ExpectedIdentity, [bool]$Retired = $false) {
    Send-Actor $Actor "$nonce $Phase"
    $ack = Read-ActorLine $Actor
    $path = Read-ActorLine $Actor
    $actualTemp = Read-ActorLine $Actor
    $actualTmp = Read-ActorLine $Actor
    $exists = Read-ActorLine $Actor
    Assert-Alive $Actor
    if ($ack -cne "$nonce $Phase" -or -not [IO.Path]::IsPathFullyQualified($path) -or $exists -cnotin @('present', 'retired')) {
        throw "Invalid real MSYS acknowledgement: $($Actor.Name)"
    }
    if (-not [StringComparer]::OrdinalIgnoreCase.Equals([IO.Path]::GetFullPath($path), [IO.Path]::GetFullPath($ExpectedPath))) {
        throw "Unexpected existing /tmp mapping; refusing takeover: $path"
    }
    foreach ($actual in @($actualTemp, $actualTmp)) {
        if (-not [IO.Path]::IsPathFullyQualified($actual) -or
            [MsysOwnerNative]::DirectoryIdentity($actual) -cne $Actor.TempIdentity) { throw 'Actual actor TEMP/TMP identity differs' }
    }
    $identity = $null
    if ($Retired) {
        if ($exists -cne 'retired' -or [IO.Directory]::Exists($path) -or [IO.File]::Exists($path)) { throw 'Retired backing still exists' }
        try { $null = [MsysOwnerNative]::DirectoryIdentity($path); throw 'Retired backing reopened' }
        catch [ComponentModel.Win32Exception] { if ($_.Exception.NativeErrorCode -notin @(2, 3)) { throw } }
    } else {
        $identity = [MsysOwnerNative]::DirectoryIdentity($path)
        if ($exists -cne 'present' -or $identity -cne $ExpectedIdentity) { throw 'Actual /tmp native identity differs' }
    }
    $observations.Add(@{ event = 'ack'; name = $Actor.Name; phase = $Phase; nonce = $nonce; path = $path;
        identity = $identity; expectedIdentity = $ExpectedIdentity; actualTemp = $actualTemp; actualTmp = $actualTmp; actorTempIdentity = $Actor.TempIdentity; retired = $Retired; originalHandleAlive = $true })
}
function Stop-Owned($Actor) {
    if ($Actor.Closed) { return }
    try {
        if ($Actor.Started) {
            try { $Actor.Process.StandardInput.Close() } catch { $cleanupErrors.Add("$($Actor.Name) stdin: $_") }
            if (-not $Actor.Process.WaitForExit(5000)) {
                $Actor.Forced = $true
                $cleanupErrors.Add("$($Actor.Name) exceeded graceful join deadline")
                $Actor.Process.Kill()
                if (-not $Actor.Process.WaitForExit(5000)) { throw 'Direct owned process did not join after termination' }
            }
            $Actor.Joined = $true
            $Actor.Exit = $Actor.Process.ExitCode
            $Actor.Exit | Set-Content -LiteralPath (Join-Path $Evidence "$($Actor.Name).exit")
            if ($Actor.Interactive) {
                if ($null -ne $Actor.ReadTask) {
                    Await-Task $Actor.ReadTask 5000
                    $remainingLine = $Actor.ReadTask.Result
                    if ($null -ne $remainingLine) {
                        $bytes = [Text.Encoding]::UTF8.GetBytes($remainingLine + "`n")
                        $Actor.Out.Write($bytes, 0, $bytes.Length)
                    }
                }
                $tail = $Actor.Process.StandardOutput.ReadToEndAsync()
                Await-Task $tail 5000
                $bytes = [Text.Encoding]::UTF8.GetBytes($tail.Result)
                $Actor.Out.Write($bytes, 0, $bytes.Length)
                if ($bytes.Length -ne 0) { $cleanupErrors.Add("$($Actor.Name) unexpected trailing protocol output") }
            } else { Await-Task $Actor.OutTask 5000 }
            Await-Task $Actor.ErrTask 5000
            $Actor.Drained = $true
        }
    } catch { $cleanupErrors.Add("$($Actor.Name) finalization: $_") } finally {
        foreach ($stream in @($Actor.Input, $Actor.Out, $Actor.Err)) {
            if ($null -ne $stream) { try { $stream.Dispose() } catch { $cleanupErrors.Add("$($Actor.Name) stream: $_") } }
        }
        try { $Actor.Process.Dispose() } catch { $cleanupErrors.Add("$($Actor.Name) handle: $_") }
        $Actor.Closed = $true
        $observations.Add(@{ event = 'join'; name = $Actor.Name; joinedOriginal = $Actor.Joined; drained = $Actor.Drained;
            exit = $Actor.Exit; forcedDirectKill = $Actor.Forced })
    }
}
function Assert-ActorClosed($Actor) {
    if (-not $Actor.Joined -or -not $Actor.Drained -or $Actor.Forced -or $Actor.Exit -ne 0 -or
        [IO.File]::ReadAllText((Join-Path $Evidence "$($Actor.Name).stderr"))) { throw "MSYS actor failed: $($Actor.Name)" }
}
function Invoke-Owned([string]$Name, [string]$Executable, [string[]]$Arguments, [int]$Milliseconds, [string]$InputFile = '') {
    $process = Start-Owned $Name $Executable $Arguments $stable
    try {
        if ($InputFile) {
            $process.Input = [IO.File]::OpenRead($InputFile)
            Await-Task ($process.Input.CopyToAsync($process.Process.StandardInput.BaseStream))
        }
        $process.Process.StandardInput.Close()
        $watch = [Diagnostics.Stopwatch]::StartNew()
        # Bounded kernel waits are watchdogs, never actor synchronization.
        while (-not $process.Process.WaitForExit(250)) {
            if ($null -ne $keeper) { Assert-Alive $keeper }
            if ($watch.ElapsedMilliseconds -ge $Milliseconds) { throw "Owned process timed out: $Name" }
        }
    } finally { Stop-Owned $process }
    if (-not $process.Joined -or -not $process.Drained -or $process.Forced) { throw "Owned process incomplete: $Name" }
    return $process.Exit
}
function Start-Actor([string]$Name, [string]$Temp) {
    $program = @'
set -eu
while IFS=' ' read -r nonce phase; do
    test "$nonce" = "$MSYS_OWNER_NONCE" || exit 91
    case "$phase" in ready|before|retired|after) ;; *) exit 92 ;; esac
    printf '%s %s\n' "$nonce" "$phase"
    "$MSYS_OWNER_CYGPATH" -w /tmp
    "$MSYS_OWNER_CYGPATH" -w "$TEMP"
    "$MSYS_OWNER_CYGPATH" -w "$TMP"
    if test -d /tmp; then printf 'present\n'; else printf 'retired\n'; fi
done
'@
    return Start-Owned $Name $shell @('--noprofile', '--norc', '-p', '-c', $program) $Temp $true
}
function Assert-Events([string]$Name, [string[]]$Required, [bool]$Negative = $false) {
    $conversion = Invoke-Owned "$Name-convert" $test2json @('-t', '-p', 'github.com/batuta-ai/console/internal/testfixture') 15000 (Join-Path $Evidence "$Name.stdout")
    Copy-Item -LiteralPath (Join-Path $Evidence "$Name-convert.stdout") -Destination (Join-Path $Evidence "$Name.json")
    if ($conversion -ne 0 -or [IO.File]::ReadAllText((Join-Path $Evidence "$Name-convert.stderr"))) { throw 'Test event conversion failed' }
    $events = @(Get-Content -LiteralPath (Join-Path $Evidence "$Name.json") | ForEach-Object { $_ | ConvertFrom-Json -ErrorAction Stop })
    $action = 'pass'
    if ($Negative) { $action = 'fail' }
    $runs = @($events | Where-Object { $_.Action -ceq 'run' } | ForEach-Object { $_.Test } | Sort-Object)
    $ends = @($events | Where-Object { $_.Action -ceq $action -and $_.PSObject.Properties['Test'] } | ForEach-Object { $_.Test } | Sort-Object)
    $terminal = @($events | Where-Object { $_.Action -in @('pass', 'fail', 'skip') -and -not $_.PSObject.Properties['Test'] })
    if (($runs -join "`n") -cne (($Required | Sort-Object) -join "`n") -or ($ends -join "`n") -cne ($runs -join "`n") -or
        $terminal.Count -ne 1 -or $terminal[0].Action -cne $action -or
        @($events | Where-Object { $_.Action -in @('pass', 'fail', 'skip') -and $_.Action -cne $action }).Count) { throw "Exact original test results required: $Name" }
    $raw = [IO.File]::ReadAllText((Join-Path $Evidence "$Name.stdout"))
    if ([IO.File]::ReadAllText((Join-Path $Evidence "$Name.stderr"))) { throw "Unexpected test process stderr: $Name" }
    if ($Negative) {
        # Require the original final assertion after successful compile/launch/exit44.
        # Compare Go's exact %q arrays; only the observed stderr may differ.
        $diagnosticEvents = [Collections.Generic.List[object]]::new()
        $pendingError = $null
        foreach ($record in $events) {
            $outputType = ''
            if ($record.PSObject.Properties['OutputType']) {
                if ($record.OutputType -isnot [string]) { throw 'Malformed negative output type' }
                $outputType = $record.OutputType
            }
            if ($null -ne $pendingError) {
                if ($record.Action -cne 'output' -or ($outputType -cne 'error-continue' -and
                    ($outputType -cne '' -or -not $record.PSObject.Properties['Output'] -or $record.Output -cne "`n")) -or
                    -not $record.PSObject.Properties['Package'] -or $record.Package -isnot [string] -or
                    -not $record.PSObject.Properties['Test'] -or $record.Test -isnot [string] -or
                    $record.Package -cne $pendingError.Package -or $record.Test -cne $pendingError.Test -or
                    -not $record.PSObject.Properties['Output'] -or $record.Output -isnot [string] -or $record.Output.Length -eq 0) {
                    throw 'Interrupted negative error continuation'
                }
                $pendingError.Output += $record.Output
            } else {
                if ($outputType -ceq 'error-continue') { throw 'Orphan negative error continuation' }
                if ($outputType -cnotin @('', 'frame', 'error') -or
                    ($outputType -cne '' -and $record.Action -cne 'output')) { throw 'Unknown negative output framing' }
                if ($outputType -cne 'error') { $diagnosticEvents.Add($record); continue }
                if (-not $record.PSObject.Properties['Package'] -or $record.Package -isnot [string] -or $record.Package.Length -eq 0 -or
                    -not $record.PSObject.Properties['Test'] -or $record.Test -isnot [string] -or $record.Test.Length -eq 0 -or
                    -not $record.PSObject.Properties['Output'] -or $record.Output -isnot [string] -or $record.Output.Length -eq 0) {
                    throw 'Malformed negative error start'
                }
                $pendingError = [pscustomobject]@{ Action = 'output'; Package = $record.Package; Test = $record.Test; Output = $record.Output }
            }
            # This collector admits one exact single-line assertion, never a multi-line error.
            $newline = $pendingError.Output.IndexOf("`n")
            if ($newline -ge 0) {
                if ($newline -ne $pendingError.Output.Length - 1) { throw 'Mixed negative error lines' }
                $diagnosticEvents.Add($pendingError)
                $pendingError = $null
            }
        }
        if ($null -ne $pendingError) { throw 'Incomplete negative error continuation' }
        $diagnostics = @($diagnosticEvents | Where-Object { $_.Action -ceq 'output' -and $_.Output -cmatch '^\s+\w+\.go:\d+: ' })
        $cause = @($diagnostics | Where-Object { $_.Output -cmatch '^\s+script_windows_test\.go:56: actual argv/stdin/home=' })
        $image = @($diagnostics | Where-Object { $_.Output -cmatch '^\s+script_windows_test\.go:38: actual native fixture image=' })
        if ($diagnostics.Count -ne 2 -or $cause.Count -ne 1 -or $image.Count -ne 1) { throw 'Negative has an unrelated original diagnostic' }
        $match = [regex]::Match($cause[0].Output, '^\s+script_windows_test\.go:56: actual argv/stdin/home=(\[.*\]) want=(\[.*\]) stderr="bash.exe: warning: could not find /tmp, please create!\\nactual-script-stderr" truncated=false/false\r?\n$')
        if (-not $match.Success -or $match.Groups[1].Value -cne $match.Groups[2].Value) { throw 'Negative is not exclusively the retired-/tmp original assertion' }
    } elseif ($raw -cmatch 'could not find /tmp|warning:.* /tmp') { throw 'Positive /tmp warning' }
}
function Invoke-Literal([string]$Name, [bool]$Negative) {
    $witnessHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $FixtureBinary).Hash
    if ($witnessHash -cne $fixtureHash) { throw 'Original compiled witness changed between arms' }
    $observations.Add(@{ event = 'literal'; name = $Name; binary = $FixtureBinary; sha256 = $witnessHash; negative = $Negative })
    $exitCode = Invoke-Owned $Name $FixtureBinary @('-test.run=^TestNativeScriptLiteralArgumentsAndExit$', '-test.v=test2json', '-test.count=1', '-test.timeout=1m') 75000
    $expectedExit = 0
    if ($Negative) { $expectedExit = 1 }
    if ($exitCode -ne $expectedExit) { throw "Original literal exit differs: $Name ($exitCode)" }
    Assert-Events $Name @('TestNativeScriptLiteralArgumentsAndExit') $Negative
}
function Invoke-Retirement([string]$Arm, [string]$Backing, [string]$Identity) {
    $aRoot = Join-Path $stable ("msys-$Arm-a-" + [guid]::NewGuid().ToString('N'))
    $bRoot = Join-Path $stable ("msys-$Arm-b-" + [guid]::NewGuid().ToString('N'))
    foreach ($root in @($aRoot, $bRoot)) { $null = [IO.Directory]::CreateDirectory($root); $roots.Add($root) }
    if ($Arm -ceq 'negative') { $Backing = $aRoot; $Identity = [MsysOwnerNative]::DirectoryIdentity($aRoot) }
    $a = $b = $null
    try {
        $a = Start-Actor "msys-$Arm-a" $aRoot
        Observe-Actor $a 'ready' $Backing $Identity
        $b = Start-Actor "msys-$Arm-b" $bRoot
        Observe-Actor $b 'ready' $Backing $Identity
        Assert-Alive $a
        Stop-Owned $a
        Assert-ActorClosed $a
        Assert-Alive $b
        [IO.Directory]::Delete($aRoot, $false)
        if ([IO.Directory]::Exists($aRoot)) { throw 'Actor A retirement failed' }
        $observations.Add(@{ event = 'retirement'; name = $a.Name; root = $aRoot; survivorOriginalHandleAlive = $true })
        Observe-Actor $b 'retired' $Backing $Identity ($Arm -ceq 'negative')
        Invoke-Literal "msys-$Arm-literal" ($Arm -ceq 'negative')
        Observe-Actor $b 'after' $Backing $Identity ($Arm -ceq 'negative')
    } finally {
        foreach ($actor in @($a, $b)) { if ($null -ne $actor) { Stop-Owned $actor } }
    }
    Assert-ActorClosed $b
}

try {
    [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
    if (-not $stable -or $env:TMP -ine $stable -or $env:LOCALAPPDATA -ine $stable) { throw 'Caller-owned stable TEMP/TMP/LOCALAPPDATA required' }
    $stableIdentity = [MsysOwnerNative]::DirectoryIdentity($stable)
    $stableAcl = (Get-Acl -LiteralPath $stable).Sddl
    foreach ($path in @($Source, $Evidence)) { $null = [MsysOwnerNative]::DirectoryIdentity($path) }
    foreach ($path in @($Go, $Git, $FixtureBinary, $shell, (Join-Path $utilities 'cygpath.exe'))) {
        if (-not [IO.Path]::IsPathFullyQualified($path) -or -not [IO.File]::Exists($path)) { throw "Actual installed input missing: $path" }
    }
    if ((Get-Command go -CommandType Application | Select-Object -First 1).Source -ine $Go -or (Get-Command git -CommandType Application | Select-Object -First 1).Source -ine $Git) { throw 'Process-local tool resolution differs' }
    $version = & $Go version
    if ($LASTEXITCODE -ne 0 -or $version -cne 'go version go1.27.3 windows/amd64') { throw 'Pinned native compiler required' }
    $goroot = & $Go env GOROOT
    if ($LASTEXITCODE -ne 0) { throw 'GOROOT unavailable' }
    $test2json = & $Go tool -n test2json
    if ($LASTEXITCODE -ne 0 -or @($test2json).Count -ne 1 -or -not [IO.Path]::IsPathFullyQualified($test2json) -or
        -not [IO.File]::Exists($test2json)) { throw 'Pinned compiler test2json tool unavailable' }
    if ($ExpectedSources) {
        # The credentialed actor reads the parent's hash-bound inventory, avoiding
        # repository ownership exceptions or any safe.directory configuration.
        $sourceFiles = @($ExpectedSources | ForEach-Object {
            if ((Get-FileHash -Algorithm SHA256 -LiteralPath $_.Path).Hash -cne $_.Hash) { throw 'Staged source manifest differs' }
            $_.Path
        })
    } else {
        $tracked = & $Git -C $Source ls-files
        if ($LASTEXITCODE -ne 0) { throw 'Source inventory unavailable' }
        $sourceFiles = @($tracked | ForEach-Object { Join-Path $Source $_ })
    }
    $hashFiles = $sourceFiles + @($PSCommandPath, $FixtureBinary, $Go, $Git, $shell,
        (Join-Path $utilities 'cygpath.exe'), (Join-Path $utilities 'msys-2.0.dll'), $test2json)
    $hashFiles += @(Get-ChildItem -LiteralPath (Join-Path $goroot 'pkg/tool/windows_amd64') -Filter '*.exe' -File | ForEach-Object { $_.FullName })
    $hashFiles = @($hashFiles | ForEach-Object { [IO.Path]::GetFullPath($_) } | Sort-Object -Unique)
    $fixtureHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $FixtureBinary).Hash
    $beforeHashes = @($hashFiles | ForEach-Object { Get-FileHash -Algorithm SHA256 -LiteralPath $_ } | Select-Object Path, Hash)
    Save-Json 'msys-hashes-before.json' $beforeHashes
    $frozen = @{ 'ambient_temp_windows_test.go' = 'd7db825ef12dd35e2770002fcbbff6bc005661585c18dd8f8dc0c60f1790187f';
        'msys_temp_lifetime_windows_test.go' = '161601257feaefe40ce6c4e57bf6fd7f9246403406f0a5066e398905be2de5b5';
        'command_windows.go' = '50231d30408b7c65192522f274162d6825ad9c60c54813e8f4dc9b46f5be1116' }
    foreach ($name in $frozen.Keys) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Source "internal/testfixture/$name")).Hash -ine $frozen[$name]) { throw "Frozen source differs: $name" }
    }
    $current = [Diagnostics.Process]::GetCurrentProcess()
    try {
        Save-Json 'msys-caller.json' @{ pid = $PID; sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value;
            tokenType = [MsysOwnerNative]::TokenInt($current, 8); elevationType = [MsysOwnerNative]::TokenInt($current, 18);
            elevated = [MsysOwnerNative]::TokenInt($current, 20); originalCurrentHandle = $current.SafeHandle.DangerousGetHandle().ToInt64();
            stable = $stable; stableIdentity = $stableIdentity; stableAcl = $stableAcl; go = $Go; goroot = $goroot;
            git = $Git; version = $version; path = $env:PATH; gocache = $env:GOCACHE; goenv = $env:GOENV;
            gotoolchain = $env:GOTOOLCHAIN; goflags = $env:GOFLAGS; gowork = $env:GOWORK; goproxy = $env:GOPROXY; gosumdb = $env:GOSUMDB }
    } finally { $current.Dispose() }
    Invoke-Retirement 'negative' '' ''
    if ($cleanupErrors.Count) { throw 'Negative finalization failed; refusing positive' }
    $keeper = Start-Actor 'msys-positive-keeper' $stable
    Observe-Actor $keeper 'ready' $stable $stableIdentity
    Invoke-Retirement 'positive' $stable $stableIdentity
    Observe-Actor $keeper 'before' $stable $stableIdentity
    if ($FullSuite) {
        $suiteExit = Invoke-Owned 'go-test' $Go @('test', './...', '-json', '-count=1', '-timeout=20m') 1500000
        Copy-Item -LiteralPath (Join-Path $Evidence 'go-test.stdout') -Destination (Join-Path $Evidence 'go-test.json')
    } else {
        $required = @('TestTerminalEnvironmentPreservesOrdinaryUserTEMP', 'TestTerminalEnvironmentPreservesOrdinaryUserTEMP/select_environment',
            'TestTerminalEnvironmentMSYSTempLifetime', 'TestTerminalEnvironmentMSYSTempLifetime/first_scope',
            'TestTerminalEnvironmentMSYSTempLifetime/later_original_literal', 'TestNativeScriptLiteralArgumentsAndExit', 'TestNativeScriptInPlacePreservesLiveSession')
        $selection = '^Test(TerminalEnvironmentPreservesOrdinaryUserTEMP|TerminalEnvironmentMSYSTempLifetime|NativeScriptLiteralArgumentsAndExit|NativeScriptInPlacePreservesLiveSession)$'
        $exitCode = Invoke-Owned 'msys-ordinary-fixtures' $FixtureBinary @("-test.run=$selection", '-test.v=test2json', '-test.count=1', '-test.timeout=5m') 315000
        Assert-Events 'msys-ordinary-fixtures' $required
        if ($exitCode -ne 0) { throw 'Ordinary original operation fixtures failed' }
        & $Operation
    }
    Observe-Actor $keeper 'after' $stable $stableIdentity
} catch { $primary = $_ } finally {
    for ($index = $owned.Count - 1; $index -ge 0; $index--) { Stop-Owned $owned[$index] }
    if ($null -ne $keeper) { try { Assert-ActorClosed $keeper } catch { $cleanupErrors.Add("Keeper: $_") } }
    try {
        if ($null -ne $beforeHashes) {
            $afterHashes = @($hashFiles | ForEach-Object { Get-FileHash -Algorithm SHA256 -LiteralPath $_ } | Select-Object Path, Hash)
            Save-Json 'msys-hashes-after.json' $afterHashes
            if (($beforeHashes | ConvertTo-Json -Compress) -cne ($afterHashes | ConvertTo-Json -Compress)) { throw 'Source/compiler/controller/binary changed' }
        }
        if ([MsysOwnerNative]::DirectoryIdentity($stable) -cne $stableIdentity -or (Get-Acl -LiteralPath $stable).Sddl -cne $stableAcl) { throw 'Caller stable root identity/ACL changed' }
        if (@($owned | Where-Object { $_.Started -and (-not $_.Joined -or -not $_.Drained) }).Count) { throw 'Owned processes not drained; retaining roots' }
        foreach ($root in $roots) { if ([IO.Directory]::Exists($root)) { [IO.Directory]::Delete($root, $false) } }
    } catch { $cleanupErrors.Add("Final guards: $_") }
    try { [Console]::OutputEncoding = $consoleEncoding } catch { $cleanupErrors.Add("Console encoding restore: $_") }
    try { Save-Json 'msys-observations.json' @($observations.ToArray()) } catch { $cleanupErrors.Add("Observation evidence: $_") }
    try {
        Save-Json 'msys-result.json' @{ primary = [string]$primary; cleanup = @($cleanupErrors.ToArray()); suiteExit = $suiteExit;
            success = ($null -eq $primary -and $cleanupErrors.Count -eq 0 -and ($null -eq $suiteExit -or $suiteExit -eq 0)) }
    } catch { $cleanupErrors.Add("Result evidence: $_") }
}
foreach ($failure in $cleanupErrors) { [Console]::Error.WriteLine("MSYS cleanup: $failure") }
if ($primary) { throw $primary }
if ($cleanupErrors.Count) { throw ($cleanupErrors -join "`n") }
if ($FullSuite) { return $suiteExit }
