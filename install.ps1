# Zoomies installer for Windows.
#
#   & ([scriptblock]::Create((irm https://zoomies.sh/install.ps1))) `
#       -Mode agent -Controller 'https://zoomies.example.com' -JoinToken 'zoojoin_...'
#
# Run it in a PowerShell window opened as administrator. The Hosts page in the
# UI (Hosts, Add a host, Windows) prints the whole command with your values in.
#
# To read it before you run it:
#
#   irm https://zoomies.sh/install.ps1 -OutFile install.ps1
#   notepad install.ps1
#   .\install.ps1 -Mode agent -Controller 'https://zoomies.example.com' -JoinToken 'zoojoin_...'
#
# What it does, in order:
#   1. checks this is Windows PowerShell 5.1 or 7+ on x86-64, in an elevated window
#   2. says what it is about to do and asks once (-Yes skips the question)
#   3. downloads zoomies_windows_amd64.exe and checks its SHA-256 against the
#      release's checksums.txt, and stops if they differ
#   4. installs it to %ProgramFiles%\Zoomies and adds that folder to the machine PATH
#   5. runs `zoomies agent join`, which redeems the token and registers the
#      zoomies-agent service
#   6. checks the service is running and starts automatically
#
# Run again on an enrolled machine, it upgrades the binary and keeps the host's
# credentials. -Force enrols again with a new token. -Uninstall removes it.
#
# Nothing here writes the join token to disk or prints it. It is handed to the
# agent as an argument of a child process, which is how the Linux installer
# does it too.

[CmdletBinding()]
param(
    # Only the agent is supported on Windows. The controller runs on Linux.
    [ValidateSet('agent')]
    [string]$Mode = 'agent',

    # The address the host joins on: https://..., or tailcat://... for a private connection.
    [string]$Controller = '',

    # The single-use token from Hosts, Add a host.
    [string]$JoinToken = '',

    # latest (the default), a release such as v1.2.3, or dev for the rolling build.
    [string]$Version = $(if ($env:ZOOMIES_VERSION) { $env:ZOOMIES_VERSION } else { 'latest' }),

    # Stop and remove the agent service and the installed binary.
    [switch]$Uninstall,

    # Do not ask for confirmation. The command the UI prints leaves this off.
    [switch]$Yes,

    # Enrol again on a machine that already holds credentials.
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$script:Repo = if ($env:ZOOMIES_REPO) { $env:ZOOMIES_REPO } else { 'eyupio/zoomies' }
$script:BaseUrl = if ($env:ZOOMIES_BASE_URL) { $env:ZOOMIES_BASE_URL.TrimEnd('/') } else { "https://github.com/$($script:Repo)/releases" }
$script:ServiceName = 'zoomies-agent'
$script:Asset = 'zoomies_windows_amd64.exe'

function Get-InstallDir {
    $base = if ($env:ProgramFiles) { $env:ProgramFiles } else { 'C:\Program Files' }
    "$($base.TrimEnd('\'))\Zoomies"
}
function Get-DataDir {
    $base = if ($env:ProgramData) { $env:ProgramData } else { 'C:\ProgramData' }
    "$($base.TrimEnd('\'))\zoomies"
}

# --- Output -----------------------------------------------------------------

function Write-Step([string]$Text) { Write-Host ''; Write-Host "==> $Text" -ForegroundColor Cyan }
function Write-Ok([string]$Text) { Write-Host "  ok  $Text" -ForegroundColor Green }
function Write-Note([string]$Text) { Write-Host "      $Text" }
function Write-Warn([string]$Text) { Write-Host " warn $Text" -ForegroundColor Yellow }

# Hide the join token wherever a message might carry it. The agent never prints
# it, but the text of an error from a proxy or a server can echo a request.
function Hide-Secret {
    param([string]$Text, [string]$Secret)
    if (-not $Text) { return $Text }
    if ($Secret) { $Text = $Text.Replace($Secret, '<join-token>') }
    $Text
}

# A failure the person has to act on. It is thrown so the callers' finally
# blocks run, and turned into an exit status once, at the bottom.
function Stop-Install([string]$Message) {
    throw [System.InvalidOperationException]::new($Message)
}

# --- Checks that decide whether to start at all ------------------------------

# The CPU architecture of the operating system, not of this PowerShell process.
# An x64 PowerShell running under emulation on Windows on ARM reports AMD64 in
# the environment, and that machine would be given a binary it can only run
# slowly and a service that is not what the person thinks they installed.
function Get-OSArchitecture {
    try {
        $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
        if ($arch) { return $arch }
    } catch {
        # Older .NET Framework: fall through to the environment.
        $null = $_
    }
    if (-not [Environment]::Is64BitOperatingSystem) { return 'X86' }
    $envArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    switch ($envArch) {
        'AMD64' { 'X64' }
        'ARM64' { 'Arm64' }
        default { $envArch }
    }
}

# Windows PowerShell 5.1 only exists on Windows and has no $IsWindows; 7 and
# later define it on every platform.
function Test-WindowsHost {
    $defined = Get-Variable -Name IsWindows -ValueOnly -ErrorAction SilentlyContinue
    $null -eq $defined -or [bool]$defined
}

function Test-Elevated {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    ([Security.Principal.WindowsPrincipal]$identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Test-Platform {
    param([string]$Architecture = (Get-OSArchitecture), [int]$Major = $PSVersionTable.PSVersion.Major, [int]$Minor = $PSVersionTable.PSVersion.Minor)
    if ($Major -lt 5 -or ($Major -eq 5 -and $Minor -lt 1)) {
        Stop-Install "This needs Windows PowerShell 5.1 or PowerShell 7 or later; this is $Major.$Minor. Update Windows PowerShell, or install PowerShell 7 from https://aka.ms/powershell."
    }
    if (-not (Test-WindowsHost)) {
        Stop-Install 'This installer is for Windows. On Linux or macOS, use: curl -fsSL https://zoomies.sh/install.sh | sh -s -- --mode agent ...'
    }
    switch ($Architecture) {
        'X64' { }
        'Arm64' { Stop-Install 'This is a Windows on ARM machine, and Zoomies only publishes a Windows build for x86-64 (amd64). Nothing has been installed.' }
        default { Stop-Install "This is a $Architecture version of Windows, and Zoomies only publishes a Windows build for 64-bit x86 (amd64). Nothing has been installed." }
    }
    if (-not [Environment]::Is64BitProcess) {
        Stop-Install 'This is a 32-bit PowerShell window. Open the 64-bit one (Windows PowerShell, not Windows PowerShell (x86)) as administrator and run the command again. Nothing has been installed.'
    }
}

function Assert-Elevated {
    if (-not (Test-Elevated)) {
        Stop-Install ("This window is not running as administrator, and the agent is installed as a Windows service. " +
            "Close this window, open PowerShell with 'Run as administrator' (right-click it in the Start menu), and paste the command again. " +
            "It does not elevate itself because the command holds a join token. Nothing has been installed.")
    }
}

# --- Versions and downloads --------------------------------------------------

# Mirrors install.sh: latest, dev, or a tag with or without its leading v.
function Resolve-ReleaseTag {
    param([string]$Requested)
    if (-not $Requested) { $Requested = 'latest' }
    if ($Requested -notmatch '^[A-Za-z0-9._-]+$') {
        Stop-Install "-Version '$Requested' does not look like a release tag or channel. Try latest, dev, or v1.2.3."
    }
    switch -Regex ($Requested) {
        '^latest$' { 'latest' }
        '^dev$' { 'dev' }
        '^v' { $Requested }
        default { "v$Requested" }
    }
}

# GitHub answers /releases/latest/download/<asset> with the newest published
# release, and the rolling dev prerelease deliberately does not count as one.
function Get-DownloadUrl {
    param([string]$Tag, [string]$Name)
    if ($Tag -eq 'latest') { return "$($script:BaseUrl)/latest/download/$Name" }
    "$($script:BaseUrl)/download/$Tag/$Name"
}

function Initialize-Network {
    # Windows PowerShell 5.1 negotiates TLS 1.0 unless told otherwise, and
    # GitHub refuses that.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
}

# Never the curl alias, which is Invoke-WebRequest with different parameters on
# 5.1 and the real curl.exe elsewhere. -UseBasicParsing keeps Internet Explorer
# out of it on a server that never ran its first-launch wizard.
function Save-Url {
    param([string]$Url, [string]$Path)
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $Path
}

function Get-UrlText {
    param([string]$Url)
    $response = Invoke-WebRequest -UseBasicParsing -Uri $Url
    $content = $response.Content
    if ($content -is [byte[]]) { $content = [Text.Encoding]::UTF8.GetString($content) }
    [string]$content
}

# The expected SHA-256 for an asset out of a checksums.txt, or $null.
function Find-Checksum {
    param([string]$Sums, [string]$Name)
    foreach ($line in ($Sums -split "`r?`n")) {
        if ($line -match '^\s*([0-9a-fA-F]{64})\s+\*?(\S+)\s*$' -and $Matches[2] -eq $Name) {
            return $Matches[1].ToLowerInvariant()
        }
    }
    $null
}

# Downloads the binary into a fresh folder and refuses it unless it matches the
# release's checksums.txt. A download that cannot be verified is refused, as
# install.sh does, because a warning on a pasted command scrolls away unread.
function Get-VerifiedBinary {
    param([string]$Tag, [string]$Directory)
    $file = Join-Path $Directory 'zoomies.exe'
    $url = Get-DownloadUrl $Tag $script:Asset
    Write-Step "Downloading $($script:Asset) $Tag"
    try {
        Save-Url $url $file
    } catch {
        Stop-Install ("Could not download $($script:Asset) $Tag from $url. $($_.Exception.Message) " +
            "Check the version exists and that this machine can reach github.com. If $Tag was published minutes ago its binaries may still be building; try again shortly.")
    }
    Write-Step 'Verifying the checksum'
    $sumsUrl = Get-DownloadUrl $Tag 'checksums.txt'
    try {
        $sums = Get-UrlText $sumsUrl
    } catch {
        Stop-Install "This download cannot be verified: $sumsUrl could not be fetched. Zoomies will not install a binary it has not checked. $($_.Exception.Message)"
    }
    $want = Find-Checksum $sums $script:Asset
    if (-not $want) {
        Stop-Install "This download cannot be verified: checksums.txt has no entry for $($script:Asset). Zoomies will not install a binary it has not checked."
    }
    $got = (Get-FileHash -Algorithm SHA256 -Path $file).Hash.ToLowerInvariant()
    if ($got -ne $want) {
        Stop-Install "Checksum mismatch for $($script:Asset). Expected $want, got $got. Do not run this binary. Try again, and if it happens twice, report it."
    }
    Write-Ok "sha256 $($got.Substring(0, 8))... matches"
    $file
}

# --- The machine -------------------------------------------------------------

function Get-AgentService {
    Get-Service -Name $script:ServiceName -ErrorAction SilentlyContinue
}

# Enrolled means credentials are on disk. A service with none, or credentials
# with no service, is a half-finished install, and joining again is the repair.
function Test-Enrolled {
    Test-Path -LiteralPath "$(Get-DataDir)\work\agent.json"
}

function Stop-AgentService {
    $svc = Get-AgentService
    if (-not $svc -or $svc.Status -eq 'Stopped') { return }
    Stop-Service -Name $script:ServiceName -Force
    $svc.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(60))
}

function Copy-Binary {
    param([string]$Source)
    $dir = Get-InstallDir
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $target = "$dir\zoomies.exe"
    $staged = "$target.new"
    Copy-Item -LiteralPath $Source -Destination $staged -Force
    Move-Item -LiteralPath $staged -Destination $target -Force
    $target
}

function Split-PathList([string]$List) {
    @($List -split ';' | Where-Object { $_ })
}

function Add-MachinePath {
    param([string]$Directory)
    $current = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $entries = Split-PathList $current
    if ($entries | Where-Object { $_.TrimEnd('\') -ieq $Directory.TrimEnd('\') }) { return $false }
    [Environment]::SetEnvironmentVariable('Path', (($entries + $Directory) -join ';'), 'Machine')
    $env:Path = "$env:Path;$Directory"
    $true
}

function Remove-MachinePath {
    param([string]$Directory)
    $entries = Split-PathList ([Environment]::GetEnvironmentVariable('Path', 'Machine'))
    $kept = @($entries | Where-Object { $_.TrimEnd('\') -ine $Directory.TrimEnd('\') })
    if ($kept.Count -eq $entries.Count) { return $false }
    [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'Machine')
    $true
}

# The accounts, other than the two that may, with access to a folder. The agent
# keeps a bearer token for the whole host under the data folder; on Windows the
# folder's ACL is what the 0600 mode is on Linux.
function Get-UnexpectedAclEntry {
    param([string]$Path)
    $allowed = @('NT AUTHORITY\SYSTEM', 'BUILTIN\Administrators')
    $acl = Get-Acl -LiteralPath $Path
    @($acl.Access | Where-Object { $allowed -notcontains $_.IdentityReference.Value } |
        ForEach-Object { $_.IdentityReference.Value } | Sort-Object -Unique)
}

function Protect-DataDir {
    $dir = Get-DataDir
    if (-not (Test-Path -LiteralPath $dir)) { return }
    $extra = Get-UnexpectedAclEntry $dir
    if ($extra.Count -eq 0) {
        Write-Ok "$dir is readable only by SYSTEM and Administrators"
        return
    }
    Write-Warn "$dir was also open to: $($extra -join ', '). Restricting it."
    & icacls.exe $dir /inheritance:r /grant:r 'SYSTEM:(OI)(CI)F' /grant:r 'Administrators:(OI)(CI)F' /T | Out-Null
    if ($LASTEXITCODE -ne 0) { Stop-Install "Could not restrict $dir with icacls (exit $LASTEXITCODE). The agent's credentials live there; fix its permissions before continuing." }
    $extra = Get-UnexpectedAclEntry $dir
    if ($extra.Count -gt 0) { Stop-Install "$dir is still open to: $($extra -join ', ')." }
    Write-Ok "$dir is now readable only by SYSTEM and Administrators"
}

# The last lines of the agent's log, for a service that would not start. The
# service has no console, so this file is the only place it can explain itself.
function Get-AgentLogTail {
    param([int]$Lines = 20)
    $log = "$(Get-DataDir)\zoomies-agent.log"
    if (-not (Test-Path -LiteralPath $log)) { return "(no log yet at $log)" }
    (Get-Content -LiteralPath $log -Tail $Lines) -join "`n"
}

function Assert-ServiceRunning {
    $deadline = (Get-Date).AddSeconds(30)
    do {
        $svc = Get-AgentService
        if ($svc -and $svc.Status -eq 'Running') { break }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    if (-not $svc) { Stop-Install "The $($script:ServiceName) service is not registered. See the output above for why." }
    if ($svc.Status -ne 'Running') {
        Stop-Install ("The $($script:ServiceName) service is $($svc.Status), not Running. The end of its log, $(Get-DataDir)\zoomies-agent.log:`n" + (Get-AgentLogTail))
    }
    $cim = Get-CimInstance -ClassName Win32_Service -Filter "Name='$($script:ServiceName)'" -ErrorAction SilentlyContinue
    if ($cim -and $cim.StartMode -ne 'Auto') {
        Write-Warn "The service start mode is $($cim.StartMode). Setting it to Automatic so the agent survives a reboot."
        Set-Service -Name $script:ServiceName -StartupType Automatic
    }
    Write-Ok "the $($script:ServiceName) service is running and starts automatically"
}

# --- Joining -----------------------------------------------------------------

# Names the likely cause of a join that failed, from what the agent said. The
# agent's own messages already explain themselves; this keeps them from being
# the last word when the person is looking at a PowerShell error.
function Get-JoinAdvice {
    param([string]$Output)
    if ($Output -match 'expired|already been used|rejected this join token') {
        return 'The join token has expired or was already used. Tokens are single-use and last 15 minutes by default. Mint a new one under Hosts, Add a host, and run the new command.'
    }
    if ($Output -match 'could not reach|does not resolve|connection refused|timed out|no such host') {
        return 'This machine could not reach the controller. The agent only connects outbound, so check the address in the command, that the controller is running, and that no firewall blocks this machine from it.'
    }
    if ($Output -match 'tailcat') {
        return 'The private connection (Tailcat) did not come up. Check this machine has outbound internet access, then mint a new token and try again.'
    }
    if ($Output -match 'certificate') {
        return 'The controller''s TLS certificate was not trusted. For a private certificate authority, run `zoomies agent join` yourself with --ca-file.'
    }
    $null
}

function Invoke-Join {
    param([string]$Exe, [string]$ControllerUrl, [string]$Token, [bool]$Replace)
    $arguments = @('agent', 'join', $ControllerUrl, '--token', $Token, '--backend', 'process', '--non-interactive')
    if ($Replace) { $arguments += '--yes' }
    # Continue, not Stop: Windows PowerShell 5.1 turns the first line a native
    # program writes to stderr into a terminating error once stderr is merged,
    # and the agent reports progress there.
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $lines = & $Exe @arguments 2>&1 | ForEach-Object { Hide-Secret ([string]$_) $Token }
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    foreach ($line in $lines) { Write-Host "      $line" }
    if ($code -ne 0) {
        $advice = Get-JoinAdvice ($lines -join "`n")
        $message = "zoomies agent join failed (exit $code)."
        if ($advice) { $message += " $advice" }
        Stop-Install $message
    }
}

# --- Actions -----------------------------------------------------------------

function Confirm-Plan {
    param([string[]]$Lines, [switch]$Yes)
    Write-Step 'This will'
    foreach ($line in $Lines) { Write-Note "- $line" }
    if ($Yes) { return }
    if (-not [Environment]::UserInteractive) {
        Stop-Install 'There is nobody to confirm. Re-run with -Yes to go ahead without asking.'
    }
    $reply = Read-Host 'Continue? [y/N]'
    if ($reply -notmatch '^(y|yes)$') { Stop-Install 'Nothing was changed.' }
}

function Install-Agent {
    param([string]$Controller, [string]$JoinToken, [string]$Version = 'latest', [switch]$Force, [switch]$Yes)
    $enrolled = Test-Enrolled
    $upgrade = $enrolled -and -not $Force
    # An upgrade redeems nothing, so it needs neither; asking for them would make
    # the UI's command fail on a re-run for a reason that has nothing to do with it.
    if (-not $upgrade) {
        if (-not $Controller) { Stop-Install 'Missing -Controller. Copy the whole command from the UI, under Hosts, Add a host.' }
        if (-not $JoinToken) { Stop-Install 'Missing -JoinToken. Copy the whole command from the UI, under Hosts, Add a host.' }
        if ($Controller -notmatch '^(https?|tailcat)://\S+$') {
            Stop-Install "-Controller '$Controller' is not an address a host can join. It starts with https://, http:// or tailcat://."
        }
    }
    $tag = Resolve-ReleaseTag $Version
    $dir = Get-InstallDir

    $plan = @("download zoomies_windows_amd64.exe ($tag) from $($script:BaseUrl) and check its SHA-256",
        "install it to $dir\zoomies.exe and add that folder to the machine PATH")
    if ($upgrade) {
        $plan += 'this machine is already enrolled: replace the binary and restart the service, keeping its credentials (use -Force to enrol again)'
    } else {
        $plan += "join $(if ($Controller -like 'tailcat://*') { 'the private connection' } else { $Controller }) and register the $($script:ServiceName) service, running jobs as processes (no containers)"
        if ($enrolled) { $plan += 'replace the credentials this machine already holds (-Force)' }
    }
    Confirm-Plan $plan -Yes:$Yes

    $temp = Join-Path ([IO.Path]::GetTempPath()) ("zoomies-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $temp | Out-Null
    try {
        $downloaded = Get-VerifiedBinary $tag $temp

        Write-Step "Installing to $dir"
        Stop-AgentService
        $exe = Copy-Binary $downloaded
        Write-Ok "$exe"
        if (Add-MachinePath $dir) { Write-Ok 'added the folder to the machine PATH (new windows see it)' }
    } finally {
        Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
    }

    if ($upgrade) {
        Write-Step 'Restarting the agent'
        Start-Service -Name $script:ServiceName
    } else {
        Write-Step 'Joining the controller'
        Invoke-Join -Exe $exe -ControllerUrl $Controller -Token $JoinToken -Replace $enrolled
    }

    Write-Step 'Checking the service'
    Assert-ServiceRunning
    Protect-DataDir

    $dataDir = Get-DataDir
    Write-Step 'Done'
    Write-Note "binary        $exe"
    Write-Note "config        $dataDir\zoomies.yaml"
    Write-Note "credentials   $dataDir\work\agent.json"
    Write-Note "agent log     $dataDir\zoomies-agent.log"
    Write-Note ''
    Write-Note 'Next: this host reports os=windows and runs jobs as processes. Create a pool for it in the UI'
    Write-Note 'with the process backend and the host selector os=windows, or with the CLI:'
    Write-Note '  zoomies pools create --name windows --labels windows --installation <id> --backend process --host-selector os=windows'
}

function Uninstall-Agent {
    param([switch]$Yes)
    $dir = Get-InstallDir
    $dataDir = Get-DataDir
    Confirm-Plan -Yes:$Yes -Lines @("stop and remove the $($script:ServiceName) service",
        "delete $dir and take it off the machine PATH",
        "leave $dataDir (the host's credentials and logs) in place")
    Write-Step 'Removing the agent'
    if (Get-AgentService) {
        Stop-AgentService
        & sc.exe delete $script:ServiceName | Out-Null
        if ($LASTEXITCODE -ne 0) { Stop-Install "sc.exe delete $($script:ServiceName) failed (exit $LASTEXITCODE)." }
        Write-Ok "removed the $($script:ServiceName) service"
    } else {
        Write-Note "no $($script:ServiceName) service was registered"
    }
    if (Test-Path -LiteralPath $dir) {
        Remove-Item -LiteralPath $dir -Recurse -Force
        Write-Ok "deleted $dir"
    }
    if (Remove-MachinePath $dir) { Write-Ok 'took the folder off the machine PATH' }
    Write-Step 'Left behind'
    Write-Note "$dataDir still holds this host's credentials, configuration and log."
    Write-Note "To remove them too: Remove-Item -Recurse -Force '$dataDir'"
    Write-Note 'The host also stays on the controller as an offline host until you delete it under Hosts.'
}

function Invoke-ZoomiesInstaller {
    param([string]$Controller, [string]$JoinToken, [string]$Version = 'latest', [switch]$Uninstall, [switch]$Yes, [switch]$Force)
    Initialize-Network
    Test-Platform
    Assert-Elevated
    if ($Uninstall) {
        Uninstall-Agent -Yes:$Yes
    } else {
        Install-Agent -Controller $Controller -JoinToken $JoinToken -Version $Version -Force:$Force -Yes:$Yes
    }
}

# Dot-sourced by the tests, which exercise the functions above and must not
# start an install.
if ($env:ZOOMIES_INSTALL_PS1_LIBRARY -eq '1') { return }

try {
    if ($Mode -ne 'agent') { Stop-Install 'Only -Mode agent is supported on Windows. The controller runs on Linux.' }
    Invoke-ZoomiesInstaller -Controller $Controller -JoinToken $JoinToken -Version $Version -Uninstall:$Uninstall -Yes:$Yes -Force:$Force
} catch {
    $text = Hide-Secret $_.Exception.Message $JoinToken
    Write-Host ''
    Write-Host "error: $text" -ForegroundColor Red
    # Never `exit` from a pasted command: this body runs as a script block in
    # the person's own window, and exit would close it with the message. From a
    # saved file, a real exit status is what a script wants.
    if ($PSCommandPath) { exit 1 }
    $global:LASTEXITCODE = 1
}
