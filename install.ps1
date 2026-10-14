#Requires -Version 5.1
<#
.SYNOPSIS
    pairmux installer for Windows, safe for `irm https://.../install.ps1 | iex`.

.DESCRIPTION
    By default this installs pairmux natively on Windows (x86-64 or ARM64).
    Windows builds use pairmux's built-in ConPTY terminal backend, so neither
    tmux nor WSL is needed. It applies the same PyPI policy as the POSIX
    install.sh: uv installs the pairmux platform wheel exclusively from
    https://pypi.org/simple, wheels only (no source builds), with no cache,
    while inherited UV_* settings and uv configuration files are ignored. If uv
    is missing it is bootstrapped from Astral's official PowerShell installer,
    downloaded completely to a temporary file before it runs. No administrator
    rights, registry PATH edits or profile edits; add the tool-bin directory to
    PATH yourself if the installer asks.

    Set PAIRMUX_INSTALL_TARGET to 'wsl' (or pass -Target wsl) to install into a
    WSL distribution instead, exactly as earlier versions of this script did:
    it finds WSL, checks it has a distribution, and runs the PyPI/uv Bash
    installer inside it, downloading that script completely before executing.

.PARAMETER Version
    Install a specific tagged release, e.g. v1.0.0. Default: the latest stable
    release on PyPI.

.PARAMETER InstallDir
    Native target: directory that receives pairmux.exe. Default:
    %USERPROFILE%\.local\bin. WSL target: install directory *inside the
    distribution*. Default: ~/.local/bin.

.PARAMETER Distribution
    WSL target only: distribution to install into. Default: the WSL default
    distribution.

.PARAMETER Target
    'native' (the default) or 'wsl'. Also read from PAIRMUX_INSTALL_TARGET.

.PARAMETER DryRun
    Print what would be installed and the exact command, without downloading
    or installing anything. For the WSL target the WSL distribution list is
    still checked. Set PAIRMUX_DRY_RUN to reach this from a piped invocation,
    which takes no arguments.

.EXAMPLE
    irm https://pairmux.treeleaves30760.com/install.ps1 | iex

.EXAMPLE
    # A piped script takes no arguments, so configure it through the environment:
    $env:PAIRMUX_VERSION = 'v1.0.0'
    irm https://pairmux.treeleaves30760.com/install.ps1 | iex

.EXAMPLE
    # The previous behaviour, inside WSL:
    $env:PAIRMUX_INSTALL_TARGET = 'wsl'
    irm https://pairmux.treeleaves30760.com/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -Target wsl -Version v1.0.0 -Distribution Ubuntu -DryRun
#>
[CmdletBinding()]
param(
    [string]$Version = $env:PAIRMUX_VERSION,
    [string]$InstallDir = $env:PAIRMUX_INSTALL_DIR,
    [string]$Distribution = $env:PAIRMUX_WSL_DISTRO,
    [string]$Target = $env:PAIRMUX_INSTALL_TARGET,
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

$InstallUrl = 'https://pairmux.treeleaves30760.com/install.sh'
$PypiIndex = 'https://pypi.org/simple'
$PypiProject = 'https://pypi.org/project/pairmux/'
$UvInstallerUrl = 'https://astral.sh/uv/install.ps1'
$DocsUrl = 'https://pairmux-docs.treeleaves30760.com'

function ConvertTo-ShellLiteral {
    param([string]$Value)
    return "'" + $Value.Replace("'", "'" + '"' + "'" + '"' + "'") + "'"
}

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
    # A script file (-File, .\install.ps1) reports failure through its exit
    # code. A piped run (irm ... | iex) has no script file and executes in the
    # caller's own session, where exit would close the window before anyone
    # could read the error: stop with a terminating error there instead.
    if ($PSCommandPath) { exit 1 }
    throw "pairmux installation failed: $Message"
}

# PowerShell 7 runs on Linux and macOS too, where WSL is neither present nor
# needed: the POSIX installer is already the right tool and is one pipe away.
if ($PSVersionTable.PSVersion.Major -ge 6 -and -not $IsWindows) {
    Write-Fatal "this script is the Windows entry point; you are already on a POSIX system" `
        "curl -fsSL $InstallUrl | bash"
}

# Native is the default. WSL stays available, but only when asked for: a
# distribution name alone is not taken as a request, so a leftover
# PAIRMUX_WSL_DISTRO cannot silently pick a target.
$InstallTarget = 'native'
if ($Target) { $InstallTarget = $Target.Trim().ToLowerInvariant() }
if (@('native', 'wsl') -notcontains $InstallTarget) {
    Write-Fatal "unknown install target '$Target'" `
        "use 'native' (the default) or 'wsl', e.g. `$env:PAIRMUX_INSTALL_TARGET = 'wsl'"
}
if ($InstallTarget -eq 'native' -and $Distribution) {
    Write-Fatal "a WSL distribution ('$Distribution') was selected, but the install target is native Windows" `
        "to install into WSL, set `$env:PAIRMUX_INSTALL_TARGET = 'wsl' (or pass -Target wsl); otherwise clear PAIRMUX_WSL_DISTRO"
}

# --- native target -----------------------------------------------------------

# uv's --no-config does not ignore environment variables. These steer uv and
# its standalone installer (bootstrap download/mirror, constraint and index
# settings), so they are removed for each child process and restored after.
$UvScrubPattern = '^(UV_|CARGO_DIST_|INSTALLER_)|^CARGO_HOME$'

function Invoke-PairmuxClean {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [string[]]$ArgumentList = @(),
        [Parameter(Mandatory = $true)][string]$BinDir,
        [hashtable]$Extra = @{},
        [switch]$Capture
    )
    $saved = @{}
    foreach ($item in @(Get-ChildItem -Path Env:)) {
        if ($item.Name -match $UvScrubPattern) { $saved[$item.Name] = $item.Value }
    }
    $previousEncoding = $null
    try {
        foreach ($name in @($saved.Keys)) {
            [Environment]::SetEnvironmentVariable($name, $null, 'Process')
        }
        [Environment]::SetEnvironmentVariable('UV_TOOL_BIN_DIR', $BinDir, 'Process')
        foreach ($name in @($Extra.Keys)) {
            [Environment]::SetEnvironmentVariable($name, [string]$Extra[$name], 'Process')
        }
        if (-not $Capture) {
            & $FilePath @ArgumentList | Out-Host
            return [pscustomobject]@{ ExitCode = $LASTEXITCODE; Lines = @() }
        }
        # uv prints UTF-8; decode it as such so non-ASCII profile paths compare
        # exactly. A host without a console cannot change this, which is fine
        # for the ASCII paths such hosts use.
        try {
            $previousEncoding = [Console]::OutputEncoding
            [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
        } catch {
            $previousEncoding = $null
        }
        $lines = @(& $FilePath @ArgumentList)
        return [pscustomobject]@{ ExitCode = $LASTEXITCODE; Lines = $lines }
    } finally {
        if ($null -ne $previousEncoding) {
            try { [Console]::OutputEncoding = $previousEncoding } catch { $previousEncoding = $null }
        }
        foreach ($item in @(Get-ChildItem -Path Env:)) {
            if ($item.Name -match $UvScrubPattern) {
                [Environment]::SetEnvironmentVariable($item.Name, $null, 'Process')
            }
        }
        foreach ($name in @($saved.Keys)) {
            [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process')
        }
    }
}

function Resolve-PairmuxRequirement {
    param([string]$Pin)
    if (-not $Pin) { return [pscustomobject]@{ Requirement = 'pairmux'; Expected = '' } }
    $number = $Pin
    if ($number.StartsWith('v', [StringComparison]::Ordinal)) { $number = $number.Substring(1) }
    $match = [regex]::Match($number,
        '\A(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(alpha|beta|rc)\.(0|[1-9][0-9]*))?\z')
    if (-not $match.Success) {
        Write-Fatal "invalid version: $Pin (expected vX.Y.Z or vX.Y.Z-rc.N)"
    }
    $wheel = $match.Groups[1].Value + '.' + $match.Groups[2].Value + '.' + $match.Groups[3].Value
    if ($match.Groups[4].Success) {
        $marker = @{ alpha = 'a'; beta = 'b'; rc = 'rc' }[$match.Groups[4].Value]
        $wheel += $marker + $match.Groups[5].Value
    }
    return [pscustomobject]@{ Requirement = "pairmux==$wheel"; Expected = $number }
}

function Write-PairmuxCommand {
    param([string]$Requirement)
    Write-Host "  uv tool install --no-config --default-index $PypiIndex --no-sources --no-build"
    Write-Host "    --python "">=3.9"" --upgrade --reinstall --no-cache $Requirement"
}

function Invoke-PairmuxDownload {
    param([string]$Uri, [string]$OutFile)
    # Windows PowerShell 5.1 on an older .NET may not offer TLS 1.2 by default.
    $previousProtocol = [Net.ServicePointManager]::SecurityProtocol
    try {
        if ($PSVersionTable.PSEdition -ne 'Core') {
            [Net.ServicePointManager]::SecurityProtocol = $previousProtocol -bor [Net.SecurityProtocolType]::Tls12
        }
        $response = Invoke-WebRequest -Uri $Uri -OutFile $OutFile -UseBasicParsing -PassThru
    } finally {
        [Net.ServicePointManager]::SecurityProtocol = $previousProtocol
    }
    # Like curl --proto-redir '=https': a redirect must not leave HTTPS.
    $final = $null
    if ($response.BaseResponse -is [Net.HttpWebResponse]) {
        $final = $response.BaseResponse.ResponseUri
    } elseif ($null -ne $response.BaseResponse.RequestMessage) {
        $final = $response.BaseResponse.RequestMessage.RequestUri
    }
    if ($null -eq $final -or $final.Scheme -ne 'https') {
        throw "the download did not stay on HTTPS (final URL: $final)"
    }
    if ((Get-Item -LiteralPath $OutFile).Length -eq 0) { throw 'the download is empty' }
}

function Resolve-PairmuxUv {
    param([string]$UvHome, [string]$BinDir)
    $found = @(Get-Command -Name uv -CommandType Application -ErrorAction SilentlyContinue)
    if ($found.Count -gt 0) {
        $candidate = $found[0].Source
        if (-not [IO.Path]::IsPathRooted($candidate)) {
            Write-Fatal "uv was found through a relative PATH entry ($candidate)" `
                'remove relative entries from PATH, or install uv manually first'
        }
        return $candidate
    }
    $managed = Join-Path $UvHome 'uv.exe'
    if (Test-Path -LiteralPath $managed -PathType Leaf) { return $managed }
    foreach ($name in @('uv.exe', 'uvx.exe', 'uvw.exe')) {
        $existing = Join-Path $UvHome $name
        if (Test-Path -LiteralPath $existing) {
            Write-Fatal "refusing to overwrite $existing; install uv manually first"
        }
    }

    $work = Join-Path ([IO.Path]::GetTempPath()) ('pairmux-uv-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work | Out-Null
    $failure = $null
    try {
        $installer = Join-Path $work 'install-uv.ps1'
        Write-Step "uv is missing; bootstrapping from $UvInstallerUrl (not PyPI)"
        try {
            Invoke-PairmuxDownload -Uri $UvInstallerUrl -OutFile $installer
        } catch {
            $failure = "uv installer download failed; nothing executed ($($_.Exception.Message))"
        }
        if (-not $failure) {
            # Execute only the fully downloaded file, in a child process of this
            # same PowerShell edition. The execution-policy bypass is scoped to
            # that process. The caller's PATH and profiles stay untouched.
            $shellName = 'powershell.exe'
            if ($PSVersionTable.PSEdition -eq 'Core') { $shellName = 'pwsh.exe' }
            $bootstrap = Invoke-PairmuxClean -FilePath (Join-Path $PSHOME $shellName) -BinDir $BinDir `
                -ArgumentList @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $installer) `
                -Extra @{ UV_INSTALL_DIR = $UvHome; UV_NO_MODIFY_PATH = '1' }
            if ($bootstrap.ExitCode -ne 0) { $failure = 'uv bootstrap failed' }
        }
    } finally {
        Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
    }
    if ($failure) { Write-Fatal $failure }
    if (-not (Test-Path -LiteralPath $managed -PathType Leaf)) {
        Write-Fatal "uv bootstrap did not install $managed"
    }
    return $managed
}

# On Windows uv copies a tool's executable into the bin directory, so "is this
# uv's own entrypoint" means "byte-identical to the tool environment's copy".
function Test-PairmuxManagedCopy {
    param([string]$Path, [string]$Managed)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $false }
    if (-not (Test-Path -LiteralPath $Managed -PathType Leaf)) { return $false }
    $left = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash
    $right = (Get-FileHash -Algorithm SHA256 -LiteralPath $Managed).Hash
    return $left -ceq $right
}

# uv reinstalls remove the entrypoints recorded by the old receipt, even without
# --force. Inspect that public listing before allowing uv to touch those paths.
function Assert-PairmuxEntrypoint {
    param([string]$Uv, [string]$BinDir)
    $listedDir = Invoke-PairmuxClean -FilePath $Uv -ArgumentList @('tool', 'dir', '--no-config') -BinDir $BinDir -Capture
    $toolDir = ''
    if ($listedDir.ExitCode -eq 0 -and $listedDir.Lines.Count -eq 1) { $toolDir = [string]$listedDir.Lines[0] }
    if (-not $toolDir -or $toolDir -match '[\x00-\x1f\x7f]' -or -not [IO.Path]::IsPathRooted($toolDir)) {
        Write-Fatal "could not determine uv's tool directory"
    }
    $toolEnv = Join-Path $toolDir 'pairmux'
    $managed = Join-Path $toolEnv 'Scripts\pairmux.exe'
    if (Test-Path -LiteralPath $toolEnv) {
        if (-not (Test-Path -LiteralPath (Join-Path $toolEnv 'uv-receipt.toml') -PathType Leaf)) {
            Write-Fatal 'existing pairmux tool has no regular uv receipt; repair it manually first'
        }
        $listing = Invoke-PairmuxClean -FilePath $Uv -BinDir $BinDir -Capture `
            -ArgumentList @('tool', 'list', '--no-config', '--show-paths', '--color', 'never')
        if ($listing.ExitCode -ne 0) { Write-Fatal 'could not inspect the existing uv tool receipt' }
        $header = '\A[A-Za-z0-9_.-]+ v\S+ \(.+\)\z'
        $inPairmux = $false
        $count = 0
        foreach ($raw in $listing.Lines) {
            $line = [string]$raw
            if ($line -match '[\x00-\x1f\x7f]') {
                Write-Fatal 'existing uv tool listing contains an unsupported control character'
            }
            # uv prints names and paths without quoting. A second delimiter can
            # hide the actual recorded path inside an entrypoint name.
            if ($line.IndexOf(' (') -ne $line.LastIndexOf(' (')) {
                Write-Fatal 'existing uv tool listing has an ambiguous name/path delimiter'
            }
            if ($line.StartsWith('pairmux v', [StringComparison]::Ordinal)) {
                if ($line -cnotmatch $header -or -not $line.EndsWith(" ($toolEnv)", [StringComparison]::Ordinal)) {
                    Write-Fatal 'existing pairmux tool listing is malformed'
                }
                $inPairmux = $true
                continue
            }
            if ($line.StartsWith('- ', [StringComparison]::Ordinal)) {
                if (-not $inPairmux) { continue }
                $entry = [regex]::Match($line, '\A- pairmux(?:\.exe)? \((.+)\)\z')
                if (-not $entry.Success) {
                    Write-Fatal 'unexpected entrypoint in existing pairmux receipt; repair it manually first'
                }
                $recorded = $entry.Groups[1].Value
                if (-not [IO.Path]::IsPathRooted($recorded) -or
                    -not $recorded.EndsWith('\pairmux.exe', [StringComparison]::OrdinalIgnoreCase)) {
                    Write-Fatal 'existing receipt entrypoint is not a supported absolute path'
                }
                $count += 1
                if ((Test-Path -LiteralPath $recorded) -and -not (Test-PairmuxManagedCopy -Path $recorded -Managed $managed)) {
                    Write-Fatal "refusing to replace ${recorded}: it is not the uv-managed copy of pairmux.exe"
                }
                continue
            }
            if ($line -cnotmatch $header) {
                Write-Fatal 'uv tool listing is malformed; repair the existing receipt manually first'
            }
            $inPairmux = $false
        }
        if ($count -ne 1) {
            Write-Fatal 'existing pairmux receipt is invalid or has unexpected entrypoints; repair it manually first'
        }
    }
    $destination = Join-Path $BinDir 'pairmux.exe'
    if ((Test-Path -LiteralPath $destination) -and -not (Test-PairmuxManagedCopy -Path $destination -Managed $managed)) {
        Write-Fatal "refusing to overwrite ${destination}: choose another PAIRMUX_INSTALL_DIR or remove the old installation manually"
    }
}

function Write-PairmuxPathHint {
    param([string]$BinDir, [string]$Executable)
    $wanted = $BinDir.TrimEnd('\')
    $entries = @($env:PATH -split ';' | ForEach-Object { $_.Trim().TrimEnd('\') } | Where-Object { $_ })
    if ($entries -notcontains $wanted) {
        Write-Host "warning: $BinDir is not on your PATH; this installer does not edit it." -ForegroundColor Yellow
        Write-Note 'add it for your user (then open a new terminal), for example:'
        $quoted = $BinDir.Replace("'", "''")
        Write-Note ("[Environment]::SetEnvironmentVariable('Path', '" + $quoted +
            ";' + [Environment]::GetEnvironmentVariable('Path', 'User'), 'User')")
        Write-Note 'or let uv do it: uv tool update-shell'
    }
    $visible = @(Get-Command -Name pairmux -CommandType Application -ErrorAction SilentlyContinue)
    if ($visible.Count -gt 0 -and $visible[0].Source -ne $Executable) {
        Write-Host "warning: PATH currently selects $($visible[0].Source) instead of $Executable." -ForegroundColor Yellow
        Write-Note 'remove the old installation or put the uv tool-bin directory first'
    }
}

function Install-PairmuxNative {
    param([string]$Version, [string]$InstallDir)
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    $archName = $null
    if ($arch -eq 'AMD64') { $archName = 'x86-64' }
    if ($arch -eq 'ARM64') { $archName = 'ARM64' }
    if (-not $archName) {
        Write-Fatal "unsupported Windows architecture: $arch" 'pairmux ships Windows wheels for x86-64 and ARM64'
    }
    $pin = Resolve-PairmuxRequirement $Version

    $profileDir = $env:USERPROFILE
    if (-not $profileDir) { $profileDir = [Environment]::GetFolderPath('UserProfile') }
    if (-not $profileDir -or -not [IO.Path]::IsPathRooted($profileDir)) {
        Write-Fatal 'could not determine your user profile directory' 'set USERPROFILE to a user-writable directory'
    }
    $binDir = $InstallDir
    if (-not $binDir) { $binDir = Join-Path $profileDir '.local\bin' }
    if ("$binDir$profileDir" -match '[\x00-\x1f\x7f]') {
        Write-Fatal 'the profile and install paths must not contain control characters'
    }
    # Relative paths follow this PowerShell session's file-system location, not
    # the process directory .NET would otherwise use.
    if (-not [IO.Path]::IsPathRooted($binDir)) {
        $binDir = Join-Path (Get-Location -PSProvider FileSystem).ProviderPath $binDir
    }
    try {
        $binDir = [IO.Path]::GetFullPath($binDir)
    } catch {
        Write-Fatal "invalid install directory '$binDir': $($_.Exception.Message)"
    }
    $uvHome = Join-Path $profileDir '.local\bin'
    $executable = Join-Path $binDir 'pairmux.exe'
    $arguments = @('tool', 'install', '--no-config', '--default-index', $PypiIndex, '--no-sources',
        '--no-build', '--python', '>=3.9', '--upgrade', '--reinstall', '--no-cache', $pin.Requirement)

    if ($DryRun) {
        Write-Host 'pairmux install.ps1 - dry run (no network, nothing written)'
        Write-Host ''
        Write-Host "  target:       native Windows ($archName)"
        Write-Host "  package:      $PypiProject"
        Write-Host "  index:        $PypiIndex (exclusive)"
        Write-Host "  requirement:  $($pin.Requirement)"
        Write-Host "  tool bin:     $binDir"
        Write-Host "  uv bootstrap: $UvInstallerUrl (only if uv is missing)"
        Write-Host ''
        Write-PairmuxCommand $pin.Requirement
        return
    }

    Write-Step "pairmux package: $PypiProject"
    Write-Step "package index: $PypiIndex (exclusive; inherited UV_* and config ignored)"
    Write-Step "uv may download Python >=3.9 from Astral's upstream if none is available."
    $uv = Resolve-PairmuxUv -UvHome $uvHome -BinDir $binDir
    Assert-PairmuxEntrypoint -Uv $uv -BinDir $binDir
    Write-Step 'installing the PyPI platform wheel with uv (no source builds, no administrator rights)'
    Write-PairmuxCommand $pin.Requirement
    $installed = Invoke-PairmuxClean -FilePath $uv -ArgumentList $arguments -BinDir $binDir
    if ($installed.ExitCode -ne 0) {
        Write-Fatal "PyPI installation failed; check uv's error above (upgrade uv if its flags are unsupported)" `
            "releases without a Windows wheel cannot install natively; for those set `$env:PAIRMUX_INSTALL_TARGET = 'wsl'"
    }
    if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) {
        Write-Fatal "uv finished, but $executable does not exist"
    }
    $reported = @(& $executable version)
    if ($LASTEXITCODE -ne 0 -or $reported.Count -ne 1 -or -not $reported[0]) {
        Write-Fatal 'the uv-installed executable did not report a version'
    }
    $installedVersion = [string]$reported[0]
    if ($pin.Expected -and $installedVersion -cne $pin.Expected) {
        Write-Fatal "installed binary reports $installedVersion; expected $($pin.Expected)"
    }
    Write-Step "pairmux $installedVersion is ready at $executable"
    Write-PairmuxPathHint -BinDir $binDir -Executable $executable
    Write-Host ''
    Write-Host 'Quickstart:'
    Write-Host '  pairmux new --name build'
    Write-Host '  pairmux run build "echo hello from pairmux"'
    Write-Host '  pairmux ls'
    Write-Host ''
    Write-Host "Docs: $DocsUrl"
}

# --- WSL target ----------------------------------------------------------------

# The previous default, unchanged: the body below keeps its original indentation
# so the WSL discovery, argument quoting and base64 transport stay byte-for-byte
# what scripts/test-install-windows.ps1 and the real WSL1 acceptance verify.
function Install-PairmuxWsl {
param([string]$Version, [string]$InstallDir)
if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
    Write-Fatal "the WSL install target was selected, but WSL was not found" `
        "install it with:  wsl --install     (then reopen this terminal and re-run), or install natively without PAIRMUX_INSTALL_TARGET"
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

    # Download completely before executing: neither an unavailable downloader
    # nor a failed transfer should look like a successful empty shell script.
    $inner = @'
set -euo pipefail
command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || { printf 'need curl or wget in WSL\n' >&2; exit 1; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
if command -v curl >/dev/null 2>&1; then
    curl -q -fsSL --proto '=https' --proto-redir '=https' "$1" -o "$work/install.sh"
else
    WGETRC=/dev/null wget --https-only -qO "$work/install.sh" "$1"
fi
shift
bash "$work/install.sh" "$@"
'@
    $inner = $inner.Replace("`r`n", "`n")
    $invoke = 'bash -c ' + (ConvertTo-ShellLiteral $inner) + ' pairmux-installer ' + (ConvertTo-ShellLiteral $InstallUrl)
    if ($Version) {
        $invoke += ' --version ' + (ConvertTo-ShellLiteral $Version)
    }
    if ($InstallDir) {
        $invoke = 'PAIRMUX_INSTALL_DIR=' + (ConvertTo-ShellLiteral $InstallDir) + ' ' + $invoke
    }

    if ($DryRun) {
        Write-Step 'Dry run: no download or installation'
        Write-Note $invoke
        exit 0
    }

    # WSL --exec bypasses its default shell. Base64 keeps the command argument
    # free of embedded double quotes, which PowerShell 5.1's native binder does
    # not escape reliably. Decode fully to a temporary file before executing.
    $payload = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($invoke))
    $transport = 'set -euo pipefail; work=$(mktemp -d /tmp/pairmux-wsl.XXXXXX); trap ''rm -rf $work'' EXIT; printf %s ' + $payload + ' | base64 -d >$work/invoke.sh; bash $work/invoke.sh'
    & wsl.exe @wslArgs --exec bash -c $transport
    if ($LASTEXITCODE -ne 0) {
        Write-Fatal "the installer failed inside WSL (exit code $LASTEXITCODE)" `
            "open WSL and download $InstallUrl to inspect the installer and run it with bash"
    }

    # tmux is optional: without it pairmux uses its built-in terminal backend.
    # Say which backend this distribution will get (and what v0.6.0 and older,
    # which predate the built-in backend, still need).
    & wsl.exe @wslArgs --exec sh -c 'command -v tmux >/dev/null 2>&1' 2>$null
    if ($LASTEXITCODE -ne 0) {
        Write-Host 'note: tmux is not installed in that distribution; pairmux will use its built-in terminal backend (tmux is optional)'
        Write-Note 'for the tmux backend (required by pairmux v0.6.0 and older), inside WSL run:  sudo apt update && sudo apt install -y tmux'
    }

    Write-Step 'Done. pairmux lives inside WSL, so run it from there:'
    Write-Note 'wsl -- pairmux version'
} finally {
    $env:WSL_UTF8 = $previousWslUtf8
}
}

# The version pin and install directory are passed explicitly rather than read
# from script scope, so each target's inputs are visible at its call.
if ($InstallTarget -eq 'wsl') {
    Install-PairmuxWsl -Version $Version -InstallDir $InstallDir
} else {
    Install-PairmuxNative -Version $Version -InstallDir $InstallDir
}
