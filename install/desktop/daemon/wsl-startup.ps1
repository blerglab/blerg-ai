# Called by install.sh, or by this account's Windows logon task. No credentials.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('enable', 'disable', 'status', 'run')][string]$Action,
    [Parameter(Mandatory)][ValidatePattern('^[a-zA-Z0-9][a-zA-Z0-9._ -]*$')][string]$Distro,
    [Parameter(Mandatory)][ValidatePattern('^[a-zA-Z_][a-zA-Z0-9_.-]*\$?$')][string]$LinuxUser
)
$ErrorActionPreference = 'Stop'

# Kept behind functions so tests never access Windows identity, tasks, or WSL.
function Get-BlergAccount { [Security.Principal.WindowsIdentity]::GetCurrent().Name }
function Get-BlergTask([string]$Name) {
    # An empty, successful enumeration confirms absence. A named lookup's
    # not-found error must not be confused with permission/provider failures.
    $tasks = @(Get-ScheduledTask -ErrorAction Stop)
    $tasks | Where-Object { $_.TaskPath -eq '\' -and $_.TaskName -eq $Name }
}
function Invoke-BlergWSL {
    & "$env:SystemRoot\System32\wsl.exe" --distribution $Distro --user $LinuxUser --exec /bin/sleep infinity 2>&1 |
        Out-File -LiteralPath $log -Append -Encoding utf8
    return $LASTEXITCODE
}
function Write-BlergLog([string]$Message) {
    # Bound disk use even if WSL cannot start for days. Never log the environment.
    if ((Test-Path -LiteralPath $log) -and (Get-Item -LiteralPath $log).Length -gt 1MB) {
        Move-Item -LiteralPath $log -Destination "$log.old" -Force
    }
    Add-Content -LiteralPath $log -Value "$(Get-Date -Format o) $Message"
}

$script:BlergStartupSource = $PSCommandPath
function Invoke-BlergStartup([string]$Action, [string]$Distro, [string]$LinuxUser) {
    $account = Get-BlergAccount
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes("$account`n$Distro`n$LinuxUser")
        $key = ([BitConverter]::ToString($sha.ComputeHash($bytes))).Replace('-', '').Substring(0, 24)
    } finally { $sha.Dispose() }
    $name = "Blerg-WSL-$key"
    $dir = Join-Path $env:LOCALAPPDATA "Blerg\wsl\$key"
    $worker = Join-Path $dir 'startup.ps1'
    $log = Join-Path $dir 'startup.log'

    switch ($Action) {
        enable {
            # New-Item has no LiteralPath; .NET keeps profile characters literal.
            [IO.Directory]::CreateDirectory($dir) | Out-Null
            # Store the worker on Windows so it is available BEFORE the distro boots.
            # Reinstall from the same checkout must not copy a file onto itself.
            if ($script:BlergStartupSource -ne $worker) { Copy-Item -LiteralPath $script:BlergStartupSource -Destination $worker -Force }
            $arguments = '-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "{0}" -Action run -Distro "{1}" -LinuxUser "{2}"' -f $worker, $Distro, $LinuxUser
            $taskAction = New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -Argument $arguments
            $trigger = New-ScheduledTaskTrigger -AtLogOn -User $account
            $principal = New-ScheduledTaskPrincipal -UserId $account -LogonType Interactive -RunLevel Limited
            $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) `
                -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
                -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
            # Stable identity + Force updates exactly one task. Never stop a running task on setup.
            Register-ScheduledTask -TaskPath '\' -TaskName $name -Action $taskAction -Trigger $trigger -Principal $principal `
                -Settings $settings -Description 'Blerg: start and keep this WSL distro alive after Windows sign-in; retry WSL startup failures.' -Force | Out-Null
            Start-ScheduledTask -TaskPath '\' -TaskName $name
            Write-Output "Windows sign-in startup enabled. Task Scheduler: $name"
            Write-Output "Keeps this WSL distro running while signed in, including on battery. Logs: $log"
        }
        disable {
            $task = Get-BlergTask $name
            if ($task) {
                Stop-ScheduledTask -TaskPath '\' -TaskName $name -ErrorAction Stop
                Unregister-ScheduledTask -TaskPath '\' -TaskName $name -Confirm:$false -ErrorAction Stop
            }
            # Retain worker/logs for diagnostics. Do not terminate the distro or its daemon.
            Write-Output "Windows sign-in startup disabled. Task Scheduler: $name"
            Write-Output 'The distro and daemon were not stopped. Startup logs are retained.'
        }
        status {
            $task = Get-BlergTask $name
            if ($task) {
                $info = Get-ScheduledTaskInfo -TaskPath '\' -TaskName $name
                Write-Output "Task Scheduler: $name; state: $($task.State); last result: $($info.LastTaskResult)"
            } else { Write-Output "Windows sign-in startup is not enabled. Task Scheduler: $name" }
            Write-Output "Startup logs: $log"
        }
        run {
            [IO.Directory]::CreateDirectory($dir) | Out-Null
            # Also guard against a manual launch alongside the task (or multiple logon triggers).
            $mutex = New-Object Threading.Mutex($false, "Local\$name")
            $held = $false
            try {
                try { $held = $mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $held = $true }
                if (-not $held) { return }
                Write-BlergLog 'Starting WSL keep-alive. The enabled systemd user service starts the daemon.'
                while ($true) {
                    try {
                        $code = Invoke-BlergWSL
                        Write-BlergLog "WSL keep-alive exited (code $code); retrying in 15 seconds. Check wsl --list --verbose and the distro's systemd journal."
                    } catch {
                        Write-BlergLog "WSL launch failed: $($_.Exception.Message); retrying in 15 seconds. Check Windows WSL installation and distro registration."
                    }
                    Start-Sleep -Seconds 15
                }
            } finally {
                if ($held) { $mutex.ReleaseMutex() }
                $mutex.Dispose()
            }
        }
    }
}

# Dot sourcing loads functions for tests; normal invocation always runs the action.
if ($MyInvocation.InvocationName -ne '.') { Invoke-BlergStartup @PSBoundParameters }
