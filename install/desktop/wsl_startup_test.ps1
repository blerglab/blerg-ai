# No Windows APIs are called: tasks, identity, sleep, and WSL are fakes.
param([Parameter(Mandatory)][string]$Scratch)
$ErrorActionPreference = 'Stop'
[IO.Directory]::CreateDirectory($Scratch) | Out-Null
$env:LOCALAPPDATA = $Scratch
$env:SystemRoot = Join-Path $Scratch 'Windows'
# Source and destination paths must both remain literal during worker setup.
$source = Join-Path $Scratch 'source[worker]*?`.ps1'
[IO.File]::Copy("$PSScriptRoot/daemon/wsl-startup.ps1", $source, $true)
. $source -Action status -Distro 'Example Distro' -LinuxUser example

function Assert($Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
$script:canaries = @('fake-wsl-daemon-secret-7e29', 'fake-wsl-oauth-secret-83cd', 'fake-url-user-49ef', 'fake-url-password-25ad')
$env:BLERG_RUNNER_DAEMON_TOKEN = $script:canaries[0]
$env:CLAUDE_CODE_OAUTH_TOKEN = $script:canaries[1]
$env:BLERG_RUNNER_SERVER_URL = "wss://$($script:canaries[2]):$($script:canaries[3])@runner.example.com/ws/daemon"
$script:diagnostics = @()
function Test-Startup {
    $output = @(Invoke-BlergStartup @args)
    $script:diagnostics += $output
    $output
}
function Assert-NoSecrets {
    $surfaces = @($script:diagnostics) + @($script:tasks.Values | ConvertTo-Json -Depth 10)
    $surfaces += @(Get-ChildItem -LiteralPath $Scratch -Recurse -File | ForEach-Object { Get-Content -Raw -LiteralPath $_.FullName })
    foreach ($secret in $script:canaries) {
        Assert (-not (($surfaces -join "`n").Contains($secret))) 'Secret in task action, worker, logs, or diagnostics'
    }
}
# Exercise actual entry-point parameter binding. If validation is removed, a
# later Windows-identity error must not masquerade as a validation rejection.
foreach ($case in @(
    @{Action='status'; Distro="Example'; Write-Output injected"; LinuxUser='example'},
    @{Action='status'; Distro='Example Distro'; LinuxUser='bad user'},
    @{Action='status'; Distro='Example Distro'; LinuxUser='bad;user'},
    @{Action='invalid'; Distro='Example Distro'; LinuxUser='example'}
)) {
    $rejected = $false
    try { & "$PSScriptRoot/daemon/wsl-startup.ps1" @case | Out-Null } catch {
        Assert ($_.FullyQualifiedErrorId -like 'ParameterArgumentValidationError*') 'Entry-point allowlist must reject invalid input'
        $rejected = $true
    }
    Assert $rejected 'Entry-point accepted invalid input'
}
$script:tasks = @{}
$script:starts = 0
$script:stops = 0
$script:failRegistration = $false
$script:account = 'EXAMPLE\Example User'
$script:expectedDistro = 'Example Distro'
function Get-BlergAccount { $script:account }
function New-ScheduledTaskAction {
    [CmdletBinding()] param($Execute, $Argument)
    Assert ($Execute -like '*WindowsPowerShell*v1.0*powershell.exe') 'Use Windows PowerShell'
    Assert ($Argument -like '*-WindowStyle Hidden*') 'Task must be hidden'
    Assert ($Argument -like "*-File `"*startup.ps1`" -Action run -Distro `"$script:expectedDistro`"*") 'Task quoting/worker path'
    [pscustomobject]@{ Execute = $Execute; Arguments = $Argument }
}
function New-ScheduledTaskTrigger {
    [CmdletBinding()] param([switch]$AtLogOn, $User)
    Assert ($AtLogOn -and $User -eq $script:account) 'Logon trigger must be scoped to installing account'
    'trigger'
}
function New-ScheduledTaskPrincipal {
    [CmdletBinding()] param($UserId, $LogonType, $RunLevel)
    Assert ($UserId -eq $script:account -and $LogonType -eq 'Interactive' -and $RunLevel -eq 'Limited') 'No elevation or password'
    'principal'
}
function New-ScheduledTaskSettingsSet {
    [CmdletBinding()] param($MultipleInstances, [TimeSpan]$ExecutionTimeLimit, $RestartCount,
    [TimeSpan]$RestartInterval, [switch]$AllowStartIfOnBatteries, [switch]$DontStopIfGoingOnBatteries, [switch]$StartWhenAvailable)
    Assert ($MultipleInstances -eq 'IgnoreNew') 'Repeated setup must not duplicate workers'
    Assert ($ExecutionTimeLimit -eq [TimeSpan]::Zero) 'Keep-alive must not expire after default 72 hours'
    Assert ($RestartCount -eq 999 -and $RestartInterval.TotalMinutes -eq 1) 'Retry crashed PowerShell task'
    Assert ($AllowStartIfOnBatteries -and $DontStopIfGoingOnBatteries -and $StartWhenAvailable) 'Laptop battery/startup recovery'
    'settings'
}
function Register-ScheduledTask {
    [CmdletBinding()] param($TaskName, $TaskPath, $Action, $Trigger, $Principal, $Settings, $Description, [switch]$Force)
    if ($script:failRegistration) { throw 'fake policy denied' }
    Assert $Force 'Setup must update stable task identity'
    Assert ($TaskPath -eq '\') 'Register in explicit root folder'
    $script:tasks[$TaskName] = [pscustomobject]@{ State = 'Running'; Action = $Action; TaskName = $TaskName; TaskPath = $TaskPath }
}
function Start-ScheduledTask {
    [CmdletBinding()] param($TaskName, $TaskPath)
    Assert ($TaskPath -eq '\' -and $script:tasks.ContainsKey($TaskName)) 'Start registered task'; $script:starts++
}
function Get-ScheduledTask {
    [CmdletBinding()] param($TaskName)
    if ($TaskName) { $script:tasks[$TaskName] } else { $script:tasks.Values }
}
function Get-ScheduledTaskInfo {
    [CmdletBinding()] param($TaskName, $TaskPath)
    Assert ($TaskPath -eq '\' -and $script:tasks.ContainsKey($TaskName)) 'Read only owned task'
    [pscustomobject]@{ LastTaskResult = 267009 }
}
function Stop-ScheduledTask {
    [CmdletBinding()] param($TaskName, $TaskPath)
    Assert ($TaskPath -eq '\' -and $script:tasks.ContainsKey($TaskName)) 'Stop only owned task'; $script:stops++
}
function Unregister-ScheduledTask {
    [CmdletBinding(SupportsShouldProcess)] param($TaskName, $TaskPath)
    Assert ($TaskPath -eq '\' -and $script:tasks.ContainsKey($TaskName)) 'Remove only owned task'
    $script:tasks.Remove($TaskName)
}

Test-Startup enable 'Example Distro' example | Out-Null
$first = @($script:tasks.Keys)[0]
$firstTask = $script:tasks[$first]
Test-Startup enable 'Example Distro' example | Out-Null
Assert ($script:tasks.Count -eq 1 -and $script:starts -eq 2 -and $script:stops -eq 0) 'Setup idempotence'
$workers = @(Get-ChildItem -LiteralPath $Scratch -Recurse -Filter startup.ps1)
Assert ($workers.Count -eq 1) 'Exactly one Windows worker'
Assert ((Get-Content -Raw -LiteralPath $workers[0].FullName) -eq (Get-Content -Raw -LiteralPath "$PSScriptRoot/daemon/wsl-startup.ps1")) 'Worker copy is self-contained'
Assert ($firstTask.Action.Arguments.Contains(' -LinuxUser "example"')) 'Explicit Linux user'
Assert ((Test-Startup status 'Example Distro' example) -match 'last result: 267009') 'Task diagnostics'
Test-Startup enable 'Example Distro' another | Out-Null
Assert ($script:tasks.Count -eq 2) 'Different Linux accounts need distinct tasks'
$script:expectedDistro = 'Another Distro'
Test-Startup enable 'Another Distro' example | Out-Null
Assert ($script:tasks.Count -eq 3) 'Different distros need distinct tasks'
$script:expectedDistro = 'Example Distro'
$script:account = 'EXAMPLE\Another User'
Test-Startup enable 'Example Distro' example | Out-Null
Assert ($script:tasks.Count -eq 4) 'Different Windows accounts need distinct tasks'
$script:account = 'EXAMPLE\Example User'
# A same-named task in another folder must never be stopped or removed.
$script:tasks['other-folder'] = [pscustomobject]@{TaskName=$first; TaskPath='\Other\'; State='Running'}
Assert-NoSecrets
Test-Startup disable 'Example Distro' example | Out-Null
Test-Startup disable 'Example Distro' example | Out-Null
Assert ($script:tasks.Count -eq 4 -and $script:stops -eq 1) 'Removal must be idempotent and scoped'
Assert ((Test-Startup status 'Example Distro' example) -match 'not enabled') 'Missing-task diagnostics'
Assert (Test-Path -LiteralPath $workers[0].FullName) 'Retain diagnostics/worker after removal'
$script:failRegistration = $true
try {
    Test-Startup enable 'Example Distro' example | Out-Null
    throw 'Registration failure was swallowed'
} catch { Assert ($_.Exception.Message -eq 'fake policy denied') 'Registration failures must propagate' }
$script:failRegistration = $false

# Run the real retry loop, but never invoke WSL or wait for wall-clock time.
$script:attempts = 0
$script:delays = @()
function Invoke-BlergWSL {
    $script:attempts++
    if ($script:attempts -eq 1) { throw 'fake WSL unavailable' }
    if ($script:attempts -eq 2) { return 42 }
    return 0 # shutdown/relaunch can exit successfully; still restart keep-alive
}
function Start-Sleep($Seconds) {
    $script:delays += $Seconds
    if ($script:delays.Count -eq 3) { throw 'test loop complete' }
}
try { Test-Startup run 'Example Distro' example } catch {
    Assert ($_.Exception.Message -eq 'test loop complete') 'Unexpected retry-loop failure'
}
Assert ($script:attempts -eq 3 -and ($script:delays -join ',') -eq '15,15,15') 'Retry both WSL startup errors and exits, with backoff'
$logs = @(Get-ChildItem -LiteralPath $Scratch -Recurse -Filter startup.log)
Assert ($logs.Count -eq 1) 'One bounded startup log'
$contents = Get-Content -Raw -LiteralPath $logs[0].FullName
Assert ($contents.Contains('fake WSL unavailable') -and $contents.Contains('code 42') -and $contents.Contains('code 0')) 'Useful startup/exit diagnostics'
Assert ($contents.Contains('systemd journal') -and $contents.Contains('retrying in 15 seconds')) 'Actionable recovery diagnostics'
# The mutex was released even after the exception: running again must enter.
$script:delays = @()
try { Test-Startup run 'Example Distro' example } catch {}
Assert ($script:attempts -eq 6) 'Release single-instance lock on worker failure'
Assert-NoSecrets

# Rotate diagnostics before they can grow without bound during repeated failures.
$log = $logs[0].FullName
Set-Content -LiteralPath $log -Value ('x' * (1MB + 1))
Write-BlergLog 'after rotation'
Assert ((Test-Path -LiteralPath "$log.old") -and (Get-Item -LiteralPath $log).Length -lt 1KB) 'Bound startup log storage'

# Actual external command boundary and foreground retry loop. Only Windows APIs
# and the retry timer are mocked; the shell executable must finish before retry.
# Linux also permits * and ? in filenames, extending the Windows-legal bracket
# and backtick cases to every PowerShell wildcard/escape character.
if (-not $IsWindows) {
    [IO.Directory]::CreateDirectory("$env:SystemRoot/System32") | Out-Null
    $fakeWSL = "$env:SystemRoot/System32/wsl.exe"
    $env:FAKE_WSL_COMPLETED = Join-Path $Scratch 'completed'
    Set-Content -LiteralPath $fakeWSL -Value '#!/bin/sh', 'printf "%s\n" "$@"',
        'printf "%s\n" "fake native stderr" >&2', 'sleep 1',
        'printf completed > "$FAKE_WSL_COMPLETED"', 'exit 42'
    & chmod +x $fakeWSL
    # Reload only the real wrapper, leaving identity and scheduling mocked.
    $ast = [Management.Automation.Language.Parser]::ParseFile("$PSScriptRoot/daemon/wsl-startup.ps1", [ref]$null, [ref]$null)
    $definition = $ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Invoke-BlergWSL'}, $true)
    . ([scriptblock]::Create($definition.Extent.Text))
    function Start-Sleep($Seconds) {
        Assert ($Seconds -eq 15) 'Native process retry backoff'
        throw 'native loop complete'
    }
    # A bracket path can silently redirect wildcard-aware writes to its sibling.
    # Create a matching sibling at the exact same task/log location and verify
    # both its current and rotated logs survive output and rotation.
    $siblingLog = $logs[0].FullName.Replace('profile[brackets]', 'profileb').Replace('profile[wild]*?`', 'profilewXY')
    if ($siblingLog -ne $logs[0].FullName) {
        [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($siblingLog)) | Out-Null
        [IO.File]::WriteAllText($siblingLog, 'sibling log untouched')
        [IO.File]::WriteAllText("$siblingLog.old", 'sibling old log untouched')
    }
    try { Test-Startup run 'Example Distro' example | Out-Null } catch {
        Assert ($_.Exception.Message -eq 'native loop complete') 'Unexpected native retry-loop failure'
    }
    Assert (Test-Path -LiteralPath $env:FAKE_WSL_COMPLETED) 'Foreground native executable must complete before retry'
    $contents = Get-Content -Raw -LiteralPath $logs[0].FullName
    $stdout = @(($contents -split "`n") | Where-Object { $_ -and $_ -ne 'fake native stderr' -and $_ -notmatch '^\d{4}-' })
    Assert (($stdout -join '|') -eq '--distribution|Example Distro|--user|example|--exec|/bin/sleep|infinity') 'Preserve native stdout and explicit foreground argv'
    Assert ($contents.Contains('fake native stderr')) 'Preserve native stderr'
    Assert ($contents.Contains('code 42') -and -not $contents.Contains('WSL launch failed')) 'Preserve native exit-code diagnostics'
    Set-Content -LiteralPath $logs[0].FullName -Value ('x' * (1MB + 1))
    # Exercise rotation with literal paths after the real wrapper too.
    try { Test-Startup run 'Example Distro' example | Out-Null } catch {
        Assert ($_.Exception.Message -eq 'native loop complete') 'Unexpected native rotation failure'
    }
    Assert ((Get-Item -LiteralPath "$($logs[0].FullName).old").Length -gt 1MB) 'Rotate the exact special-character log'
    if ($siblingLog -ne $logs[0].FullName) {
        Assert ([IO.File]::ReadAllText($siblingLog) -eq 'sibling log untouched') 'Do not write a wildcard-matching sibling log'
        Assert ([IO.File]::ReadAllText("$siblingLog.old") -eq 'sibling old log untouched') 'Do not rotate a wildcard-matching sibling log'
    }
    Remove-Item -LiteralPath Env:FAKE_WSL_COMPLETED
}
Assert-NoSecrets
Write-Output 'wsl_startup_test.ps1: ok'
