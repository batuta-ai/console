param([Parameter(Mandatory)][string]$Evidence)
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
$cleanup = @{ created = $false; drained = $false; removed = $false }
function Record-Source([string]$Name) {
    $status = & git -C $source status --porcelain --untracked-files=all
    if ($LASTEXITCODE -ne 0) { throw 'Source status failed' }
    [IO.File]::WriteAllText((Join-Path $Evidence $Name), ($status -join [Environment]::NewLine))
    if ($status) { throw 'Source checkout is dirty' }
}
try {
    Record-Source 'source-before.log'
    $revision = & git -C $source rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Source revision missing' }
    $revision | Set-Content (Join-Path $Evidence 'revision.log')
    $version = & go version
    if ($LASTEXITCODE -ne 0 -or $version -cne 'go version go1.27.2 windows/amd64') { throw 'Pinned Go toolchain required' }
    $version | Set-Content (Join-Path $Evidence 'go-version.log')
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
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'check-windows-ordinary-child.ps1') -Destination $stage
    $binaryHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binary).Hash
    $tool = (Get-Command go -CommandType Application).Source
    $shell = Join-Path $PSHOME 'pwsh.exe'
    $test2json = (& go tool -n test2json)
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $test2json -PathType Leaf)) { throw 'test2json tool missing' }
    $files = @($binary, $tool, $shell, $test2json, (Join-Path $stage 'check-windows-ordinary-child.ps1'))
    $tracked = & git -C $source ls-files
    if ($LASTEXITCODE -ne 0) { throw 'Source inventory failed' }
    $files += @($tracked | ForEach-Object { Join-Path $source $_ })
    $files | ForEach-Object { Get-FileHash -Algorithm SHA256 -LiteralPath $_ } | Select-Object Path, Hash | ConvertTo-Json | Set-Content (Join-Path $Evidence 'hashes-before.json')
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
    $seen = @{}
    foreach ($line in Get-Content -LiteralPath (Join-Path $Evidence 'child.stdout')) {
        $record = $line | ConvertFrom-Json
        if ($record.name -cnotin $allowed -or $seen.ContainsKey($record.name)) { throw 'Unexpected or duplicate evidence' }
        $seen[$record.name] = $true
        [IO.File]::WriteAllBytes((Join-Path $Evidence $record.name), [Convert]::FromBase64String($record.base64))
    }
    foreach ($required in $allowed) { if (-not $seen.ContainsKey($required)) { throw "Missing evidence: $required" } }
    if ($process.ExitCode -ne 0 -or [int](Get-Content (Join-Path $Evidence 'test.exit')) -ne 0 -or [int](Get-Content (Join-Path $Evidence 'inventory.exit')) -ne 0) { throw 'Ordinary process failed' }
    $actor = Get-Content (Join-Path $Evidence 'actor.json') -Raw | ConvertFrom-Json
    if ($actor.sid -cne $sid.Value -or $actor.pid -ne $process.Id -or $actor.tokenType -ne 1 -or $actor.elevated -ne 0 -or $actor.binarySHA256 -cne $binaryHash) { throw 'Actor metadata differs' }
    $convert = [Diagnostics.ProcessStartInfo]::new($test2json, '-t -p github.com/batuta-ai/console/internal/service')
    $convert.UseShellExecute = $false
    $convert.RedirectStandardInput = $true
    $convert.RedirectStandardOutput = $true
    $convert.RedirectStandardError = $true
    $converter = [Diagnostics.Process]::Start($convert)
    $outTask = $converter.StandardOutput.ReadToEndAsync()
    $errTask = $converter.StandardError.ReadToEndAsync()
    $converter.StandardInput.Write([IO.File]::ReadAllText((Join-Path $Evidence 'test.stdout')))
    $converter.StandardInput.Close()
    $converter.WaitForExit()
    $outTask.Result | Set-Content (Join-Path $Evidence 'test.json')
    $errTask.Result | Set-Content (Join-Path $Evidence 'test2json.stderr')
    $converter.ExitCode | Set-Content (Join-Path $Evidence 'test2json.exit')
    if ($converter.ExitCode -ne 0) { throw 'Test event conversion failed' }
    $events = @($outTask.Result -split "`n" | Where-Object { $_ } | ForEach-Object { $_ | ConvertFrom-Json })
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
    Record-Source 'source-after.log'
    $files | ForEach-Object { Get-FileHash -Algorithm SHA256 -LiteralPath $_ } | Select-Object Path, Hash | ConvertTo-Json | Set-Content (Join-Path $Evidence 'hashes-after.json')
    if ((Get-Content (Join-Path $Evidence 'hashes-before.json') -Raw) -cne (Get-Content (Join-Path $Evidence 'hashes-after.json') -Raw)) { throw 'Source/tool/binary hashes changed' }
    'Exact three native pass events; genuine created-account primary actor' | Set-Content (Join-Path $Evidence 'summary.log')
} finally {
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
    } finally {
        $cleanup | ConvertTo-Json | Set-Content (Join-Path $Evidence 'cleanup.json')
        if ($secret) { $secret.Dispose() }
    }
}
