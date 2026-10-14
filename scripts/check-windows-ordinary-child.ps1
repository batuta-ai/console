param(
    [Parameter(Mandatory)][string]$ExpectedSid,
    [Parameter(Mandatory)][string]$BinaryHash
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$PSNativeCommandUseErrorActionPreference = $false

function Publish-Evidence([string]$Name, [byte[]]$Bytes) {
    @{ name = $Name; base64 = [Convert]::ToBase64String($Bytes) } | ConvertTo-Json -Compress | Write-Output
}
function Publish-Text([string]$Name, [string]$Value) {
    Publish-Evidence $Name ([Text.Encoding]::UTF8.GetBytes($Value))
}

$fixture = $null
try {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $sid = $identity.User
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if ($sid.Value -cne $ExpectedSid -or $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Created ordinary account identity required'
    }
    $whoami = Join-Path $env:SystemRoot 'System32\whoami.exe'
    $groups = & $whoami /groups /fo csv /nh
    if ($LASTEXITCODE -ne 0 -or -not $groups) { throw 'Missing native group metadata' }
    $privileges = & $whoami /priv /fo csv /nh
    if ($LASTEXITCODE -ne 0 -or -not $privileges) { throw 'Missing native privilege metadata' }
    Publish-Text 'groups.log' ($groups -join "`n")
    Publish-Text 'privileges.log' ($privileges -join "`n")
    if (($groups -join "`n") -match 'S-1-5-32-544' -or ($privileges -join "`n") -match 'SeCreateSymbolicLinkPrivilege') {
        throw 'Administrator group or special symlink privilege present'
    }
    $profilePath = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
    $registered = (Get-ItemProperty -LiteralPath "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\$ExpectedSid").ProfileImagePath
    if (-not $profilePath -or $profilePath -ine [Environment]::ExpandEnvironmentVariables($registered)) { throw 'Loaded created-account profile required' }
    $profileAcl = Get-Acl -LiteralPath $profilePath
    $profileOwner = $profileAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    Publish-Text 'profile.json' (@{ path = $profilePath; registered = $registered; accountSid = $ExpectedSid;
        ownerSid = $profileOwner; acl = $profileAcl.Sddl } | ConvertTo-Json)
    # The profile service can own a registered profile; only the new fixture is claimed private.
    if ($profileOwner -cnotin @($ExpectedSid, 'S-1-5-18', 'S-1-5-32-544')) { throw 'Registered profile has a foreign owner' }
    $fixture = Join-Path $profilePath ('co-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $fixture | Out-Null
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true, $false)
    $rule = [Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', 'ContainerInherit, ObjectInherit', 'None', 'Allow')
    $acl.AddAccessRule($rule)
    Set-Acl -LiteralPath $fixture -AclObject $acl
    $actual = Get-Acl -LiteralPath $fixture
    $rules = @($actual.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    if ($actual.GetOwner([Security.Principal.SecurityIdentifier]).Value -cne $ExpectedSid -or -not $actual.AreAccessRulesProtected -or
        $rules.Count -ne 1 -or $rules[0].IdentityReference.Value -cne $ExpectedSid -or $rules[0].IsInherited -or
        $rules[0].FileSystemRights -ne $rule.FileSystemRights -or $rules[0].InheritanceFlags -ne $rule.InheritanceFlags -or
        $rules[0].PropagationFlags -ne $rule.PropagationFlags -or $rules[0].AccessControlType -ne $rule.AccessControlType) { throw 'Private fixture ACL differs' }
    $env:USERPROFILE = $profilePath
    $env:HOME = $profilePath
    $env:TEMP = $fixture
    $env:TMP = $fixture
    $env:LOCALAPPDATA = $fixture
    # Read the current process token only; no token duplication or privilege adjustment.
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class OrdinaryToken {
    [DllImport("advapi32.dll", SetLastError=true)]
    static extern bool GetTokenInformation(IntPtr token, int kind, IntPtr data, int size, out int needed);
    public static int ReadInt(IntPtr token, int kind) {
        IntPtr data = Marshal.AllocHGlobal(4);
        try {
            int needed;
            if (!GetTokenInformation(token, kind, data, 4, out needed) || needed != 4)
                throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            return Marshal.ReadInt32(data);
        } finally { Marshal.FreeHGlobal(data); }
    }
}
'@
    $tokenType = [OrdinaryToken]::ReadInt($identity.Token, 8)
    $elevationType = [OrdinaryToken]::ReadInt($identity.Token, 18)
    $elevated = [OrdinaryToken]::ReadInt($identity.Token, 20)
    if ($tokenType -ne 1 -or $elevated -ne 0 -or $elevationType -ne 1) { throw 'Genuine unelevated primary token required' }
    Set-Location -LiteralPath $fixture
    $binary = Join-Path $PSScriptRoot 'service.test.exe'
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binary).Hash
    if ($hash -cne $BinaryHash) { throw 'Staged binary hash differs' }
    Publish-Text 'actor.json' (@{ sid = $sid.Value; pid = $PID; tokenType = $tokenType; elevationType = $elevationType;
        elevated = $elevated; profile = $profilePath; fixture = $fixture; acl = $actual.Sddl; binarySHA256 = $hash;
        temp = $env:TEMP; tmp = $env:TMP; localAppData = $env:LOCALAPPDATA } | ConvertTo-Json)
    & $binary '-test.list=^TestOrdinaryWindowsUserDiscoversSelectedSkills$' 1> inventory.stdout 2> inventory.stderr
    $inventoryExit = $LASTEXITCODE
    Publish-Text 'inventory.exit' ([string]$inventoryExit)
    foreach ($name in @('inventory.stdout', 'inventory.stderr')) { Publish-Evidence $name ([IO.File]::ReadAllBytes((Join-Path $fixture $name))) }
    if ($inventoryExit -ne 0 -or (Get-Content inventory.stdout -Raw).Trim() -cne 'TestOrdinaryWindowsUserDiscoversSelectedSkills') { throw 'Exact named test inventory required' }
    & $binary '-test.run=^TestOrdinaryWindowsUserDiscoversSelectedSkills$' '-test.v=test2json' '-test.count=1' '-test.timeout=5m' 1> test.stdout 2> test.stderr
    $testExit = $LASTEXITCODE
    Publish-Text 'test.exit' ([string]$testExit)
    foreach ($name in @('test.stdout', 'test.stderr')) { Publish-Evidence $name ([IO.File]::ReadAllBytes((Join-Path $fixture $name))) }
    $after = & $whoami /priv /fo csv /nh
    if ($LASTEXITCODE -ne 0 -or ($after -join "`n") -cne ($privileges -join "`n")) { throw 'Actor process privileges changed' }
    Publish-Text 'privileges-after.log' ($after -join "`n")
    if ($testExit -ne 0) { throw 'Selected native test failed' }
    Publish-Text 'actor-complete.log' 'Native test process returned; streams drained'
} catch {
    [Console]::Error.WriteLine($_.ToString())
    exit 1
}
