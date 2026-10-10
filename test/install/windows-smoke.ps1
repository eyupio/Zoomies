# Runs install.ps1 end to end on a hosted Windows runner, against a real
# controller built from this commit and a local stand-in for the release page.
# It proves the three things the unit tests cannot: the service is registered
# and started by the real binary, a second run upgrades without enrolling
# again, and -Uninstall takes it all away.
#
#   test/install/windows-smoke.ps1 -Binary .\zoomies.exe
#
# It changes the machine it runs on (a service, Program Files, the machine
# PATH), so it refuses to run anywhere that is not a CI runner unless told to.
param(
    [Parameter(Mandatory)][string]$Binary,
    [switch]$IKnowThisChangesThisMachine
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

if (-not $env:GITHUB_ACTIONS -and -not $IKnowThisChangesThisMachine) {
    throw 'This installs a service on the machine it runs on. Run it from CI, or pass -IKnowThisChangesThisMachine.'
}

$root = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$installer = Join-Path $root 'install.ps1'
$binary = (Resolve-Path $Binary).Path
$work = Join-Path ([IO.Path]::GetTempPath()) ('zoomies-smoke-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
$tag = 'v0.0.0-smoke'
$controllerPort = 18080
$releasePort = 18099
$controllerUrl = "http://127.0.0.1:$controllerPort"
$controllerProcess = $null
$releaseJob = $null

function Assert-That([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "smoke: $Message" }
    Write-Host "  ok  $Message"
}

try {
    # The release page: /download/<tag>/<asset> for the binary and checksums.txt.
    $site = Join-Path $work 'site'
    $dir = Join-Path $site "download\$tag"
    New-Item -ItemType Directory -Path $dir | Out-Null
    Copy-Item $binary (Join-Path $dir 'zoomies_windows_amd64.exe')
    $hash = (Get-FileHash -Algorithm SHA256 (Join-Path $dir 'zoomies_windows_amd64.exe')).Hash.ToLowerInvariant()
    Set-Content -Path (Join-Path $dir 'checksums.txt') -Value "$hash  zoomies_windows_amd64.exe" -Encoding ascii
    # $using: hands the job the two values it needs; PSScriptAnalyzer asks for it
    # (PSUseUsingScopeModifierInNewRunspaces) rather than a param block.
    $releaseJob = Start-Job -ScriptBlock {
        $port = $using:releasePort
        $root = $using:site
        $listener = [Net.HttpListener]::new()
        $listener.Prefixes.Add("http://127.0.0.1:$port/")
        $listener.Start()
        while ($listener.IsListening) {
            $context = $listener.GetContext()
            $path = Join-Path $root ($context.Request.Url.AbsolutePath.TrimStart('/').Replace('/', '\'))
            if (Test-Path -LiteralPath $path -PathType Leaf) {
                $bytes = [IO.File]::ReadAllBytes($path)
                $context.Response.OutputStream.Write($bytes, 0, $bytes.Length)
            } else {
                $context.Response.StatusCode = 404
            }
            $context.Response.Close()
        }
    }

    # A real controller, auth off, with no agent of its own.
    $env:ZOOMIES_DISABLE_AUTH = 'true'
    $env:ZOOMIES_BIND = "127.0.0.1:$controllerPort"
    $env:ZOOMIES_DB_PATH = Join-Path $work 'controller.db'
    $env:ZOOMIES_AGENT_EMBEDDED = 'false'
    $controllerProcess = Start-Process -FilePath $binary -ArgumentList 'controller' -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $work 'controller.out') -RedirectStandardError (Join-Path $work 'controller.err')
    $deadline = (Get-Date).AddSeconds(60)
    $up = $false
    while (-not $up -and (Get-Date) -lt $deadline) {
        try { Invoke-WebRequest -UseBasicParsing -Uri "$controllerUrl/healthz" | Out-Null; $up = $true } catch { Start-Sleep -Milliseconds 500 }
    }
    Assert-That $up 'the controller came up'

    function New-JoinToken {
        $json = & $binary hosts join-token create --url $controllerUrl --output json | Out-String
        ($json | ConvertFrom-Json).token
    }
    $env:ZOOMIES_BASE_URL = "http://127.0.0.1:$releasePort"

    Write-Host '== first run: install and join'
    $token = New-JoinToken
    $output = & $installer -Mode agent -Controller $controllerUrl -JoinToken $token -Version $tag -Yes *>&1 | Out-String
    Write-Host $output
    Assert-That ($output -notmatch [regex]::Escape($token)) 'the join token is not in the installer output'
    $svc = Get-Service -Name 'zoomies-agent' -ErrorAction SilentlyContinue
    Assert-That ($null -ne $svc) 'the zoomies-agent service is registered'
    Assert-That ($svc.Status -eq 'Running') 'the zoomies-agent service is running'
    Assert-That ((Get-CimInstance Win32_Service -Filter "Name='zoomies-agent'").StartMode -eq 'Auto') 'the service starts automatically'
    Assert-That (Test-Path "$env:ProgramFiles\Zoomies\zoomies.exe") 'the binary is in Program Files'
    $hosts = & $binary hosts list --url $controllerUrl --output json | Out-String | ConvertFrom-Json
    $items = if ($hosts.items) { @($hosts.items) } else { @($hosts) }
    Assert-That ($items.Count -ge 1) 'the controller lists the host'
    Assert-That (($items[0].os -eq 'windows') -or ($items[0].labels.os -eq 'windows') -or ($items[0].backends -contains 'process')) 'the host is a Windows host on the process backend'
    $leaked = Get-ChildItem -Path "$env:ProgramData\zoomies" -Recurse -File -ErrorAction SilentlyContinue |
        Select-String -SimpleMatch -Pattern $token -List
    Assert-That (-not $leaked) 'the join token is not on disk'

    Write-Host '== second run: upgrade, no new enrolment'
    $before = (Get-Content "$env:ProgramData\zoomies\work\agent.json" -Raw)
    $second = & $installer -Mode agent -Controller $controllerUrl -JoinToken 'not-a-real-token' -Version $tag -Yes *>&1 | Out-String
    Write-Host $second
    Assert-That ($second -match 'already enrolled') 'a re-run says the machine is already enrolled'
    Assert-That ((Get-Content "$env:ProgramData\zoomies\work\agent.json" -Raw) -eq $before) 'the credentials are untouched'
    Assert-That ((Get-Service 'zoomies-agent').Status -eq 'Running') 'the service is running again after the upgrade'

    Write-Host '== uninstall'
    $removed = & $installer -Uninstall -Yes *>&1 | Out-String
    Write-Host $removed
    Assert-That ($null -eq (Get-Service -Name 'zoomies-agent' -ErrorAction SilentlyContinue)) 'the service is gone'
    Assert-That (-not (Test-Path "$env:ProgramFiles\Zoomies")) 'the install folder is gone'
    Assert-That ($removed -match 'Left behind') 'it says what it left behind'
} finally {
    if ($controllerProcess -and -not $controllerProcess.HasExited) { Stop-Process -Id $controllerProcess.Id -Force }
    if ($releaseJob) { Stop-Job $releaseJob -ErrorAction SilentlyContinue; Remove-Job $releaseJob -Force -ErrorAction SilentlyContinue }
    if (Get-Service -Name 'zoomies-agent' -ErrorAction SilentlyContinue) {
        Stop-Service -Name 'zoomies-agent' -Force -ErrorAction SilentlyContinue
        & sc.exe delete zoomies-agent | Out-Null
    }
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
