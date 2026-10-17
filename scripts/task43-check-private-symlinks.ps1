#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('Prepare', 'Consume', 'Cleanup')][string]$Phase,
    [Parameter(Mandatory = $true)][string]$RuntimeTest,
    [Parameter(Mandatory = $true)][string]$SetupTest,
    [Parameter(Mandatory = $true)][string]$ReceiptDirectory,
    [Parameter(Mandatory = $true)][string]$NativeBuildReceipt,
    [string]$PrivateParent,
    [string]$Manifest,
    [string]$ProducerReceipt
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# The conductor calls this script from its owned primary-child wrapper through the existing launcher,
# joins that original child handle, and retains launcher metadata/exit/stdout/stderr.
# Prepare exits before the elevated producer starts. Consume requires its joined receipt.
# Supply binaries built natively on Windows from this full source tree (runtime.Caller).
$Launcher = 'C:\Users\franc\.local\batuta-console-qa\windows\qa-primary-full-access-20261014.exe'
$LauncherHash = 'f33c7aa4675ceec245828ffad0deb111b6ffa4127a47832cba3a3a8c0550c0f1'
$ExpectedSid = 'S-1-5-21-3534651229-1508634349-1091504176-1001'
$Identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$Principal = New-Object Security.Principal.WindowsPrincipal($Identity)
if ($Identity.User.Value -cne $ExpectedSid -or $Principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Requires unchanged ordinary identity.' }
if ((Get-FileHash -LiteralPath $Launcher -Algorithm SHA256).Hash.ToLowerInvariant() -cne $LauncherHash) { throw 'Ordinary launcher hash mismatch.' }
if (-not [IO.Directory]::Exists($ReceiptDirectory)) { throw 'Conductor must provide a fresh receipt directory outside the fixture root.' }
foreach ($Binary in @($RuntimeTest, $SetupTest)) {
    if (-not [IO.File]::Exists($Binary) -or [IO.Path]::GetExtension($Binary) -cne '.exe') { throw 'Requires native-built Windows test executables.' }
}
$SourceRoot = Split-Path -Parent $PSScriptRoot
$Build = Get-Content -LiteralPath $NativeBuildReceipt -Raw | ConvertFrom-Json
if ($Build.joined -ne $true -or $Build.exit -ne 0 -or $Build.gohostos -cne 'windows' -or $Build.go_version -cne 'go version go1.27.2 windows/amd64' -or $Build.source_root -cne $SourceRoot -or $Build.runtime_sha256 -cne (Get-FileHash -LiteralPath $RuntimeTest -Algorithm SHA256).Hash.ToLowerInvariant() -or $Build.setup_sha256 -cne (Get-FileHash -LiteralPath $SetupTest -Algorithm SHA256).Hash.ToLowerInvariant() -or $Build.go_sha256 -cne (Get-FileHash -LiteralPath $Build.go_executable -Algorithm SHA256).Hash.ToLowerInvariant()) { throw 'Requires conductor native Windows build receipt, original process join and tool/binary hashes.' }
if ($Build.runtime_source_sha256 -cne (Get-FileHash -LiteralPath (Join-Path $SourceRoot 'internal/runtime/task43_preexisting_symlink_windows_test.go') -Algorithm SHA256).Hash.ToLowerInvariant() -or $Build.setup_source_sha256 -cne (Get-FileHash -LiteralPath (Join-Path $SourceRoot 'integrations/batuta/task43_preexisting_symlink_windows_test.go') -Algorithm SHA256).Hash.ToLowerInvariant()) { throw 'Native build source differs from the current companions.' }
$Paths = @($Launcher, $RuntimeTest, $SetupTest, $PSCommandPath,
    (Join-Path $PSScriptRoot 'task43-prepare-private-symlinks.ps1'),
    (Join-Path $SourceRoot 'internal/runtime/task43_preexisting_symlink_windows_test.go'),
    (Join-Path $SourceRoot 'integrations/batuta/task43_preexisting_symlink_windows_test.go'), $Build.go_executable, $NativeBuildReceipt)
$Hashes = @($Paths | ForEach-Object { [ordered]@{ path = $_; sha256 = (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash.ToLowerInvariant() } })
$HashPath = Join-Path $ReceiptDirectory ($Phase.ToLowerInvariant() + '-hashes.json')
if ([IO.File]::Exists($HashPath)) { throw 'Refusing to overwrite receipts.' }
$Hashes | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $HashPath -Encoding UTF8

function Invoke-Qualification([string]$Binary, [string]$Test, [string[]]$Required) {
    $Prefix = Join-Path $ReceiptDirectory $Test
    if ([IO.File]::Exists($Prefix + '.stdout.log') -or [IO.File]::Exists($Prefix + '.stderr.log')) { throw 'Refusing to overwrite raw test receipts.' }
    $Info = New-Object Diagnostics.ProcessStartInfo
    $Info.FileName = [IO.Path]::GetFullPath($Binary)
    $Info.Arguments = "-test.run=^$Test`$ -test.v -test.count=1 -test.timeout=2m"
    $Info.UseShellExecute = $false
    $Info.RedirectStandardOutput = $true
    $Info.RedirectStandardError = $true
    $Process = New-Object Diagnostics.Process
    $Process.StartInfo = $Info
    $Stdout = $null; $Stderr = $null; $Started = $false
    try {
        $Stdout = [IO.File]::Open($Prefix + '.stdout.log', [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::Read)
        $Stderr = [IO.File]::Open($Prefix + '.stderr.log', [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::Read)
        $Started = $Process.Start()
        if (-not $Started) { throw 'Test process did not start.' }
        $OriginalHandle = $Process.Handle
        $OutCopy = $Process.StandardOutput.BaseStream.CopyToAsync($Stdout)
        $ErrCopy = $Process.StandardError.BaseStream.CopyToAsync($Stderr)
        $Process.WaitForExit()
        $OutCopy.GetAwaiter().GetResult()
        $ErrCopy.GetAwaiter().GetResult()
        $Code = $Process.ExitCode
        [ordered]@{ binary = $Binary; arguments = $Info.Arguments; pid = $Process.Id; original_handle = $OriginalHandle.ToInt64(); joined = $true; exit = $Code } | ConvertTo-Json | Set-Content -LiteralPath ($Prefix + '.join.json') -Encoding UTF8
    } finally {
        if ($Started -and -not $Process.HasExited) { $Process.WaitForExit() }
        if ($null -ne $Stdout) { $Stdout.Dispose() }
        if ($null -ne $Stderr) { $Stderr.Dispose() }
        $Process.Dispose()
    }
    [IO.File]::WriteAllText($Prefix + '.exit', [string]$Code)
    $Output = [IO.File]::ReadAllText($Prefix + '.stdout.log', [Text.Encoding]::UTF8)
    if ($Code -ne 0) { throw "Qualification failed ($Code); retain fixture: $Manifest" }
    if ($Output -match '(?m)^\s*--- SKIP:') { throw 'Skipped qualification is not proof.' }
    foreach ($Name in $Required) {
        if ($Output -notmatch ('(?m)^\s*--- PASS: ' + [regex]::Escape($Test + '/' + $Name) + ' \(')) { throw "Missing explicit PASS: $Name" }
    }
    if ($Output -notmatch ('(?m)^--- PASS: ' + [regex]::Escape($Test) + ' \(')) { throw 'Missing entrypoint PASS.' }
    return $Output
}

$OldPhase = $env:BATUTA_TASK43_PHASE
$OldManifest = $env:BATUTA_TASK43_MANIFEST
$OldParent = $env:BATUTA_TASK43_PARENT
try {
    $env:BATUTA_TASK43_PHASE = $Phase.ToLowerInvariant()
    $env:BATUTA_TASK43_MANIFEST = $Manifest
    $env:BATUTA_TASK43_PARENT = $PrivateParent
    if ($Phase -eq 'Prepare') {
        if (-not $PrivateParent -or $Manifest -or $ProducerReceipt) { throw 'Prepare requires only an existing ordinary private parent.' }
        $Output = Invoke-Qualification $RuntimeTest 'TestTask43PreexistingPrepare' @()
        $MatchesFound = [regex]::Matches($Output, '(?m)TASK43_MANIFEST=([^\r\n]+)')
        if ($MatchesFound.Count -ne 1) { throw 'Missing unique preparation manifest.' }
        $Manifest = $MatchesFound[0].Groups[1].Value
        Write-Output "TASK43_MANIFEST=$Manifest"
        Write-Output 'PREPARED; join this ordinary child before invoking the fixed producer.'
    } else {
        if (-not $Manifest -or -not $ProducerReceipt) { throw 'Consume/Cleanup requires the manifest and conductor joined producer receipt.' }
        $FixtureRoot = [IO.Path]::GetDirectoryName([IO.Path]::GetFullPath($Manifest))
        if ([IO.Path]::GetFullPath($ReceiptDirectory).StartsWith($FixtureRoot + '\', [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetFullPath($ReceiptDirectory) -ieq $FixtureRoot) { throw 'Receipts must remain outside the disposable fixture.' }
        # Receipt is evidence of the external join, not authorization to run another process.
        $Joined = Get-Content -LiteralPath $ProducerReceipt -Raw | ConvertFrom-Json
        if ($Joined.exit -ne 0 -or $Joined.joined -ne $true -or $Joined.manifest -cne $Manifest -or $Joined.produced -ne 8 -or $Joined.producer_sha256 -cne (Get-FileHash -LiteralPath (Join-Path $PSScriptRoot 'task43-prepare-private-symlinks.ps1') -Algorithm SHA256).Hash.ToLowerInvariant()) { throw 'Missing successful original-handle producer join/binding.' }
        if ($Phase -eq 'Consume') {
            Invoke-Qualification $RuntimeTest 'TestTask43PreexistingRuntime' @('create_file', 'create_parent', 'auth_file', 'daemon_lock', 'auth_lock', 'read_lock', 'controls/fresh_private_create', 'controls/repeated_auth', 'controls/lock_roundtrip') | Write-Output
            Invoke-Qualification $SetupTest 'TestTask43PreexistingSetup' @('setup_file', 'setup_parent', 'controls/exact_setup_read') | Write-Output
            [ordered]@{ outcome = '8 cases and 4 controls PASS; 0 skips'; manifest = $Manifest; manifest_sha256 = (Get-FileHash -LiteralPath $Manifest -Algorithm SHA256).Hash.ToLowerInvariant(); producer_receipt_sha256 = (Get-FileHash -LiteralPath $ProducerReceipt -Algorithm SHA256).Hash.ToLowerInvariant(); runtime_sha256 = $Hashes[1].sha256; setup_sha256 = $Hashes[2].sha256 } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $ReceiptDirectory 'consumer-pass.json') -Encoding UTF8
        } else {
            $Pass = Get-Content -LiteralPath (Join-Path $ReceiptDirectory 'consumer-pass.json') -Raw | ConvertFrom-Json
            if ($Pass.manifest -cne $Manifest -or $Pass.manifest_sha256 -cne (Get-FileHash -LiteralPath $Manifest -Algorithm SHA256).Hash.ToLowerInvariant() -or $Pass.runtime_sha256 -cne $Hashes[1].sha256 -or $Pass.setup_sha256 -cne $Hashes[2].sha256) { throw 'Cleanup requires this exact successful consumer fixture and binaries.' }
            Invoke-Qualification $RuntimeTest 'TestTask43PreexistingCleanup' @('validate_owned_fixture') | Write-Output
            Write-Output "CLEANED_CURRENT_OWNED_FIXTURE=$Manifest"
        }
    }
} catch {
    Write-Error "Retain failed fixture for conductor reconciliation; never recursively delete unbound paths. Manifest=$Manifest ; $_"
    exit 1
} finally {
    $env:BATUTA_TASK43_PHASE = $OldPhase
    $env:BATUTA_TASK43_MANIFEST = $OldManifest
    $env:BATUTA_TASK43_PARENT = $OldParent
    $Identity.Dispose()
}
