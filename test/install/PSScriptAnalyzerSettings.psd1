# What PSScriptAnalyzer holds install.ps1 to. Two rules are switched off on
# purpose, and each is a decision rather than a convenience.
@{
    Severity     = @('Error', 'Warning')
    ExcludeRules = @(
        # An interactive installer talks to the person at the console. Write-Output
        # would put its progress lines into the pipeline of whatever called it.
        'PSAvoidUsingWriteHost'
        # Stop-Install and the service helpers change state by design, and are
        # internal functions of one script, not cmdlets anyone pipes into -WhatIf.
        'PSUseShouldProcessForStateChangingFunctions'
    )
}
