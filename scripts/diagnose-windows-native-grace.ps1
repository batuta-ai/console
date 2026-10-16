# Observation only. Supply an existing SID-owned private temp directory and a warm
# module cache. This script never provisions credentials, privileges, or ACLs.
param(
    [Parameter(Mandatory)][string]$PrivateTemp,
    [Parameter(Mandatory)][string]$ModuleCache,
    [Parameter(Mandatory)][string]$Go,
    [ValidateRange(1, 100)][int]$Iterations = 10
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$PSNativeCommandUseErrorActionPreference = $false
if (-not $IsWindows) { throw 'Native Windows required; cross-compilation is not runtime proof' }
$source = Split-Path $PSScriptRoot
$evidence = Join-Path $source ('.batuta/runs/windows-graceful-observation/native-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $evidence
$result = 0
$sourcePaths = @()
$before = @()
$saved = @{}
$environment = @{
    GOTOOLCHAIN = 'local'; GOENV = 'off'; GOWORK = 'off'; GOFLAGS = '-mod=readonly';
    GOPROXY = 'off'; GOSUMDB = 'off'; GOOS = 'windows'; GOARCH = 'amd64'; CGO_ENABLED = '0';
    GOROOT = (Split-Path (Split-Path $Go)); GOMODCACHE = $ModuleCache;
    GOCACHE = (Join-Path $evidence 'cache'); TEMP = $PrivateTemp; TMP = $PrivateTemp;
    LOCALAPPDATA = $PrivateTemp; BATUTA_WINDOWS_GRACE_OBSERVE = '1'
}
function Save-Json([string]$Name, $Value) {
    ConvertTo-Json -InputObject $Value -Depth 12 | Set-Content -LiteralPath (Join-Path $evidence $Name)
}
function Get-Hashes([string[]]$Paths) {
    @($Paths | Sort-Object -Unique | ForEach-Object {
        Get-FileHash -Algorithm SHA256 -LiteralPath $_ | Select-Object Path, Hash
    })
}
function Invoke-Captured([string]$Name, [string]$Executable, [string[]]$Arguments, [int]$DeadlineMS, [string]$InputFile = '') {
    Save-Json "$Name.command.json" @{ executable = $Executable; arguments = $Arguments; deadlineMS = $DeadlineMS; input = $InputFile }
    $process = [Diagnostics.Process]::new()
    $outFile = $errFile = $inputStream = $outTask = $errTask = $inputTask = $null
    $started = $false
    $exitCode = $null
    $failures = [Collections.Generic.List[string]]::new()
    try {
        $start = [Diagnostics.ProcessStartInfo]::new($Executable)
        $start.UseShellExecute = $false
        $start.WorkingDirectory = $source
        $start.RedirectStandardInput = $true
        $start.RedirectStandardOutput = $true
        $start.RedirectStandardError = $true
        foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
        $process.StartInfo = $start
        $outFile = [IO.File]::Create((Join-Path $evidence "$Name.stdout"))
        $errFile = [IO.File]::Create((Join-Path $evidence "$Name.stderr"))
        $started = $process.Start()
        if (-not $started) { throw "Cannot start $Name" }
        $null = $process.SafeHandle
        $outTask = $process.StandardOutput.BaseStream.CopyToAsync($outFile)
        $errTask = $process.StandardError.BaseStream.CopyToAsync($errFile)
        $watch = [Diagnostics.Stopwatch]::StartNew()
        if ($InputFile) {
            $inputStream = [IO.File]::OpenRead($InputFile)
            $inputTask = $inputStream.CopyToAsync($process.StandardInput.BaseStream)
            if (-not $inputTask.Wait($DeadlineMS)) { throw "Input deadline: $Name" }
            $null = $inputTask.GetAwaiter().GetResult()
        }
        $process.StandardInput.Close()
        $remaining = [Math]::Max(0, $DeadlineMS - [int]$watch.ElapsedMilliseconds)
        if (-not $process.WaitForExit($remaining)) { throw "Watchdog expired: $Name" }
    } catch {
        $failures.Add($_.ToString())
    } finally {
        if ($started) {
            try {
                if (-not $process.HasExited) {
                    # Only this original test/controller process; never enumerate descendants.
                    $process.Kill()
                    if (-not $process.WaitForExit(5000)) { throw "Original process did not join: $Name" }
                }
                $exitCode = $process.ExitCode
                $exitCode | Set-Content -LiteralPath (Join-Path $evidence "$Name.exit")
                if ($exitCode -ne 0 -and $script:result -eq 0) { $script:result = $exitCode }
            } catch { $failures.Add($_.ToString()) }
        }
        foreach ($task in @($inputTask, $outTask, $errTask)) {
            if ($null -ne $task) {
                try {
                    if (-not $task.Wait(5000)) { throw "Stream deadline: $Name" }
                    $null = $task.GetAwaiter().GetResult()
                } catch { $failures.Add($_.ToString()) }
            }
        }
        foreach ($resource in @($inputStream, $outFile, $errFile, $process)) {
            if ($null -ne $resource) {
                try { $resource.Dispose() } catch { $failures.Add($_.ToString()) }
            }
        }
        Save-Json "$Name.controller.json" @{ exit = $exitCode; errors = $failures.ToArray() }
    }
    if ($failures.Count) { throw ($failures -join "`n") }
    return $exitCode
}
function Invoke-Focused([string]$Name, [string]$Filter) {
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $binary).Hash -cne $binaryHash) { throw 'Compiled diagnostic binary changed' }
    $actual = $null
    $converted = $null
    try {
        $actual = Invoke-Captured $Name $binary @("-test.run=$Filter", '-test.count=1', '-test.v=test2json', '-test.timeout=30s') 45000
    } finally {
        # Convert failed and watchdog-interrupted streams without rerunning the test.
        try {
            $converted = Invoke-Captured "$Name-events" $test2json @('-t', '-p', 'github.com/batuta-ai/console/internal/session') 15000 (Join-Path $evidence "$Name.stdout")
        } finally {
            if (Test-Path -LiteralPath (Join-Path $evidence "$Name-events.stdout")) {
                Copy-Item -LiteralPath (Join-Path $evidence "$Name-events.stdout") -Destination (Join-Path $evidence "$Name.json")
            }
            if (Test-Path -LiteralPath (Join-Path $evidence "$Name.stdout")) {
                Select-String -LiteralPath (Join-Path $evidence "$Name.stdout") -Pattern 'GRACE-OBSERVATION' |
                    ForEach-Object { $_.Line } | Set-Content -LiteralPath (Join-Path $evidence "$Name.observations.log")
            }
        }
    }
    if ($actual -ne 0) { return $actual }
    if ($converted -ne 0) { throw "Event conversion failed: $Name" }
    return 0
}
try {
    $git = (Get-Command git -CommandType Application | Select-Object -First 1).Source
    foreach ($path in @($Go, $ModuleCache, $PrivateTemp)) {
        if (-not [IO.Path]::IsPathFullyQualified($path) -or -not (Test-Path -LiteralPath $path)) { throw "Existing absolute input required: $path" }
    }
    $tracked = & $git -C $source ls-files --cached --others --exclude-standard
    if ($LASTEXITCODE -ne 0) { throw 'Source inventory failed' }
    $sourcePaths = @($tracked | ForEach-Object { Join-Path $source $_ })
    $before = @(Get-Hashes $sourcePaths)
    Save-Json 'sources-before.json' $before
    $revision = & $git -C $source rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Source revision failed' }
    $status = & $git -C $source status --porcelain --untracked-files=all
    if ($LASTEXITCODE -ne 0) { throw 'Source status failed' }
    Save-Json 'experiment.json' @{ revision = $revision; status = @($status); iterations = $Iterations;
        purpose = 'Instrumented observation only; no correction or release qualification';
        jsonTiming = 'test2json replays retained stdout; use embedded monotonic offsets for observation timing';
        perturbation = 'Duplicate Job/root handles retain objects, delay last-handle KILL_ON_JOB_CLOSE fallback and perturb timing; exact forced decision may be unavailable';
        environment = $environment; privateTemp = $PrivateTemp; perTestTimeoutSeconds = 30; watchdogSeconds = 45 }
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $acl = Get-Acl -LiteralPath $PrivateTemp
    $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    Save-Json 'temp.json' @{ path = $PrivateTemp; sid = $sid.Value; sddl = $acl.Sddl }
    $item = Get-Item -LiteralPath $PrivateTemp
    if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or
        $PrivateTemp.Length -gt 160 -or -not $acl.AreAccessRulesProtected -or
        $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -cne $sid.Value -or $rules.Count -ne 1 -or
        $rules[0].IdentityReference.Value -cne $sid.Value -or $rules[0].IsInherited -or
        $rules[0].FileSystemRights -ne [Security.AccessControl.FileSystemRights]::FullControl -or
        $rules[0].AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
        $rules[0].InheritanceFlags -ne [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit' -or
        $rules[0].PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None) {
        throw 'Existing short, protected, current-SID-only private temp directory required; ask conductor to supply it'
    }
    foreach ($key in $environment.Keys) {
        $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
        [Environment]::SetEnvironmentVariable($key, $environment[$key], 'Process')
    }
    $null = New-Item -ItemType Directory -Path $environment.GOCACHE
    $version = & $Go version
    $version | Set-Content -LiteralPath (Join-Path $evidence 'go-version.log')
    if ($LASTEXITCODE -ne 0 -or $version -cne 'go version go1.27.2 windows/amd64') { throw 'Pinned native Go 1.27.2 required' }
    $test2json = & $Go tool -n test2json
    if ($LASTEXITCODE -ne 0 -or @($test2json).Count -ne 1 -or -not [IO.File]::Exists($test2json)) { throw 'Native test2json unavailable' }
    $toolPaths = @($Go, $git, (Join-Path $PSHOME 'pwsh.exe'), $test2json)
    $toolPaths += @(Get-ChildItem -LiteralPath (Join-Path $environment.GOROOT 'pkg/tool/windows_amd64') -Filter '*.exe' -File | ForEach-Object { $_.FullName })
    Save-Json 'tools.json' @(Get-Hashes $toolPaths)
    $binary = Join-Path $evidence 'session.test.exe'
    $compile = Invoke-Captured 'compile' $Go @('test', '-c', '-o', $binary, './internal/session') 300000
    if ($compile -ne 0) { throw "Diagnostic package compilation failed: $compile" }
    $binaryHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binary).Hash
    Save-Json 'binary.json' @{ path = $binary; sha256 = $binaryHash }
    $checks = Invoke-Focused 'observer-checks' '^TestTask43GraceObservation'
    if ($checks -ne 0) { throw "Observer checks failed: $checks" }
    for ($iteration = 1; $iteration -le $Iterations; $iteration++) {
        $actual = Invoke-Focused ('iteration-{0:D3}' -f $iteration) '^TestTask43WindowsNativeGracefulDrain$/^cooperate$'
        if ($actual -ne 0) { break }
    }
} catch {
    $_ | Out-String | Add-Content -LiteralPath (Join-Path $evidence 'wrapper-errors.log')
    if ($result -eq 0) { $result = 1 }
} finally {
    try {
        if ($sourcePaths.Count) {
            $after = @(Get-Hashes $sourcePaths)
            Save-Json 'sources-after.json' $after
            if (($before | ConvertTo-Json -Compress) -cne ($after | ConvertTo-Json -Compress)) { throw 'Source bytes changed during observation' }
        }
    } catch {
        $_ | Out-String | Add-Content -LiteralPath (Join-Path $evidence 'wrapper-errors.log')
        if ($result -eq 0) { $result = 1 }
    }
    foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
    $result | Set-Content -LiteralPath (Join-Path $evidence 'result.exit')
    Write-Host "Observation evidence: $evidence (exit $result; not release qualification)"
}
exit $result
