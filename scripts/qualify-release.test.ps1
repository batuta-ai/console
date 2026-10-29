#Requires -Version 7.4
[CmdletBinding()]
param([Parameter(Mandatory)][string]$Inputs, [Parameter(Mandatory)][string]$Evidence)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'Native actual-ZIP controls required' }
$root = Split-Path $Inputs
$labels = Get-Content -LiteralPath (Join-Path $root 'labels.json') -Raw | ConvertFrom-Json
$qualifier = Join-Path $root 'qualify-release.ps1'
$archive = Join-Path $root $labels.archive
$checksums = Join-Path $root 'checksums.txt'
function Digest([string]$Path) { (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant() }
$original = Digest $qualifier
$sourceLines = [IO.File]::ReadAllLines($qualifier)
function Guard-Line([string]$Message) {
    $matches = @(for ($i=0; $i -lt $sourceLines.Length; $i++) {
        if ($sourceLines[$i].Contains("throw '$Message'")) { $i+1 }
    })
    if ($matches.Count -ne 1) { throw 'Frozen guard location must be unique' }
    return $matches[0]
}
Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
public static class BundleCaseStreams {
 public static async Task<string> Read(Stream stream, CancellationToken token) {
  using (var data = new MemoryStream()) {
   var buffer = new byte[4096]; int count;
   while ((count = await stream.ReadAsync(buffer,0,buffer.Length,token).ConfigureAwait(false)) != 0) {
    if (data.Length + count > 65536) throw new InvalidOperationException("Qualifier stream exceeds 64 KiB");
    data.Write(buffer,0,count);
   }
   return System.Text.Encoding.UTF8.GetString(data.ToArray());
  }
 }
}
'@
if ($original -cne '5c77d83fce63ec9321c0ce1a24a1d0464742f528f5b3605c3a6117d847137074') { throw 'Frozen consumer differs' }
$null = New-Item -ItemType Directory -Path $Evidence
$cases = @('counterfactual','checksum','missing','duplicate','extra','renamed','traversal','relative','ads','dotdot','pe','version','revision','go','core','positive')
$results = [Collections.Generic.List[object]]::new()
$caseFailure=$null
$wrapper = Join-Path $root 'invoke-qualifier.ps1'
@'
param($Qualifier,$Archive,$Checksums,$ExpectedVersion,$ExpectedRevision,$Evidence,$Actor,$ExpectedSid)
$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class QualifierToken {
 [DllImport("advapi32.dll", SetLastError=true)] static extern bool GetTokenInformation(IntPtr t,int k,out int v,int s,out int n);
 public static int Read(IntPtr t,int k) { int v,n; if (!GetTokenInformation(t,k,out v,4,out n) || n!=4) throw new Exception("Token query failed"); return v; }
}
"@
$id = [Security.Principal.WindowsIdentity]::GetCurrent()
$type = [QualifierToken]::Read($id.Token,8)
$elevation = [QualifierToken]::Read($id.Token,18)
$elevated = [QualifierToken]::Read($id.Token,20)
$groups = & "$env:SystemRoot\System32\whoami.exe" /groups /fo csv /nh
if ($LASTEXITCODE -ne 0) { throw 'Group query failed' }
$privileges = & "$env:SystemRoot\System32\whoami.exe" /priv /fo csv /nh
if ($LASTEXITCODE -ne 0) { throw 'Privilege query failed' }
if ($id.User.Value -cne $ExpectedSid -or $type -ne 1 -or $elevation -ne 1 -or $elevated -ne 0 -or
    ($groups -join '') -match 'S-1-5-32-544' -or ($privileges -join '') -match 'SeCreateSymbolicLinkPrivilege') { throw 'Qualifier must inherit ordinary primary token' }
@{ sid=$id.User.Value; pid=$PID; created=[Diagnostics.Process]::GetCurrentProcess().StartTime.ToFileTimeUtc(); tokenType=$type;
 elevationType=$elevation; elevated=$elevated; groups=$groups; privileges=$privileges } | ConvertTo-Json | Set-Content -LiteralPath $Actor
& $Qualifier -Archive $Archive -Checksums $Checksums -ExpectedVersion $ExpectedVersion -ExpectedRevision $ExpectedRevision -Evidence $Evidence
'@ | Set-Content -LiteralPath $wrapper
function Assert-Rejection($Receipt, $Events, [int]$ExitCode, [string]$Stage, [int]$Launches, [int]$Line) {
    if ($ExitCode -eq 0 -or $Receipt.native -cne 'failed' -or $Receipt.stage -cne $Stage -or
        $null -eq $Receipt.primary -or $Receipt.primary.stage -cne $Stage -or $Receipt.primary.line -ne $Line -or @($Receipt.cleanup).Count -ne 0 -or
        @($Events | Where-Object event -eq 'launch').Count -ne $Launches -or
        @($Events | Where-Object { $_.event -eq 'launch' -and ($_.argv -join ',') -cne '-version' }).Count -ne 0) {
        throw 'Intended rejection/launch boundary not observed'
    }
}
function Replace-ImageData([byte[]]$Bytes, [string]$Before, [string]$After) {
    if ($Before.Length -ne $After.Length) { throw 'Equal-length data mutation required' }
    $text = [Text.Encoding]::Latin1.GetString($Bytes)
    $count = [regex]::Matches($text, [regex]::Escape($Before)).Count
    if ($count -lt 1) { throw 'Actual image provenance data not found' }
    return @{ bytes = [Text.Encoding]::Latin1.GetBytes($text.Replace($Before,$After)); count=$count }
}
try {
    foreach ($case in $cases) {
        $dir = Join-Path $root $case
        $null = New-Item -ItemType Directory -Path $dir
        $zipPath = Join-Path $dir $labels.archive
        $sumPath = Join-Path $dir 'checksums.txt'
        Copy-Item -LiteralPath $archive -Destination $zipPath
        Copy-Item -LiteralPath $checksums -Destination $sumPath
        $mutation = @{ case=$case; archiveBefore=(Digest $zipPath); qualifierBefore=$original; dataMutation=$false }
        if ($case -in @('missing','duplicate','extra','renamed','traversal','pe','go','core')) {
            $zip = [IO.Compression.ZipFile]::Open($zipPath,'Update')
            try {
                if ($case -eq 'missing') { $zip.GetEntry('README.md').Delete() }
                elseif ($case -eq 'extra') {
                    $stream = $zip.CreateEntry('extra.txt').Open()
                    try { $stream.WriteByte(65) } finally { $stream.Dispose() }
                } elseif ($case -in @('duplicate','renamed','traversal')) {
                    $entry = $zip.GetEntry('README.md')
                    $stream = $entry.Open(); $memory = [IO.MemoryStream]::new()
                    try { $stream.CopyTo($memory); $bytes=$memory.ToArray() } finally { $stream.Dispose(); $memory.Dispose() }
                    $entry.Delete()
                    $name = switch ($case) { duplicate { 'LICENSE' }; renamed { 'renamed.md' }; traversal { '../escaped.txt' } }
                    $stream = $zip.CreateEntry($name).Open()
                    try { $stream.Write($bytes,0,$bytes.Length) } finally { $stream.Dispose() }
                } else {
                    $name = 'console.exe'
                    if ($case -eq 'core') { $name = 'consoled.exe' }
                    $entry = $zip.GetEntry($name)
                    $stream = $entry.Open(); $memory = [IO.MemoryStream]::new()
                    try { $stream.CopyTo($memory); $bytes=$memory.ToArray() } finally { $stream.Dispose(); $memory.Dispose() }
                    $mutation.image=$name
                    $mutation.imageBefore=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
                    if ($case -eq 'pe') { $offset=[BitConverter]::ToInt32($bytes,0x3c); $bytes[$offset+4]=0x4c; $bytes[$offset+5]=0x01 }
                    else {
                        $before='go1.27.2'; $after='go1.27.3'
                        if ($case -eq 'core') { $before='v1.1.0-beta.55.1'; $after='v1.1.0-beta.55.0' }
                        $changed=Replace-ImageData $bytes $before $after
                        $bytes=$changed.bytes
                        $mutation.dataMutation=$true; $mutation.replacements=$changed.count
                        $mutation.before=$before; $mutation.after=$after
                        $mutation.limit='Data-mutated actual artifact; NOT different SDK build qualification'
                    }
                    $mutation.imageAfter=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
                    $entry.Delete(); $stream=$zip.CreateEntry($name).Open()
                    try { $stream.Write($bytes,0,$bytes.Length) } finally { $stream.Dispose() }
                }
            } finally { $zip.Dispose() }
            "$(Digest $zipPath)  $($labels.archive)" | Set-Content -LiteralPath $sumPath
        }
        $version=$labels.version; $revision=$labels.revision; $scriptPath=$qualifier
        if ($case -in @('checksum','counterfactual')) { "$('0'*64)  $($labels.archive)" | Set-Content -LiteralPath $sumPath }
        if ($case -eq 'version') { $version='wrong-version' }
        if ($case -eq 'revision') { $revision='0'*40 }
        if ($case -eq 'counterfactual') {
            $sourceText=[IO.File]::ReadAllText($qualifier)
            $guard="if (-not `$expectedHash -or `$archiveHash -cne `$expectedHash) { throw 'Archive checksum mismatch' }"
            if ([regex]::Matches($sourceText,[regex]::Escape($guard)).Count -ne 1) { throw 'Exact single checksum guard required' }
            $scriptPath=Join-Path $dir 'mutant.ps1'
            [IO.File]::WriteAllText($scriptPath,$sourceText.Replace($guard,"if (`$false) { throw 'Archive checksum mismatch' }"))
            $mutation.guardBefore=$guard; $mutation.guardAfter="if (`$false) { throw 'Archive checksum mismatch' }"
            $version='wrong-version'
        }
        $mutation.archiveAfter=Digest $zipPath; $mutation.checksums=Digest $sumPath; $mutation.qualifierAfter=Digest $scriptPath
        $mutation | ConvertTo-Json | Set-Content (Join-Path $Evidence "bundle-$case-mutation.json")
        $inputPath=$zipPath
        if ($case -eq 'relative') { $inputPath='.\'+$labels.archive }
        if ($case -eq 'ads') { $inputPath=$zipPath+':payload' }
        if ($case -eq 'dotdot') { $inputPath=$dir+'\..\'+$case+'\'+$labels.archive }
        $caseEvidence=Join-Path $dir 'evidence'
        $actorPath=Join-Path $Evidence "bundle-$case-actor.json"
        $start=[Diagnostics.ProcessStartInfo]::new((Join-Path $PSHOME 'pwsh.exe'))
        $arguments=@('-NoLogo','-NoProfile','-NonInteractive','-File',$wrapper,'-Qualifier',$scriptPath,'-Archive',$inputPath,
            '-Checksums',$sumPath,'-ExpectedVersion',$version,'-ExpectedRevision',$revision,'-Evidence',$caseEvidence,
            '-Actor',$actorPath,'-ExpectedSid',[Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
        foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
        $start.UseShellExecute=$false; $start.WorkingDirectory=$dir
        $start.RedirectStandardOutput=$true; $start.RedirectStandardError=$true
        $process=[Diagnostics.Process]::new(); $process.StartInfo=$start
        $out=$err=$null; $started=$false; $exitCode=$null
        $primaryError=$null; $failureStage='Qualifier did not start'
        $cleanupErrors=[Collections.Generic.List[string]]::new()
        $stopAttempted=$false; $originalJoined=$false
        $cancel=[Threading.CancellationTokenSource]::new(230000)
        try {
            $started=$process.Start()
            if (-not $started) { throw 'Qualifier did not start' }
            $failureStage='Qualifier capture setup failed'
            $created=$process.StartTime.ToFileTimeUtc(); $childPid=$process.Id
            $out=[BundleCaseStreams]::Read($process.StandardOutput.BaseStream,$cancel.Token)
            $err=[BundleCaseStreams]::Read($process.StandardError.BaseStream,$cancel.Token)
            $failureStage='Qualifier deadline exceeded'
            if (-not $process.WaitForExit(220000)) { throw 'Qualifier deadline exceeded' }
            $failureStage='Qualifier streams did not drain'
            if (-not $out.Wait(5000) -or -not $err.Wait(5000)) { throw 'Qualifier streams did not drain' }
            $failureStage='Qualifier capture persistence failed'
            $exitCode=$process.ExitCode
            [IO.File]::WriteAllText((Join-Path $dir 'stdout.private'),$out.Result)
            [IO.File]::WriteAllText((Join-Path $dir 'stderr.private'),$err.Result)
        } catch { $primaryError=$_ } finally {
            try {
                if ($started) {
                    $originalJoined=$process.HasExited
                    if (-not $originalJoined) {
                        $stopAttempted=$true
                        $process.Kill()
                        $originalJoined=$process.WaitForExit(5000)
                        if (-not $originalJoined) { $cleanupErrors.Add('Original process failed to join') }
                    }
                }
            } catch { $cleanupErrors.Add('Original process stop/join failed') }
            try { $cancel.Cancel() } catch { $cleanupErrors.Add('Stream cancellation failed') }
            $stdoutDrained=($null -ne $out -and $out.IsCompletedSuccessfully)
            $stderrDrained=($null -ne $err -and $err.IsCompletedSuccessfully)
            try { $cancel.Dispose() } catch { $cleanupErrors.Add('Stream cancellation disposal failed') }
            try { $process.Dispose() } catch { $cleanupErrors.Add('Original process disposal failed') }
        }
        if ($null -ne $primaryError -or $cleanupErrors.Count -ne 0) {
            if ($null -eq $primaryError) { $failureStage='Qualifier cleanup failed' }
            $caseFailure=@{ case=$case; primary=$failureStage; cleanup=$cleanupErrors.ToArray();
                originalProcessStopAttempted=$stopAttempted; originalProcessJoined=$originalJoined;
                stdoutDrained=$stdoutDrained; stderrDrained=$stderrDrained;
                descendants='descendant cleanup unverified/incomplete' }
            $message="$failureStage; original process stop attempted=$stopAttempted; original process joined=$originalJoined; descendant cleanup unverified/incomplete"
            if ($cleanupErrors.Count -ne 0) { $message+='; cleanup: '+($cleanupErrors -join '; ') }
            if ($null -ne $primaryError) {
                $primaryError.ErrorDetails=[Management.Automation.ErrorDetails]::new($message)
                throw $primaryError
            }
            throw $message
        }
        $actor=Get-Content -LiteralPath $actorPath -Raw | ConvertFrom-Json
        if ($actor.sid -cne [Security.Principal.WindowsIdentity]::GetCurrent().User.Value -or $actor.pid -ne $childPid -or
            $actor.created -ne $created -or $actor.tokenType -ne 1 -or $actor.elevationType -ne 1 -or $actor.elevated -ne 0) { throw 'Qualifier actor binding differs' }
        $observation=@{ case=$case; exit=$exitCode; pid=$childPid; created=$created; arguments=$arguments; stdoutSHA256=(Digest (Join-Path $dir 'stdout.private')); stderrSHA256=(Digest (Join-Path $dir 'stderr.private')) }
        $observation | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $Evidence "bundle-$case-result.json")
        if ($case -in @('relative','ads','dotdot')) {
            if ($exitCode -eq 0 -or (Test-Path -LiteralPath $caseEvidence) -or $err.Result -notmatch 'Artifact qualification failed at paths') { throw 'Canonical input rejection missing' }
        } else {
            foreach ($name in @('receipt','observations')) { Copy-Item -LiteralPath (Join-Path $caseEvidence "$name.json") -Destination (Join-Path $Evidence "bundle-$case-$name.json") }
            $receipt=Get-Content (Join-Path $caseEvidence 'receipt.json') -Raw | ConvertFrom-Json
            $events=@(Get-Content (Join-Path $caseEvidence 'observations.json') -Raw | ConvertFrom-Json)
            if ($case -eq 'positive') {
                if ($exitCode -ne 0 -or $err.Result -or $receipt.native -cne 'passed' -or $receipt.stage -cne 'complete' -or $receipt.primary -or @($receipt.cleanup).Count -ne 0 -or
                    @($events | Where-Object event -eq 'bound').Count -ne 2 -or @($events | Where-Object event -eq 'reuse').Count -ne 1 -or
                    @($events | Where-Object event -eq 'direct-crash-termination').Count -ne 1 -or @($events | Where-Object event -eq 'daemon-log-closed').Count -ne 2 -or
                    @($events | Where-Object { $_.event -eq 'http' -and $_.status -eq 401 }).Count -ne 8 -or
                    @($events | Where-Object { $_.event -eq 'cleanup' -and $_.originalObjectJoined }).Count -ne 2 -or
                    $receipt.hashes.archive -cne $labels.archiveSHA256 -or (Digest $zipPath) -cne $labels.archiveSHA256 -or (Digest $sumPath) -cne $labels.checksumsSHA256) { throw 'Positive actual artifact contract failed' }
            } elseif ($case -eq 'counterfactual') {
                Assert-Rejection $receipt $events $exitCode 'provenance' 1 (Guard-Line 'Unexpected binary provenance')
                $red=$false
                try { Assert-Rejection $receipt $events $exitCode 'checksum' 0 (Guard-Line 'Archive checksum mismatch') } catch { if ($_.Exception.Message -cne 'Intended rejection/launch boundary not observed') { throw }; $red=$true }
                if (-not $red) { throw 'Critical guard counterfactual failed to make harness RED' }
            } else {
                $stage='archive'; $launches=0
                if ($case -eq 'checksum') { $stage='checksum' }
                if ($case -in @('pe','version','revision','go','core')) { $stage='provenance' }
                if ($case -in @('version','revision','go')) { $launches=1 }
                if ($case -eq 'core') { $launches=2 }
                $message = switch ($case) {
                    checksum { 'Archive checksum mismatch' }
                    { $_ -in @('missing','extra') } { 'Archive must contain exactly five members' }
                    { $_ -in @('duplicate','renamed','traversal') } { 'Wrong, duplicate or unsafe archive member' }
                    pe { 'Native Windows amd64 PE required' }
                    core { 'Unexpected linked Core provenance' }
                    default { 'Unexpected binary provenance' }
                }
                Assert-Rejection $receipt $events $exitCode $stage $launches (Guard-Line $message)
            }
        }
        if ((Digest $scriptPath) -cne $mutation.qualifierAfter -or (Digest $zipPath) -cne $mutation.archiveAfter -or (Digest $sumPath) -cne $mutation.checksums) { throw 'Case source/input changed during execution' }
        if ((Digest $qualifier) -cne $original -or (Digest $archive) -cne $labels.archiveSHA256 -or (Digest $checksums) -cne $labels.checksumsSHA256) { throw 'Original source/input changed' }
        if (Test-Path -LiteralPath (Join-Path $root 'escaped.txt')) { throw 'Traversal escaped' }
        $results.Add(@{ case=$case; passed=$true; exit=$exitCode; counterfactualRED=($case -eq 'counterfactual') })
    }
} finally {
    @{ cases=$results.ToArray(); expected=$cases; complete=($results.Count -eq $cases.Count); qualifierSHA256=(Digest $qualifier);
        archiveSHA256=(Digest $archive); checksumsSHA256=(Digest $checksums); failure=$caseFailure; native='PENDING until complete and independently inspected' } |
        ConvertTo-Json -Depth 5 | Set-Content (Join-Path $Evidence 'bundle-summary.json')
}
