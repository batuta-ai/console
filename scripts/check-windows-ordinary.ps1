param([Parameter(Mandatory)][string]$Evidence, [Parameter(Mandatory)][string]$Bundle,
    [Parameter(Mandatory)][string]$LabelsHash, [Parameter(Mandatory)][string]$ProducerRevision)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$PSNativeCommandUseErrorActionPreference = $false
# This bootstrap is exclusively for GitHub's disposable hosted Windows VM.
@{ isWindows = $IsWindows; githubActions = $env:GITHUB_ACTIONS; runnerEnvironment = $env:RUNNER_ENVIRONMENT;
   runnerOS = $env:RUNNER_OS; runnerArchitecture = $env:RUNNER_ARCH; imageOS = $env:ImageOS;
   imageVersion = $env:ImageVersion; runID = $env:GITHUB_RUN_ID; workspace = $env:GITHUB_WORKSPACE } |
    ConvertTo-Json | Set-Content (Join-Path $Evidence 'bootstrap-host.json')
if (-not $IsWindows -or $env:GITHUB_ACTIONS -cne 'true' -or $env:RUNNER_ENVIRONMENT -cne 'github-hosted' -or
    $env:RUNNER_OS -cne 'Windows' -or $env:RUNNER_ARCH -cne 'X64' -or $env:ImageOS -cnotin @('win25', 'win25-vs2026') -or
    -not $env:GITHUB_RUN_ID -or -not $env:GITHUB_WORKSPACE) { throw 'GitHub-hosted Windows 2025 only; refusing account bootstrap' }
$parentIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not ([Security.Principal.WindowsPrincipal]::new($parentIdentity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Hosted bootstrap administrator required' }
$source = (Resolve-Path $PSScriptRoot/..).Path
if ($source -ine (Resolve-Path $env:GITHUB_WORKSPACE).Path) { throw 'Exact checked-out workspace required' }
$account = $null
$process = $null
$drained = $false
$launchAttempted = $false
$secret = $null
$stage = $null
$files = @()
$hashesBefore = $null
$proofPassed = $false
$executionError = $null
$finalErrors = [Collections.Generic.List[Management.Automation.ErrorRecord]]::new()
$cleanup = @{ created = $false; drained = $false; removed = $false }
function Record-Source([string]$Name) {
    $status = & git -C $source status --porcelain --untracked-files=all
    if ($LASTEXITCODE -ne 0) { throw 'Source status failed' }
    [IO.File]::WriteAllText((Join-Path $Evidence $Name), ($status -join [Environment]::NewLine))
    if ($status) { throw 'Source checkout is dirty' }
}
function Convert-TestOutput([string]$Prefix, [string]$Package) {
    $stdout = Join-Path $Evidence "$Prefix.stdout"
    if (-not (Test-Path -LiteralPath $stdout)) { return }
    $start = [Diagnostics.ProcessStartInfo]::new($test2json)
    $start.ArgumentList.Add('-t')
    $start.ArgumentList.Add('-p')
    $start.ArgumentList.Add($Package)
    $start.UseShellExecute = $false
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $converter = [Diagnostics.Process]::Start($start)
    try {
        $outTask = $converter.StandardOutput.ReadToEndAsync()
        $errTask = $converter.StandardError.ReadToEndAsync()
        $converter.StandardInput.Write([IO.File]::ReadAllText($stdout))
        $converter.StandardInput.Close()
        $converter.WaitForExit()
        [IO.File]::WriteAllText((Join-Path $Evidence "$Prefix.json"), $outTask.Result)
        $conversion = "$Prefix-test2json"
        if ($Prefix -ceq 'test') { $conversion = 'test2json' }
        [IO.File]::WriteAllText((Join-Path $Evidence "$conversion.stderr"), $errTask.Result)
        $converter.ExitCode | Set-Content (Join-Path $Evidence "$conversion.exit")
        if ($converter.ExitCode -ne 0 -or $errTask.Result) { throw "Test event conversion failed: $Prefix" }
    } finally { $converter.Dispose() }
}
function Assert-TempResult([string]$Variant, [string]$ExpectedSid) {
    $prefix = "temp-$Variant"
    $expected = @('TestTerminalEnvironmentPreservesOrdinaryUserTEMP', 'TestTerminalEnvironmentPreservesOrdinaryUserTEMP/select_environment')
    $action = 'pass'
    $exitCode = 0
    $failed = 'false'
    if ($Variant -ceq 'baseline') { $action = 'fail'; $exitCode = 1; $failed = 'true' }
    if ([int](Get-Content (Join-Path $Evidence "$prefix.exit")) -ne $exitCode -or
        (Get-Content (Join-Path $Evidence "$prefix.timeout") -Raw).Trim() -cne 'False' -or
        [IO.File]::ReadAllText((Join-Path $Evidence "$prefix.stderr"))) { throw "Unexpected native exit, timeout or stderr: $prefix" }
    $events = @(Get-Content (Join-Path $Evidence "$prefix.json") | Where-Object { $_ } | ForEach-Object { $_ | ConvertFrom-Json })
    $runs = @($events | Where-Object { $_.Action -ceq 'run' -and $_.PSObject.Properties['Test'] } | ForEach-Object { $_.Test } | Sort-Object)
    $results = @($events | Where-Object { $_.Action -ceq $action -and $_.PSObject.Properties['Test'] } | ForEach-Object { $_.Test } | Sort-Object)
    $terminal = @($events | Where-Object { $_.Action -in @('pass', 'fail', 'skip') -and -not $_.PSObject.Properties['Test'] })
    if (($runs -join "`n") -cne ($expected -join "`n") -or ($results -join "`n") -cne ($expected -join "`n") -or
        @($events | Where-Object { $_.Action -in @('pass', 'fail', 'skip') -and $_.Action -cne $action }).Count -ne 0 -or
        $terminal.Count -ne 1 -or $terminal[0].Action -cne $action) { throw "Exact TEMP semantic event set required: $prefix" }
    $output = [IO.File]::ReadAllText((Join-Path $Evidence "$prefix.stdout"))
    $cause = @($events | Where-Object { $_.Action -ceq 'output' -and $_.PSObject.Properties['Test'] -and
        $_.Test -ceq $expected[1] -and $_.Output -cmatch '^\s+ambient_temp_windows_test\.go:124: owned protected harness temporary root private Windows DACL must prevent inherited access\r?\n$' })
    $wantCause = 0
    if ($Variant -ceq 'baseline') { $wantCause = 1 }
    if ($cause.Count -ne $wantCause -or
        [regex]::Matches($output, 'ordinary TEMP rejected by private-directory policy: private Windows DACL must prevent inherited access').Count -ne 1) { throw "TEMP selection cause differs: $prefix" }
    # A second cleanup error can share the already-failed parent's FAIL event.
    $testNames = ($expected | ForEach-Object { [regex]::Escape($_) }) -join '|'
    foreach ($event in $events) {
        if ($event.Action -cne 'output') { continue }
        if ($event.Output -cmatch ('^\s*(=== (RUN|NAME)\s+(' + $testNames + ')|--- (PASS|FAIL): (' + $testNames + ') \([0-9.]+s\)|PASS|FAIL)\r?\n$')) { continue }
        if ($event.Output -cmatch '^\s+ambient_temp_windows_test\.go:(88|92): ordinary TEMP ACE \d+: SID=\S+ flags=0x[0-9a-f]+ mask=0x[0-9a-f]+\r?\n$' -or
            $event.Output -cmatch '^\s+ambient_temp_windows_test\.go:89: before selection: identity=[^\r\n]+\r?\n$' -or
            $event.Output -cmatch '^\s+ambient_temp_windows_test\.go:93: after selection \(failed=(true|false)\): identity=[^\r\n]+\r?\n$' -or
            $event.Output -cmatch '^\s+ambient_temp_windows_test\.go:106: ordinary TEMP rejected by private-directory policy: private Windows DACL must prevent inherited access\r?\n$' -or
            ($Variant -ceq 'baseline' -and $event.Output -cmatch '^\s+ambient_temp_windows_test\.go:124: owned protected harness temporary root private Windows DACL must prevent inherited access\r?\n$')) { continue }
        throw "Unrelated TEMP diagnostic: $prefix"
    }
    $before = [regex]::Matches($output, 'before selection: (identity=[^\r\n]+)')
    $after = [regex]::Matches($output, ('after selection \(failed=' + $failed + '\): (identity=[^\r\n]+)'))
    if ($before.Count -ne 1 -or $after.Count -ne 1 -or $before[0].Groups[1].Value -cne $after[0].Groups[1].Value -or
        $before[0].Groups[1].Value -cnotmatch (' owner=' + [regex]::Escape($ExpectedSid) + ' control=')) { throw "TEMP identity/owner/DACL cleanup proof differs: $prefix" }
}
try {
    Record-Source 'source-before.log'
    $revision = & git -C $source rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Source revision missing' }
    $revision | Set-Content (Join-Path $Evidence 'revision.log')
    $version = & go version
    if ($LASTEXITCODE -ne 0 -or $version -cne 'go version go1.27.2 windows/amd64') { throw 'Pinned Go toolchain required' }
    $version | Set-Content (Join-Path $Evidence 'go-version.log')
    $helper = Join-Path $source 'internal/testfixture/command_windows.go'
    $frozen = Join-Path $source 'internal/testfixture/ambient_temp_windows_test.go'
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $helper).Hash -ine '50231d30408b7c65192522f274162d6825ad9c60c54813e8f4dc9b46f5be1116' -or
        (Get-FileHash -Algorithm SHA256 -LiteralPath $frozen).Hash -ine 'd7db825ef12dd35e2770002fcbbff6bc005661585c18dd8f8dc0c60f1790187f') { throw 'Frozen helper/regression source differs' }
    $tracked = & git -C $source ls-files
    if ($LASTEXITCODE -ne 0) { throw 'Source inventory failed' }
    $sourceFiles = @($tracked | ForEach-Object { Join-Path $source $_ })
    $sourceHashes = @($sourceFiles | ForEach-Object { Get-FileHash -Algorithm SHA256 -LiteralPath $_ } | Select-Object Path, Hash)
    $sourceHashes | ConvertTo-Json | Set-Content (Join-Path $Evidence 'source-manifest.json')
    $helperPath = [IO.Path]::GetFullPath($helper.Replace('/', '\'))
    $helperSources = @($sourceHashes | Where-Object {
        [StringComparer]::OrdinalIgnoreCase.Equals([IO.Path]::GetFullPath($_.Path.Replace('/', '\')), $helperPath)
    })
    if ($helperSources.Count -ne 1) { throw 'Exactly one tracked helper source required' }
    $stage = Join-Path $env:SystemDrive ('co-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $stage | Out-Null
    $binary = Join-Path $stage 'service.test.exe'
    & go vet -tags console_ordinary_user ./internal/service 1> (Join-Path $Evidence 'vet.stdout') 2> (Join-Path $Evidence 'vet.stderr')
    $vetExit = $LASTEXITCODE
    $vetExit | Set-Content (Join-Path $Evidence 'vet.exit')
    if ($vetExit -ne 0) { throw 'Service tagged full vet failed' }
    & go test -tags console_ordinary_user -c -o $binary ./internal/service 1> (Join-Path $Evidence 'compile.stdout') 2> (Join-Path $Evidence 'compile.stderr')
    $compileExit = $LASTEXITCODE
    $compileExit | Set-Content (Join-Path $Evidence 'compile.exit')
    if ($compileExit -ne 0) { throw 'Service test compilation failed' }
    # Both TEMP binaries use this checkout; only the baseline helper is overlaid.
    $env:GOENV = 'off'
    $env:GOTOOLCHAIN = 'local'
    $env:GOFLAGS = '-mod=readonly'
    $env:GOWORK = 'off'
    if ($env:GOSUMDB -cne 'sum.golang.org' -or $env:GOPRIVATE -or $env:GONOSUMDB) { throw 'Public checksum database required for frozen module preparation' }
    & go mod download 1> (Join-Path $Evidence 'mod-download.stdout') 2> (Join-Path $Evidence 'mod-download.stderr')
    $downloadExit = $LASTEXITCODE
    $downloadExit | Set-Content (Join-Path $Evidence 'mod-download.exit')
    if ($downloadExit -ne 0) { throw 'Frozen module graph download failed' }
    & go mod verify 1> (Join-Path $Evidence 'mod-verify.stdout') 2> (Join-Path $Evidence 'mod-verify.stderr')
    $verifyExit = $LASTEXITCODE
    $verifyExit | Set-Content (Join-Path $Evidence 'mod-verify.exit')
    if ($verifyExit -ne 0) { throw 'Frozen module graph verification failed' }
    $env:GOPROXY = 'off'
    $env:GOSUMDB = 'off'
    $baselineRef = 'e506fe1b358eeabc9db3142a0caee2859c115735:internal/testfixture/command_windows.go'
    $baselineHelper = Join-Path $Evidence 'baseline-command_windows.go'
    $extract = [Diagnostics.ProcessStartInfo]::new((Get-Command git -CommandType Application | Select-Object -First 1).Source)
    foreach ($arg in @('-C', $source, 'cat-file', 'blob', $baselineRef)) { $extract.ArgumentList.Add($arg) }
    $extract.UseShellExecute = $false
    $extract.RedirectStandardOutput = $true
    $extract.RedirectStandardError = $true
    $reader = [Diagnostics.Process]::Start($extract)
    $destination = [IO.File]::Create($baselineHelper)
    try {
        $errorTask = $reader.StandardError.ReadToEndAsync()
        $reader.StandardOutput.BaseStream.CopyTo($destination)
        $reader.WaitForExit()
        $reader.ExitCode | Set-Content (Join-Path $Evidence 'baseline-blob.exit')
        $errorTask.Result | Set-Content (Join-Path $Evidence 'baseline-blob.stderr')
        if ($reader.ExitCode -ne 0) { throw 'Exact baseline blob unavailable locally' }
    } finally { $destination.Dispose(); $reader.Dispose() }
    $baselineHelperHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $baselineHelper).Hash
    if ($baselineHelperHash -ine '33d48802bff54c7e4d8525781ccc69ca4d7222348def2f5342f0db62ec7c1110') { throw 'Original baseline helper bytes differ' }
    $overlay = Join-Path $Evidence 'baseline-overlay.json'
    @{ Replace = @{ $helper = $baselineHelper } } | ConvertTo-Json | Set-Content $overlay
    $pairHashes = @{}
    foreach ($variant in @('baseline', 'candidate')) {
        $prefix = "temp-$variant"
        $pairBinary = Join-Path $stage "$prefix.test.exe"
        $flags = @('-tags', 'console_ordinary_user')
        if ($variant -ceq 'baseline') { $flags += "-overlay=$overlay" }
        $graph = '{{.ImportPath}}|{{.Dir}}|{{join .GoFiles ","}}|{{join .CgoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}|{{join .TestImports ","}}|{{join .XTestImports ","}}|{{join .EmbedFiles ","}}|{{.Module}}'
        & go list @flags -deps -test -f $graph ./internal/testfixture 1> (Join-Path $Evidence "$prefix-graph.stdout") 2> (Join-Path $Evidence "$prefix-graph.stderr")
        $graphExit = $LASTEXITCODE
        $graphExit | Set-Content (Join-Path $Evidence "$prefix-graph.exit")
        if ($graphExit -ne 0) { throw "TEMP dependency/test graph failed: $variant" }
        & go vet @flags ./internal/testfixture 1> (Join-Path $Evidence "$prefix-vet.stdout") 2> (Join-Path $Evidence "$prefix-vet.stderr")
        $pairVetExit = $LASTEXITCODE
        $pairVetExit | Set-Content (Join-Path $Evidence "$prefix-vet.exit")
        if ($pairVetExit -ne 0) { throw "TEMP full vet failed: $variant" }
        & go test @flags -c -o $pairBinary ./internal/testfixture 1> (Join-Path $Evidence "$prefix-compile.stdout") 2> (Join-Path $Evidence "$prefix-compile.stderr")
        $pairCompileExit = $LASTEXITCODE
        $pairCompileExit | Set-Content (Join-Path $Evidence "$prefix-compile.exit")
        if ($pairCompileExit -ne 0) { throw "TEMP compilation failed: $variant" }
        $pairHashes[$variant] = (Get-FileHash -Algorithm SHA256 -LiteralPath $pairBinary).Hash
        Copy-Item -LiteralPath $pairBinary -Destination $Evidence
        $replacementCount = 0
        $expectedReplacements = 0
        if ($variant -ceq 'baseline') { $expectedReplacements = 1 }
        $effectiveSources = @(foreach ($entry in $sourceHashes) {
            $hash = $entry.Hash
            if ($variant -ceq 'baseline' -and
                [StringComparer]::OrdinalIgnoreCase.Equals([IO.Path]::GetFullPath($entry.Path.Replace('/', '\')), $helperPath)) {
                if ($hash -ine '50231d30408b7c65192522f274162d6825ad9c60c54813e8f4dc9b46f5be1116' -or $hash -ieq $baselineHelperHash) { throw 'Helper substitution source hash differs' }
                $hash = $baselineHelperHash
                $replacementCount++
            }
            [pscustomobject]@{ Path = $entry.Path; Hash = $hash }
        })
        if ($replacementCount -ne $expectedReplacements) { throw "Helper substitution count differs: $variant" }
        @{ revision = $revision; variant = $variant; go = $version; flags = $flags; package = './internal/testfixture';
            baselineBlob = $baselineRef; baselineHelperSHA256 = $baselineHelperHash; binarySHA256 = $pairHashes[$variant];
            helperSourceMatchCount = $helperSources.Count; sourceReplacementCount = $replacementCount;
            source = $effectiveSources; goenv = $env:GOENV; gotoolchain = $env:GOTOOLCHAIN; goflags = $env:GOFLAGS;
            gowork = $env:GOWORK; goproxy = $env:GOPROXY; gosumdb = $env:GOSUMDB } |
            ConvertTo-Json -Depth 5 | Set-Content (Join-Path $Evidence "$prefix-manifest.json")
    }
    # The original helper's runtime import can change traversal order, not test sources.
    if (((Get-Content (Join-Path $Evidence 'temp-baseline-graph.stdout') | Sort-Object) -join "`n") -cne
        ((Get-Content (Join-Path $Evidence 'temp-candidate-graph.stdout') | Sort-Object) -join "`n")) { throw 'TEMP test/dependency graphs differ' }
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'check-windows-ordinary-child.ps1') -Destination $stage
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'check-windows-msys-owner.ps1') -Destination $stage
    $binaryHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binary).Hash
    $tool = (Get-Command go -CommandType Application | Select-Object -First 1).Source
    $shell = Join-Path $PSHOME 'pwsh.exe'
    $test2json = (& go tool -n test2json)
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $test2json -PathType Leaf)) { throw 'test2json tool missing' }
    $gitTool = (Get-Command git -CommandType Application | Select-Object -First 1).Source
    $goroot = & $tool env GOROOT
    if ($LASTEXITCODE -ne 0) { throw 'Pinned compiler root unavailable' }
    $labelsPath = Join-Path $Bundle 'labels.json'
    if ($LabelsHash -cnotmatch '^[a-f0-9]{64}$' -or
        (Get-FileHash -Algorithm SHA256 -LiteralPath $labelsPath).Hash.ToLowerInvariant() -cne $LabelsHash) { throw 'Producer labels digest differs' }
    $labels = Get-Content -LiteralPath $labelsPath -Raw | ConvertFrom-Json
    if ($labels.revision -cne $revision -or $ProducerRevision -cne $revision -or
        $labels.runID -cne $env:GITHUB_RUN_ID -or $labels.runAttempt -cne $env:GITHUB_RUN_ATTEMPT -or
        $labels.version -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._+-]{0,79}$' -or
        $labels.archive -cne "console_$($labels.version)_windows_amd64.zip") { throw 'Same-run exact-source package labels required' }
    $bundleFiles = @($labelsPath, (Join-Path $Bundle $labels.archive), (Join-Path $Bundle 'checksums.txt'),
        (Join-Path $PSScriptRoot 'qualify-release.ps1'), (Join-Path $PSScriptRoot 'qualify-release.test.ps1'))
    $bundleDigests = @{}
    foreach ($file in $bundleFiles) {
        $digest = (Get-FileHash -Algorithm SHA256 -LiteralPath $file).Hash.ToLowerInvariant()
        Copy-Item -LiteralPath $file -Destination $stage
        $copy = Join-Path $stage ([IO.Path]::GetFileName($file))
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $copy).Hash.ToLowerInvariant() -cne $digest) { throw 'Package staging changed bytes' }
        $bundleDigests[[IO.Path]::GetFileName($file)] = $digest
    }
    if ($bundleDigests[$labels.archive] -cne $labels.archiveSHA256 -or $bundleDigests['checksums.txt'] -cne $labels.checksumsSHA256 -or
        $bundleDigests['qualify-release.ps1'] -cne $labels.qualifierSHA256 -or $bundleDigests['qualify-release.test.ps1'] -cne $labels.testsSHA256) { throw 'Producer/source input binding differs' }
    $bundleInputs = Join-Path $stage 'bundle-inputs.json'
    @{ revision=$revision; labelsSHA256=$LabelsHash; files=$bundleDigests } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $bundleInputs
    $bundleInputsHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $bundleInputs).Hash
    Copy-Item -LiteralPath $bundleInputs -Destination (Join-Path $Evidence 'bundle-sourcebridge.json')
    $runtimeInputs = Join-Path $stage 'msys-inputs.json'
    @{ source = $source; sources = $sourceHashes; go = $tool; git = $gitTool; goroot = $goroot;
        controllerSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $stage 'check-windows-msys-owner.ps1')).Hash } |
        ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $runtimeInputs
    $runtimeInputsHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $runtimeInputs).Hash
    $files = @($binary, $tool, $shell, $test2json, $gitTool, $runtimeInputs,
        (Join-Path $goroot 'pkg/tool/windows_amd64/compile.exe'), (Join-Path $goroot 'pkg/tool/windows_amd64/link.exe'),
        (Join-Path $stage 'check-windows-msys-owner.ps1'), (Join-Path $stage 'check-windows-ordinary-child.ps1'),
        (Join-Path $stage 'temp-baseline.test.exe'), (Join-Path $stage 'temp-candidate.test.exe'), $baselineHelper, $overlay)
    $files += $bundleFiles + @($bundleInputs) + @($bundleDigests.Keys | ForEach-Object { Join-Path $stage $_ })
    $hashesBefore = @($files | ForEach-Object { Get-FileHash -Algorithm SHA256 -LiteralPath $_ } | Select-Object Path, Hash) + $sourceHashes
    $files += $sourceFiles
    $hashesBefore | ConvertTo-Json | Set-Content (Join-Path $Evidence 'hashes-before.json')
    $name = 'co' + [guid]::NewGuid().ToString('N').Substring(0, 16)
    if (Get-LocalUser -Name $name -ErrorAction SilentlyContinue) { throw 'Account name collision' }
    $secret = [Security.SecureString]::new()
    foreach ($character in ('Aa1!' + [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(48))).ToCharArray()) { $secret.AppendChar($character) }
    $secret.MakeReadOnly()
    # New-LocalUser accepts SecureString; Start-Process accepts PSCredential directly.
    $account = New-LocalUser -Name $name -Password $secret -Description 'Disposable Console ordinary skills CI'
    $sid = $account.SID
    $cleanup.created = $true
    $cleanup.sid = $sid.Value
    $cleanup.name = $name
    if ((Get-LocalUser -Name $name).SID.Value -cne $sid.Value) { throw 'Created SID mismatch' }
    Add-LocalGroupMember -SID 'S-1-5-32-545' -Member $account
    if (@(Get-LocalGroupMember -SID 'S-1-5-32-544' | Where-Object { $_.SID.Value -ceq $sid.Value }).Count -ne 0) { throw 'Created user is an administrator' }
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetOwner($parentIdentity.User)
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($owner in @($parentIdentity.User, [Security.Principal.SecurityIdentifier]::new('S-1-5-18'))) {
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($owner, 'FullControl', 'ContainerInherit, ObjectInherit', 'None', 'Allow'))
    }
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, 'ReadAndExecute', 'ContainerInherit, ObjectInherit', 'None', 'Allow'))
    Set-Acl -LiteralPath $stage -AclObject $acl
    (Get-Acl -LiteralPath $stage).Sddl | Set-Content (Join-Path $Evidence 'stage-acl.log')
    $credential = [Management.Automation.PSCredential]::new("$env:COMPUTERNAME\$name", $secret)
    $arguments = '-NoLogo -NoProfile -NonInteractive -File "' + (Join-Path $stage 'check-windows-ordinary-child.ps1') + '" -ExpectedSid ' + $sid.Value + ' -BinaryHash ' + $binaryHash
    $arguments += ' -BaselineHash ' + $pairHashes.baseline + ' -CandidateHash ' + $pairHashes.candidate + ' -RuntimeInputsHash ' + $runtimeInputsHash + ' -BundleInputsHash ' + $bundleInputsHash
    # -Wait waits for descendants as well as the credentialed primary process.
    $launchAttempted = $true
    try {
        $process = Start-Process -FilePath $shell -ArgumentList $arguments -Credential $credential -LoadUserProfile -UseNewEnvironment -WorkingDirectory $stage -PassThru -Wait -RedirectStandardOutput (Join-Path $Evidence 'child.stdout') -RedirectStandardError (Join-Path $Evidence 'child.stderr')
    } catch {
        $_.ToString() | Set-Content (Join-Path $Evidence 'launch-error.log')
        throw
    }
    $process.WaitForExit()
    $drained = $true
    $cleanup.pid = $process.Id
    $process.ExitCode | Set-Content (Join-Path $Evidence 'child.exit')
    $allowed = @('groups.log', 'privileges.log', 'profile.json', 'actor.json', 'inventory.exit', 'inventory.stdout', 'inventory.stderr', 'test.exit', 'test.stdout', 'test.stderr', 'privileges-after.log', 'actor-complete.log')
    $allowed += 'temp-binaries.json', 'temp-binaries-after.json', 'user.log'
    foreach ($variant in @('baseline', 'candidate')) {
        foreach ($suffix in @('inventory.exit', 'inventory.stdout', 'inventory.stderr', 'inventory.timeout', 'exit', 'stdout', 'stderr', 'timeout')) {
            $allowed += "temp-$variant.$suffix"
        }
    }
    $allowed += 'msys-inputs.json', 'msys-caller.json', 'msys-hashes-before.json', 'msys-hashes-after.json', 'msys-observations.json', 'msys-result.json'
    foreach ($prefix in @('msys-negative-a', 'msys-negative-b', 'msys-positive-keeper', 'msys-positive-a', 'msys-positive-b',
        'msys-negative-literal', 'msys-negative-literal-convert', 'msys-positive-literal', 'msys-positive-literal-convert',
        'msys-ordinary-fixtures', 'msys-ordinary-fixtures-convert')) {
        foreach ($suffix in @('stdout', 'stderr', 'exit')) { $allowed += "$prefix.$suffix" }
    }
    $allowed += 'msys-negative-literal.json', 'msys-positive-literal.json', 'msys-ordinary-fixtures.json'
    $bundleAllowed = @('bundle-summary.json', 'bundle-copy-before.json', 'bundle-copy-after.json')
    foreach ($case in @('counterfactual','checksum','missing','duplicate','extra','renamed','traversal','relative','ads','dotdot','pe','version','revision','go','core','positive')) {
        foreach ($suffix in @('actor','mutation','result')) { $bundleAllowed += "bundle-$case-$suffix.json" }
        if ($case -notin @('relative','ads','dotdot')) {
            foreach ($suffix in @('receipt','observations')) { $bundleAllowed += "bundle-$case-$suffix.json" }
        }
    }
    $allowed += $bundleAllowed
    $seen = @{}
    foreach ($line in Get-Content -LiteralPath (Join-Path $Evidence 'child.stdout')) {
        $record = $line | ConvertFrom-Json
        if ((@($record.PSObject.Properties.Name | Sort-Object) -join ',') -cne 'base64,name' -or $record.name -cnotin $allowed -or $seen.ContainsKey($record.name)) { throw 'Unexpected or duplicate evidence' }
        $seen[$record.name] = $true
        [IO.File]::WriteAllBytes((Join-Path $Evidence $record.name), [Convert]::FromBase64String($record.base64))
    }
    Convert-TestOutput 'test' 'github.com/batuta-ai/console/internal/service'
    foreach ($variant in @('baseline', 'candidate')) { Convert-TestOutput "temp-$variant" 'github.com/batuta-ai/console/internal/testfixture' }
    foreach ($required in $allowed) { if (-not $seen.ContainsKey($required)) { throw "Missing evidence: $required" } }
    if ($process.ExitCode -ne 0 -or [int](Get-Content (Join-Path $Evidence 'test.exit')) -ne 0 -or [int](Get-Content (Join-Path $Evidence 'inventory.exit')) -ne 0) { throw 'Ordinary process failed' }
    $actor = Get-Content (Join-Path $Evidence 'actor.json') -Raw | ConvertFrom-Json
    if ($actor.sid -cne $sid.Value -or $actor.pid -ne $process.Id -or $actor.tokenType -ne 1 -or $actor.elevated -ne 0 -or $actor.elevationType -ne 1 -or $actor.binarySHA256 -cne $binaryHash) { throw 'Actor metadata differs' }
    $events = @(Get-Content (Join-Path $Evidence 'test.json') | Where-Object { $_ } | ForEach-Object { $_ | ConvertFrom-Json })
    $passes = @($events | Where-Object { $_.Action -eq 'pass' -and $_.PSObject.Properties['Test'] } | ForEach-Object { $_.Test } | Sort-Object)
    $expected = @('TestOrdinaryWindowsUserDiscoversSelectedSkills', 'TestOrdinaryWindowsUserDiscoversSelectedSkills/claude', 'TestOrdinaryWindowsUserDiscoversSelectedSkills/codex')
    if (($passes -join "`n") -cne ($expected -join "`n") -or @($events | Where-Object { $_.Action -in @('fail', 'skip') }).Count -ne 0) { throw 'Exact parent and two provider passes required, without failures/skips' }
    $runs = @($events | Where-Object { $_.Action -eq 'run' -and $_.PSObject.Properties['Test'] } | ForEach-Object { $_.Test } | Sort-Object)
    $packagePasses = @($events | Where-Object { $_.Action -eq 'pass' -and -not $_.PSObject.Properties['Test'] })
    if (($runs -join "`n") -cne ($expected -join "`n") -or $packagePasses.Count -ne 1) { throw 'Exact test starts and terminal package result required' }
    if ((Get-Content (Join-Path $Evidence 'inventory.stdout') -Raw).Trim() -cne $expected[0]) { throw 'Named inventory differs' }
    $output = [IO.File]::ReadAllText((Join-Path $Evidence 'test.stdout'))
    if ([regex]::Matches($output, 'actual ordinary actor from current process elevation=1; source read only').Count -ne 2 -or
        [regex]::Matches($output, 'actual caller nonadmin=true privileges=\d+, no special symlink privilege').Count -ne 2) { throw 'Missing native current-process authority assertions' }
    $inventory = $null
    foreach ($variant in @('baseline', 'candidate')) {
        $prefix = "temp-$variant"
        if ([int](Get-Content (Join-Path $Evidence "$prefix.inventory.exit")) -ne 0 -or
            (Get-Content (Join-Path $Evidence "$prefix.inventory.timeout") -Raw).Trim() -cne 'False' -or
            [IO.File]::ReadAllText((Join-Path $Evidence "$prefix.inventory.stderr"))) { throw "TEMP inventory failed: $variant" }
        $currentInventory = [IO.File]::ReadAllText((Join-Path $Evidence "$prefix.inventory.stdout"))
        if (@($currentInventory -split "\r?\n" | Where-Object { $_ -ceq 'TestTerminalEnvironmentPreservesOrdinaryUserTEMP' }).Count -ne 1 -or
            ($null -ne $inventory -and $currentInventory -cne $inventory)) { throw 'Exact paired TEMP inventory differs' }
        $inventory = $currentInventory
        Assert-TempResult $variant $sid.Value
    }
    foreach ($name in @('temp-binaries.json', 'temp-binaries-after.json')) {
        $pairActor = Get-Content (Join-Path $Evidence $name) -Raw | ConvertFrom-Json
        if ($pairActor.sid -cne $sid.Value -or $pairActor.pid -ne $process.Id -or
            $pairActor.baseline -cne $pairHashes.baseline -or $pairActor.candidate -cne $pairHashes.candidate) { throw 'TEMP actor/binary hash binding differs' }
    }
    $ownerResult = Get-Content (Join-Path $Evidence 'msys-result.json') -Raw | ConvertFrom-Json
    $ownerActor = Get-Content (Join-Path $Evidence 'msys-caller.json') -Raw | ConvertFrom-Json
    if (-not $ownerResult.success -or $ownerResult.primary -or @($ownerResult.cleanup).Count -ne 0 -or
        $ownerActor.sid -cne $sid.Value -or $ownerActor.pid -ne $process.Id -or $ownerActor.tokenType -ne 1 -or
        $ownerActor.elevationType -ne 1 -or $ownerActor.elevated -ne 0 -or $ownerActor.stable -cne $actor.fixture -or
        $ownerActor.stableAcl -cne $actor.acl) { throw 'Ordinary MSYS run owner authority/result differs' }
    if ((Get-Content (Join-Path $Evidence 'msys-hashes-before.json') -Raw) -cne
        (Get-Content (Join-Path $Evidence 'msys-hashes-after.json') -Raw)) { throw 'Ordinary MSYS source/tools/binary changed' }
    $bundleSummary = Get-Content (Join-Path $Evidence 'bundle-summary.json') -Raw | ConvertFrom-Json
    if (-not $bundleSummary.complete -or @($bundleSummary.cases).Count -ne 16 -or
        @($bundleSummary.cases | Where-Object { -not $_.passed }).Count -ne 0 -or
        $bundleSummary.archiveSHA256 -cne $labels.archiveSHA256 -or $bundleSummary.qualifierSHA256 -cne $labels.qualifierSHA256 -or
        (Get-Content (Join-Path $Evidence 'bundle-copy-before.json') -Raw) -cne (Get-Content (Join-Path $Evidence 'bundle-copy-after.json') -Raw)) { throw 'Actual ZIP case suite incomplete or inputs changed' }
    $proofPassed = $true
} catch { $executionError = $_ } finally {
    try {
        Record-Source 'source-after.log'
    } catch { $finalErrors.Add($_) }
    try {
        if ($null -ne $hashesBefore) {
            $files | ForEach-Object { Get-FileHash -Algorithm SHA256 -LiteralPath $_ } | Select-Object Path, Hash | ConvertTo-Json | Set-Content (Join-Path $Evidence 'hashes-after.json')
            if ((Get-Content (Join-Path $Evidence 'hashes-before.json') -Raw) -cne (Get-Content (Join-Path $Evidence 'hashes-after.json') -Raw)) { throw 'Source/tool/binary hashes changed' }
        }
    } catch { $finalErrors.Add($_) }
    $cleanup.drained = $drained
    $cleanup.stage = $stage
    $cleanup.launchAttempted = $launchAttempted
    try {
        if ($account) {
            if (-not $drained -and $launchAttempted) { throw 'Owned process not drained; refusing account removal' }
            $current = Get-LocalUser -Name $account.Name
            if ($current.SID.Value -cne $account.SID.Value) { throw 'Cleanup SID mismatch; refusing account removal' }
            Remove-LocalUser -SID $account.SID
            if (Get-LocalUser -SID $account.SID -ErrorAction SilentlyContinue) { throw 'Owned account removal failed' }
            $cleanup.removed = $true
        }
    } catch { $finalErrors.Add($_) }
    try {
        $cleanup | ConvertTo-Json | Set-Content (Join-Path $Evidence 'cleanup.json')
    } catch { $finalErrors.Add($_) }
    try {
        if ($secret) { $secret.Dispose() }
    } catch { $finalErrors.Add($_) }
}
if ($executionError -or $finalErrors.Count -ne 0) {
    $diagnostics = @(
        if ($executionError) { "Execution: $executionError`n$($executionError.ScriptStackTrace)" }
        foreach ($failure in $finalErrors) { "Finalization: $failure`n$($failure.ScriptStackTrace)" }
    )
    foreach ($diagnostic in $diagnostics) { [Console]::Error.WriteLine($diagnostic) }
    try {
        $diagnostics | Set-Content (Join-Path $Evidence 'failure-errors.log')
    } catch {
        [Console]::Error.WriteLine("Failure diagnostics could not be saved: $_")
    }
    if ($executionError) { throw $executionError }
    throw $finalErrors[0]
}
if ($proofPassed) { 'Exact three skills PASS; TEMP baseline exact causal FAIL and candidate exact PASS; MSYS causal negative then owned positive and original operation fixtures PASS; genuine created-account primary actor' | Set-Content (Join-Path $Evidence 'summary.log') }
