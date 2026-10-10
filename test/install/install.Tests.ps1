# Pester 5 tests for install.ps1. They dot-source the script with
# ZOOMIES_INSTALL_PS1_LIBRARY=1, which defines its functions and stops before
# anything is installed. What they pin is the behaviour that must hold before
# the first byte is downloaded and the behaviour that keeps the join token out
# of sight, which a Windows laptop is a poor place to discover is wrong.

BeforeAll {
    $env:ZOOMIES_INSTALL_PS1_LIBRARY = '1'
    . (Join-Path (Join-Path $PSScriptRoot '..\..') 'install.ps1')
    # These cmdlets exist on Windows. Defined here when they are missing so the
    # suite also runs under PowerShell on Linux, where Pester can mock them.
    foreach ($name in 'Start-Service', 'Stop-Service', 'Get-Service', 'Set-Service') {
        if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
            Set-Item -Path "function:global:$name" -Value { }
        }
    }
    Mock Test-WindowsHost { $true }
}


AfterAll {
    Remove-Item Env:\ZOOMIES_INSTALL_PS1_LIBRARY -ErrorAction SilentlyContinue
}

Describe 'Resolve-ReleaseTag' {
    It 'maps <requested> to <tag>' -ForEach @(
        @{ requested = 'latest'; tag = 'latest' }
        @{ requested = 'dev'; tag = 'dev' }
        @{ requested = 'v1.2.3'; tag = 'v1.2.3' }
        @{ requested = '1.2.3'; tag = 'v1.2.3' }
        @{ requested = ''; tag = 'latest' }
    ) {
        Resolve-ReleaseTag $requested | Should -Be $tag
    }

    It 'refuses a version that could change the download URL' {
        { Resolve-ReleaseTag '../../evil' } | Should -Throw '*does not look like a release tag*'
        { Resolve-ReleaseTag 'v1 2' } | Should -Throw '*does not look like a release tag*'
    }
}

Describe 'Get-DownloadUrl' {
    It 'asks GitHub for the newest published release when the tag is latest' {
        Get-DownloadUrl 'latest' 'zoomies_windows_amd64.exe' | Should -Be 'https://github.com/eyupio/zoomies/releases/latest/download/zoomies_windows_amd64.exe'
    }
    It 'names the tag otherwise, dev included' {
        Get-DownloadUrl 'dev' 'checksums.txt' | Should -Be 'https://github.com/eyupio/zoomies/releases/download/dev/checksums.txt'
        Get-DownloadUrl 'v1.2.3' 'checksums.txt' | Should -Be 'https://github.com/eyupio/zoomies/releases/download/v1.2.3/checksums.txt'
    }
}

Describe 'Find-Checksum' {
    BeforeAll {
        $script:hash = ('ab' * 32)
        $script:sums = "$($script:hash)  zoomies_linux_amd64`n$(('cd' * 32))  zoomies_windows_amd64.exe`r`n"
    }
    It 'finds the entry for the asset by exact name' {
        Find-Checksum $script:sums 'zoomies_windows_amd64.exe' | Should -Be ('cd' * 32)
    }
    It 'returns nothing when the asset is not listed, so the install stops' {
        Find-Checksum $script:sums 'zoomies_windows_arm64.exe' | Should -BeNullOrEmpty
    }
}

Describe 'Test-Platform' {
    It 'accepts x86-64' {
        { Test-Platform -Architecture 'X64' -Major 5 -Minor 1 } | Should -Not -Throw
    }
    It 'refuses ARM64 and says only amd64 is published' {
        { Test-Platform -Architecture 'Arm64' -Major 7 -Minor 4 } | Should -Throw '*Windows on ARM*amd64*Nothing has been installed*'
    }
    It 'refuses 32-bit Windows' {
        { Test-Platform -Architecture 'X86' -Major 5 -Minor 1 } | Should -Throw '*x86*Nothing has been installed*'
    }
    It 'refuses a PowerShell older than 5.1' {
        { Test-Platform -Architecture 'X64' -Major 5 -Minor 0 } | Should -Throw '*5.1*'
    }
}

Describe 'Starting an install' {
    BeforeEach {
        Mock Initialize-Network { }
        Mock Save-Url { throw 'nothing may be downloaded' }
        Mock Get-UrlText { throw 'nothing may be downloaded' }
        Mock Copy-Binary { throw 'nothing may be installed' }
    }
    It 'stops on ARM64 before touching the network or the disk' {
        Mock Get-OSArchitecture { 'Arm64' }
        Mock Test-Elevated { $true }
        { Invoke-ZoomiesInstaller } | Should -Throw '*Windows on ARM*'
        Should -Invoke Save-Url -Times 0
        Should -Invoke Copy-Binary -Times 0
    }
    It 'stops when the window is not elevated and says how to open one' {
        Mock Get-OSArchitecture { 'X64' }
        Mock Test-Elevated { $false }
        { Invoke-ZoomiesInstaller } | Should -Throw "*Run as administrator*"
        Should -Invoke Save-Url -Times 0
        Should -Invoke Copy-Binary -Times 0
    }
}

Describe 'Hide-Secret' {
    It 'replaces the token wherever it appears' {
        Hide-Secret 'bad token zoojoin_abc, retry zoojoin_abc' 'zoojoin_abc' | Should -Be 'bad token <join-token>, retry <join-token>'
    }
    It 'leaves text alone when there is no secret' {
        Hide-Secret 'nothing here' '' | Should -Be 'nothing here'
    }
}

Describe 'Install-Agent' {
    BeforeEach {
        $script:Controller = 'https://zoomies.example.com'
        $script:JoinToken = 'zoojoin_supersecret'
        Mock Confirm-Plan { }
        Mock Get-VerifiedBinary { 'C:\temp\zoomies.exe' }
        Mock Stop-AgentService { }
        Mock Copy-Binary { 'C:\Program Files\Zoomies\zoomies.exe' }
        Mock Add-MachinePath { $false }
        Mock Start-Service { }
        Mock Assert-ServiceRunning { }
        Mock Protect-DataDir { }
        Mock Invoke-Join { }
        Mock Remove-Item { }
        Mock New-Item { }
    }

    It 'joins a machine that is not enrolled, without replacing anything' {
        Mock Test-Enrolled { $false }
        Install-Agent -Controller $script:Controller -JoinToken $script:JoinToken -Yes *>&1 | Out-Null
        Should -Invoke Invoke-Join -Times 1 -ParameterFilter { $Replace -eq $false }
    }

    It 'upgrades an enrolled machine without redeeming a token' {
        Mock Test-Enrolled { $true }
        Install-Agent -Controller $script:Controller -JoinToken $script:JoinToken -Yes *>&1 | Out-Null
        Should -Invoke Invoke-Join -Times 0
        Should -Invoke Start-Service -Times 1
    }

    It 'enrols again on an enrolled machine only with -Force, and says it is replacing' {
        Mock Test-Enrolled { $true }
        Install-Agent -Controller $script:Controller -JoinToken $script:JoinToken -Yes -Force *>&1 | Out-Null
        Should -Invoke Invoke-Join -Times 1 -ParameterFilter { $Replace -eq $true }
    }

    It 'never prints the token' {
        Mock Test-Enrolled { $false }
        $output = (Install-Agent -Controller $script:Controller -JoinToken $script:JoinToken -Yes *>&1 | Out-String)
        $output | Should -Not -Match 'supersecret'
    }

    It 'asks for the controller and the token by name when they are missing' {
        Mock Test-Enrolled { $false }
        { Install-Agent -Controller '' -JoinToken 'x' -Yes } | Should -Throw '*-Controller*'
    }

    It 'refuses a controller address that is not an address' {
        Mock Test-Enrolled { $false }
        { Install-Agent -Controller 'zoomies.example.com' -JoinToken 'x' -Yes } | Should -Throw '*tailcat://*'
    }
}

Describe 'Invoke-Join' -Skip:($env:OS -ne 'Windows_NT') {
    It 'keeps the token out of the output and explains an expired one' {
        $fake = Join-Path $TestDrive 'fake-zoomies.cmd'
        # Echoes every argument, token included, as a hostile server might, then fails.
        Set-Content -Path $fake -Value "@echo off`r`necho rejected this join token %* 1>&2`r`nexit /b 1"
        $join = { Invoke-Join -Exe $fake -ControllerUrl 'https://c' -Token 'zoojoin_tokenvalue' -Replace $false }
        $output = & { $join | Should -Throw '*expired*' } *>&1 | Out-String
        $output | Should -Not -Match 'tokenvalue'
    }
}

Describe 'The command the UI prints' {
    # The server single-quotes every value and doubles a quote inside one. This
    # is the other half of that contract: PowerShell reads such an argument back
    # as exactly the value, with nothing expanded.
    It 'passes hostile values through as one literal argument' {
        $command = "& { param(`$Controller, `$JoinToken) `$Controller + '|' + `$JoinToken } -Controller 'https://x/it''s`$(Get-Date)' -JoinToken 'a`"b``c'"
        $got = & ([scriptblock]::Create($command))
        $got | Should -Be 'https://x/it''s$(Get-Date)|a"b`c'
    }
}

Describe 'Get-UnexpectedAclEntry' -Skip:($env:OS -ne 'Windows_NT') {
    It 'flags a folder that ordinary users can read' {
        $dir = Join-Path $TestDrive 'data'
        New-Item -ItemType Directory -Path $dir | Out-Null
        # A folder under the test drive inherits its parent's ACL, which includes the current user.
        (Get-UnexpectedAclEntry $dir).Count | Should -BeGreaterThan 0
    }
}
