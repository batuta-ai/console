# NEVER MERGE/RELEASE: branch-only recurrence evidence; no qualification result is changed.
function Get-Task43GraphFile([string]$Path) {
    try {
        $item = Get-Item -LiteralPath $Path -ErrorAction Stop
        return [ordered]@{ path = $item.FullName; length = $item.Length;
            sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path -ErrorAction Stop).Hash }
    } catch { return [ordered]@{ path = $Path; error = $_.Exception.Message } }
}

function Save-Task43GraphDiagnostic([hashtable]$State) {
    $State | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $State.evidence "$($State.prefix)-diagnostic.json")
}

function Get-Task43GraphContext([hashtable]$State) {
    $context = [ordered]@{ cwd = (Get-Location).Path; processCwd = [Environment]::CurrentDirectory;
        executable = Get-Task43GraphFile $State.executable;
        overlay = Get-Task43GraphFile $State.overlay; replacement = Get-Task43GraphFile $State.replacement;
        environment = [ordered]@{}; resolutions = @(); errors = @() }
    # Explicit allowlist: no whole environment, proxy credentials, or auth configuration.
    foreach ($name in @('PATH', 'PATHEXT', 'SystemRoot', 'ComSpec', 'TEMP', 'TMP', 'TMPDIR',
        'PROCESSOR_ARCHITECTURE', 'PROCESSOR_ARCHITEW6432', 'GOENV', 'GOTOOLCHAIN', 'GOFLAGS', 'GOWORK',
        'GOROOT', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR', 'GOOS', 'GOARCH', 'GOAMD64',
        'CGO_ENABLED', 'GODEBUG', 'GOEXPERIMENT', 'GOMAXPROCS', 'GOMEMLIMIT', 'GOCACHEPROG')) {
        $context.environment[$name] = [Environment]::GetEnvironmentVariable($name)
    }
    foreach ($name in @('GOPROXY', 'GOSUMDB')) {
        $value = [Environment]::GetEnvironmentVariable($name)
        $context.environment[$name] = if ($null -eq $value -or $value -cin @('', 'off', 'direct', 'sum.golang.org')) { $value }
            else { '<configured; redacted>' }
    }
    try {
        $context.resolutions = @(Get-Command go -All -ErrorAction Stop | ForEach-Object {
            [ordered]@{ name = $_.Name; type = [string]$_.CommandType; source = $_.Source;
                path = $(if ($_.CommandType -eq 'Application') { $_.Path } else { $null }) }
        })
    } catch { $context.errors += $_.Exception.Message }
    return $context
}

function Invoke-Task43GraphProcess([string]$Executable, [string[]]$Arguments, [string]$WorkingDirectory,
    [string]$Destination, [int]$TimeoutMilliseconds) {
    $result = [ordered]@{ executable = $Executable; declaredArgumentList = $Arguments; cwd = $WorkingDirectory;
        timeoutMilliseconds = $TimeoutMilliseconds; started = $false; timedOut = $false; exitCode = $null;
        killAttempted = $false; joined = $false; drained = $false; errors = @() }
    $process = $null
    $output = $null
    $errorOutput = $null
    $cancellation = $null
    $drains = @()
    $watch = [Diagnostics.Stopwatch]::StartNew()
    try {
        $result | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath "$Destination.json"
        if ($TimeoutMilliseconds -lt 1 -or $TimeoutMilliseconds -gt 90000) { throw 'Diagnostic deadline must be within 90 seconds' }
        $launchDeadline = $TimeoutMilliseconds - [Math]::Min(15000, [int]($TimeoutMilliseconds / 5))
        $result.launchDeadlineMilliseconds = $launchDeadline
        $start = [Diagnostics.ProcessStartInfo]::new($Executable)
        $start.WorkingDirectory = $WorkingDirectory
        $start.UseShellExecute = $false
        $start.RedirectStandardOutput = $true
        $start.RedirectStandardError = $true
        foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
        $output = [IO.File]::Create("$Destination.stdout")
        $errorOutput = [IO.File]::Create("$Destination.stderr")
        $cancellation = [Threading.CancellationTokenSource]::new()
        $process = [Diagnostics.Process]::new()
        $process.StartInfo = $start
        if (-not $process.Start()) { throw 'Diagnostic process did not start' }
        $result.started = $true
        $result.pid = $process.Id
        $drains += $process.StandardOutput.BaseStream.CopyToAsync($output, 81920, $cancellation.Token)
        $drains += $process.StandardError.BaseStream.CopyToAsync($errorOutput, 81920, $cancellation.Token)
        $remaining = [Math]::Max(0, $launchDeadline - [int]$watch.ElapsedMilliseconds)
        $result.joined = $process.WaitForExit($remaining)
        if ($result.joined) {
            $remaining = [Math]::Max(0, $launchDeadline - [int]$watch.ElapsedMilliseconds)
            $result.drained = [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]$drains, $remaining)
        }
        $result.timedOut = -not ($result.joined -and $result.drained)
    } catch { $result.errors += $_.Exception.Message }
    finally {
        if ($result.started) {
            # On setup/drain errors, wait out the same deadline before timeout-only owned cleanup.
            try {
                if (-not $result.joined) {
                    $remaining = [Math]::Max(0, $launchDeadline - [int]$watch.ElapsedMilliseconds)
                    $result.joined = $process.WaitForExit($remaining)
                    if (-not $result.joined) { $result.timedOut = $true }
                }
                if ($result.timedOut -and -not $process.HasExited) {
                    $result.killAttempted = $true
                    $process.Kill()
                }
            } catch { $result.errors += "Owned process cleanup: $($_.Exception.Message)" }
            try {
                if ($result.timedOut) {
                    $remaining = [Math]::Min(5000, [Math]::Max(0, $TimeoutMilliseconds - [int]$watch.ElapsedMilliseconds))
                    $result.joined = $process.WaitForExit($remaining)
                }
                if ($result.joined) { $result.exitCode = $process.ExitCode }
                else { $result.errors += 'Owned process join incomplete; no PID/tree/global fallback attempted' }
            } catch { $result.errors += "Owned process join: $($_.Exception.Message)" }
            try {
                if ($drains.Count -eq 2 -and -not $result.drained) {
                    $remaining = [Math]::Min(5000, [Math]::Max(0, $TimeoutMilliseconds - [int]$watch.ElapsedMilliseconds))
                    $result.drained = [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]$drains, $remaining)
                }
            } catch { $result.errors += "Raw drain: $($_.Exception.Message)" }
            if (-not $result.drained) {
                $result.errors += 'Raw drain incomplete; cancelling only owned pipe reads'
                try { $cancellation.Cancel() } catch { $result.errors += "Drain cancellation: $($_.Exception.Message)" }
            }
            foreach ($streamName in @('StandardOutput', 'StandardError')) {
                try { $process.$streamName.Dispose() } catch { $result.errors += "Pipe close: $($_.Exception.Message)" }
            }
            if ($drains.Count -and -not $result.drained) {
                try {
                    $remaining = [Math]::Min(5000, [Math]::Max(0, $TimeoutMilliseconds - [int]$watch.ElapsedMilliseconds))
                    if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]$drains, $remaining)) {
                        $result.errors += 'Owned drain tasks still pending after cancellation/close'
                    }
                } catch { $result.errors += "Drain completion after close: $($_.Exception.Message)" }
            }
        }
        foreach ($drain in $drains) {
            if ($drain.IsCompleted) {
                try { $drain.Dispose() } catch { $result.errors += "Drain task disposal: $($_.Exception.Message)" }
            } else { $result.errors += 'Pending drain task cannot be disposed safely' }
        }
        foreach ($owned in @($output, $errorOutput, $cancellation, $process)) {
            if ($null -ne $owned) {
                try { $owned.Dispose() } catch { $result.errors += "Owned object disposal: $($_.Exception.Message)" }
            }
        }
        $watch.Stop()
        $result.elapsedMilliseconds = $watch.ElapsedMilliseconds
        $result.stdout = Get-Task43GraphFile "$Destination.stdout"
        $result.stderr = Get-Task43GraphFile "$Destination.stderr"
        try {
            if ($null -ne $result.exitCode) { $result.exitCode | Set-Content -LiteralPath "$Destination.exit" }
            $result | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath "$Destination.json"
        } catch { $result.errors += "Process evidence persistence: $($_.Exception.Message)"; Write-Warning $result.errors[-1] }
    }
    return $result
}

function Start-Task43GraphDiagnostic([string]$Evidence, [string]$Prefix, [string[]]$Flags,
    [string]$Formatter, [string]$Overlay, [string]$Replacement, [hashtable]$Run) {
    $state = @{ evidence = $Evidence; prefix = $Prefix; errors = @(); executable = ''; overlay = $Overlay;
        replacement = $Replacement; arguments = @('list') + $Flags + @('-deps', '-test', '-f', $Formatter, './internal/testfixture');
        scope = 'NEVER MERGE/RELEASE; original native result authoritative; diagnostic comparison cannot qualify';
        argumentProvenance = 'Declared logical arguments, not observed Windows command line or parsed argv';
        executableProvenance = 'Get-Command go selection and boundary file hashes, not an observed native process image';
        perturbation = 'Extra SDK metadata launches, file hashing and diagnostic IO can perturb execution/cache/timing';
        cleanupBudget = '90s total wait budget: 75s launch/drain, up to 15s reserved for timeout-only handle kill/join/drain';
        cacheScope = 'Same inherited environment and cache locations; no clear/reset/snapshot; original launch may change cache contents';
        powershell = $PSVersionTable.PSVersion.ToString(); edition = $PSVersionTable.PSEdition;
        nativeArgumentPassing = [string](Get-Variable PSNativeCommandArgumentPassing -ValueOnly -ErrorAction SilentlyContinue) }
    Save-Task43GraphDiagnostic $state
    try {
        $selected = Get-Command go -ErrorAction Stop
        $state.selectedCommandType = [string]$selected.CommandType
        if ($selected.CommandType -ne 'Application' -or -not [IO.Path]::IsPathFullyQualified($selected.Path)) {
            throw 'Native go resolution is not an absolute application; executable identity is unproven'
        }
        $state.executable = $selected.Path
    } catch { $state.errors += $_.Exception.Message }
    try {
        $formatterPath = Join-Path $Evidence "$Prefix-formatter.txt"
        [IO.File]::WriteAllText($formatterPath, $Formatter, [Text.UTF8Encoding]::new($false))
        $state.formatterUTF8 = Get-Task43GraphFile $formatterPath
        $state.formatterLiteral = $Formatter
    } catch { $state.errors += $_.Exception.Message }
    $state.beforeSDK = Get-Task43GraphContext $state
    Save-Task43GraphDiagnostic $state
    if (-not $Run.metadataAttempted) {
        $Run.metadataAttempted = $true
        if ($state.executable) {
            $state.sdkVersion = Invoke-Task43GraphProcess $state.executable @('version', '-m', $state.executable) $state.beforeSDK.cwd (Join-Path $Evidence 'task43-sdk-version') 10000
            Save-Task43GraphDiagnostic $state
            $envNames = @('GOENV', 'GOTOOLCHAIN', 'GOFLAGS', 'GOWORK', 'GOROOT', 'GOPATH', 'GOMOD',
                'GOMODCACHE', 'GOCACHE', 'GOTMPDIR', 'GOOS', 'GOARCH', 'GOAMD64', 'CGO_ENABLED', 'GOEXPERIMENT', 'GOVERSION', 'GOTOOLDIR')
            $state.sdkEnvironment = Invoke-Task43GraphProcess $state.executable (@('env', '-json') + $envNames) $state.beforeSDK.cwd (Join-Path $Evidence 'task43-sdk-env') 10000
        } else { $state.errors += 'SDK metadata skipped: executable identity unavailable' }
    }
    $state.before = Get-Task43GraphContext $state
    $state.contextChangedDuringSDK = ($state.beforeSDK | ConvertTo-Json -Depth 8 -Compress) -cne ($state.before | ConvertTo-Json -Depth 8 -Compress)
    $state.identityChangedBeforeNative = $state.contextChangedDuringSDK -or $state.before.resolutions.Count -eq 0 -or
        $state.before.resolutions[0].type -cne 'Application' -or $state.before.resolutions[0].path -cne $state.executable
    Save-Task43GraphDiagnostic $state
    return $state
}

function Complete-Task43GraphDiagnostic([hashtable]$State, [hashtable]$Run, [int]$NativeExit) {
    $State.nativeExit = $NativeExit
    $State.after = Get-Task43GraphContext $State
    $State.nativeStdout = Get-Task43GraphFile (Join-Path $State.evidence "$($State.prefix)-graph.stdout")
    $State.nativeStderr = Get-Task43GraphFile (Join-Path $State.evidence "$($State.prefix)-graph.stderr")
    $State.contextChanged = ($State.before | ConvertTo-Json -Depth 8 -Compress) -cne ($State.after | ConvertTo-Json -Depth 8 -Compress)
    $State.comparisonDisposition = 'native-success'
    if ($NativeExit -ne 0) {
        $State.comparisonDisposition = 'pending'
        if ($Run.comparisonAttempted) { $State.comparisonDisposition = 'already-attempted' }
        elseif ($State.contextChanged -or $State.identityChangedBeforeNative) { $State.comparisonDisposition = 'context-changed' }
        elseif ($State.errors.Count -or $State.before.errors.Count -or $State.before.executable.Contains('error') -or
            $State.before.overlay.Contains('error') -or $State.before.replacement.Contains('error')) {
            $State.comparisonDisposition = 'incomplete-provenance'
        }
    }
    Save-Task43GraphDiagnostic $State
    if ($State.comparisonDisposition -ceq 'pending') {
        $Run.comparisonAttempted = $true
        $State.comparisonDisposition = 'attempted-once'
        Save-Task43GraphDiagnostic $State
        try {
            $State.comparison = Invoke-Task43GraphProcess $State.executable $State.arguments $State.before.cwd (Join-Path $State.evidence "$($State.prefix)-alternative") 90000
        } catch { $State.errors += "Alternative launch: $($_.Exception.Message)" }
        $State.afterComparison = Get-Task43GraphContext $State
        $State.contextChangedDuringComparison = ($State.after | ConvertTo-Json -Depth 8 -Compress) -cne ($State.afterComparison | ConvertTo-Json -Depth 8 -Compress)
        Save-Task43GraphDiagnostic $State
    }
}
