#Requires -Version 5.1
<#
.SYNOPSIS
    pairmux installer for Windows, safe for `irm https://.../install.ps1 | iex`.

.DESCRIPTION
    pairmux is a layer over tmux, and tmux does not run on Windows: there is no
    native Windows artifact and never will be. What does work, and what Windows
    users actually want, is pairmux inside WSL. So this script does not pretend
    to install a Windows binary: it finds WSL, checks it has a distribution,
    and runs the ordinary POSIX installer inside it.

    Anything that would end with pairmux not on the WSL PATH is refused with the
    command that fixes it, rather than half-done.

.PARAMETER Version
    Install a specific tagged release, e.g. v0.5.1. Default: the latest release.

.PARAMETER InstallDir
    Install target *inside the distribution*. Default: ~/.local/bin.

.PARAMETER Distribution
    WSL distribution to install into. Default: the WSL default distribution.

.PARAMETER DryRun
    Print what would be run, then exit without touching the distribution. Set
    PAIRMUX_DRY_RUN to reach this from a piped invocation, which takes no
    arguments.

.EXAMPLE
    irm https://raw.githubusercontent.com/treeleaves30760/pairmux/main/install.ps1 | iex

.EXAMPLE
    # A piped script takes no arguments, so configure it through the environment:
    $env:PAIRMUX_VERSION = 'v0.5.1'
    irm https://raw.githubusercontent.com/treeleaves30760/pairmux/main/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -Version v0.5.1 -Distribution Ubuntu -DryRun
#>
[CmdletBinding()]
param(
    [string]$Version = $env:PAIRMUX_VERSION,
    [string]$InstallDir = $env:PAIRMUX_INSTALL_DIR,
    [string]$Distribution = $env:PAIRMUX_WSL_DISTRO,
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'

# A piped script takes no arguments, so the switch is also readable from the
# environment. Every non-empty value means yes EXCEPT the four that plainly mean
# no: a bare [bool] cast would make PAIRMUX_DRY_RUN=0 turn dry run *on*, which is
# the opposite of what anyone typing it intends.
function Test-PairmuxTruthy {
    param([string]$Value)
    if (-not $Value) { return $false }
    return @('0', 'false', 'no', 'off') -notcontains $Value.Trim().ToLowerInvariant()
}
if (-not $DryRun -and (Test-PairmuxTruthy $env:PAIRMUX_DRY_RUN)) {
    $DryRun = $true
}

$InstallUrl = 'https://raw.githubusercontent.com/treeleaves30760/pairmux/main/install.sh'

# Write-Host, not Write-Output, throughout: this script is normally run as
# `irm ... | iex`, where anything written to the success stream becomes the
# pipeline's return value instead of something the user sees. PSScriptAnalyzer's
# PSAvoidUsingWriteHost is about libraries; for an installer the console IS the
# output.
function Write-Step { param([string]$Message) Write-Host "==> $Message" }
function Write-Note { param([string]$Message) Write-Host "    $Message" -ForegroundColor DarkGray }
function Write-Fatal {
    param([string]$Message, [string]$Hint)
    Write-Host "error: $Message" -ForegroundColor Red
    if ($Hint) { Write-Host "  $Hint" -ForegroundColor Yellow }
    exit 1
}

# PowerShell 7 runs on Linux and macOS too, where WSL is neither present nor
# needed: the POSIX installer is already the right tool and is one pipe away.
if ($PSVersionTable.PSVersion.Major -ge 6 -and -not $IsWindows) {
    Write-Fatal "this script is the Windows entry point; you are already on a POSIX system" `
        "curl -fsSL $InstallUrl | sh"
}

if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
    Write-Fatal "pairmux needs tmux, which has no Windows build, so it installs into WSL, and WSL was not found" `
        "install it with:  wsl --install     (then reopen this terminal and re-run)"
}

# wsl.exe emits UTF-16 by default, which turns every parsed line into a string
# full of NULs. WSL_UTF8 is the supported way to get plain UTF-8 out of it.
$previousWslUtf8 = $env:WSL_UTF8
$env:WSL_UTF8 = '1'
try {
    $distros = @(& wsl.exe --list --quiet 2>$null |
        ForEach-Object { $_.Trim() } |
        Where-Object { $_ })
    if ($LASTEXITCODE -ne 0 -or $distros.Count -eq 0) {
        Write-Fatal "WSL is present but has no Linux distribution installed" `
            "install one with:  wsl --install -d Ubuntu"
    }

    $wslArgs = @()
    if ($Distribution) {
        if ($distros -notcontains $Distribution) {
            Write-Fatal "no WSL distribution named '$Distribution' (found: $($distros -join ', '))" `
                "pick one of those, or omit -Distribution to use the default"
        }
        $wslArgs += @('--distribution', $Distribution)
        Write-Step "Installing pairmux into WSL distribution '$Distribution'"
    } else {
        Write-Step "Installing pairmux into the default WSL distribution"
    }

    # The POSIX installer needs one of these to fetch the release archive. A
    # minimal distribution image may ship neither, and finding that out here
    # beats finding it out halfway through a piped shell script.
    & wsl.exe @wslArgs -- sh -c 'command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1' 2>$null
    if ($LASTEXITCODE -ne 0) {
        Write-Fatal "the distribution has neither curl nor wget, so the installer cannot download anything" `
            "inside WSL run:  sudo apt update && sudo apt install -y curl"
    }

    # Everything below runs inside the distribution. install.sh reads its target
    # directory from the environment, so it is exported rather than passed.
    $inner = "set -e; "
    if ($InstallDir) {
        $inner += "export PAIRMUX_INSTALL_DIR='$InstallDir'; "
    }
    $inner += "curl -fsSL '$InstallUrl' | sh -s --"
    if ($Version) {
        $inner += " --version '$Version'"
    }

    if ($DryRun) {
        Write-Step 'Dry run: nothing will be installed'
        Write-Note "wsl.exe $($wslArgs -join ' ') -- sh -c `"$inner`""
        exit 0
    }

    & wsl.exe @wslArgs -- sh -c $inner
    if ($LASTEXITCODE -ne 0) {
        Write-Fatal "the installer failed inside WSL (exit code $LASTEXITCODE)" `
            "re-run it there directly to see the full output:  wsl -- sh -c ""curl -fsSL $InstallUrl | sh"""
    }

    # tmux is pairmux's runtime dependency, not a build one, so a successful
    # install can still be a pairmux that cannot open a terminal.
    & wsl.exe @wslArgs -- sh -c 'command -v tmux >/dev/null 2>&1' 2>$null
    if ($LASTEXITCODE -ne 0) {
        Write-Host 'warning: tmux is not installed in that distribution; pairmux needs tmux 3.2 or newer' -ForegroundColor Yellow
        Write-Note 'inside WSL run:  sudo apt update && sudo apt install -y tmux'
    }

    Write-Step 'Done. pairmux lives inside WSL, so run it from there:'
    Write-Note 'wsl -- pairmux version'
} finally {
    $env:WSL_UTF8 = $previousWslUtf8
}
