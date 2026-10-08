#Requires -Version 7.0
<# Real WSL1 acceptance on an ephemeral windows-2025 runner. No mock fallback. #>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$LogDir,
    [Parameter(Mandatory)][string]$ProvenancePath
)
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
if (-not $IsWindows) { throw 'WSL acceptance requires the Windows hosted runner' }
if ($Version -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
    throw 'Version must be canonical stable vX.Y.Z'
}
$wsl = (Get-Command wsl.exe -ErrorAction Stop).Source
$repo = Split-Path -Parent $PSScriptRoot
$LogDir = [IO.Path]::GetFullPath($LogDir)
New-Item -ItemType Directory -Path $LogDir -Force | Out-Null
$provenance = Get-Content -LiteralPath $ProvenancePath -Raw | ConvertFrom-Json
if ($provenance.tag -cne $Version -or $provenance.selected.target -cne 'linux-amd64') {
    throw 'WSL selected public release provenance does not match'
}
$binaryHash = [string]$provenance.selected.binary_sha256
if ($binaryHash -cnotmatch '^[a-f0-9]{64}$') { throw 'Missing selected binary SHA256' }
$base = 'https://cloud-images.ubuntu.com/wsl/releases/noble/current'
$rootfsName = 'ubuntu-noble-wsl-amd64-wsl.rootfs.tar.gz'
$rootfsUrl = "$base/$rootfsName"
$sumUrl = "$base/SHA256SUMS"
$nonce = [Guid]::NewGuid().ToString('N')
$name = "Pairmux-Acceptance-$nonce"
$work = Join-Path $env:RUNNER_TEMP "pairmux-wsl-$nonce"
$trustedWindows = Join-Path $work 'trusted-scripts'
$rootfsDir = Join-Path $work 'rootfs-download'
$sumDir = Join-Path $work 'checksum-download'
$distributionDir = Join-Path $work 'owned-distribution'
foreach ($directory in @($work, $trustedWindows, $rootfsDir, $sumDir, $distributionDir)) {
    New-Item -ItemType Directory -Path $directory | Out-Null
}
$importAttempted = $false
$linuxLogs = $null
$oldUtf8 = $env:WSL_UTF8
$env:WSL_UTF8 = '1'

function Invoke-WslChecked {
    param([string[]]$Arguments, [string]$LogName)
    $output = @(& $wsl @Arguments 2>&1 | ForEach-Object { "$_".Replace([string][char]0, '') })
    $code = $LASTEXITCODE
    $text = $output -join "`n"
    if ($LogName) {
        [IO.File]::WriteAllText((Join-Path $LogDir $LogName), $text + "`n", [Text.UTF8Encoding]::new($false))
    }
    if ($code -ne 0) { throw "WSL command failed ($code); see $LogName. Platform failures are not skipped." }
    return $text.Trim()
}
function Write-TrustedShell {
    param([string]$FileName, [string]$Source)
    $path = Join-Path $trustedWindows $FileName
    [IO.File]::WriteAllText($path, $Source.Replace("`r`n", "`n") + "`n", [Text.UTF8Encoding]::new($false))
    return $path
}

try {
    $feature = Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Windows-Subsystem-Linux
    $feature | Format-List FeatureName, State | Out-String | Set-Content (Join-Path $LogDir 'wsl-feature.txt')
    if ($feature.State -ne 'Enabled') {
        $enabled = Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Windows-Subsystem-Linux -All -NoRestart
        $enabled | Format-List | Out-String | Add-Content (Join-Path $LogDir 'wsl-feature.txt')
        if ($enabled.RestartNeeded) { throw 'WSL1 feature needs a reboot on this runner; cannot claim WSL acceptance' }
    }
    Invoke-WslChecked -Arguments @('--status') -LogName 'wsl-status.txt' | Out-Null
    $before = Invoke-WslChecked -Arguments @('--list', '--quiet') -LogName 'wsl-list-before.txt'
    if (@($before -split "`n" | ForEach-Object { $_.Trim() }) -contains $name) {
        throw 'Unique acceptance distribution unexpectedly exists; refusing to touch it'
    }
    # Data and scripts are never co-located, and downloads are fully complete
    # before import. Verify the official Canonical SHA256SUMS entry exactly.
    $sumPath = Join-Path $sumDir 'SHA256SUMS'
    $rootfsPath = Join-Path $rootfsDir $rootfsName
    Invoke-WebRequest -Uri $sumUrl -OutFile $sumPath
    $pattern = '^([a-f0-9]{64})\s+\*?' + [regex]::Escape($rootfsName) + '$'
    $rows = @(Get-Content -LiteralPath $sumPath | Where-Object { $_ -cmatch $pattern })
    if ($rows.Count -ne 1) { throw 'No unique rootfs entry in Canonical SHA256SUMS' }
    $expectedHash = [regex]::Match($rows[0], $pattern).Groups[1].Value
    Invoke-WebRequest -Uri $rootfsUrl -OutFile $rootfsPath
    $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $rootfsPath).Hash.ToLowerInvariant()
    if ($actualHash -cne $expectedHash) { throw 'Canonical rootfs SHA256 mismatch' }
    @(
        "distribution=$name", 'requested_wsl_version=1', "rootfs=$rootfsUrl", "checksums=$sumUrl",
        "rootfs_sha256=$actualHash", "pairmux_tag=$Version", "pairmux_binary_sha256=$binaryHash",
        'installer=trusted-checkout/install.ps1', 'bash_endpoint=https://pairmux.treeleaves30760.com/install.sh',
        "install_ps1_sha256=$((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $repo 'install.ps1')).Hash.ToLowerInvariant())"
    ) | Set-Content (Join-Path $LogDir 'wsl-provenance.txt')
    $importAttempted = $true
    Invoke-WslChecked -Arguments @('--import', $name, $distributionDir, $rootfsPath, '--version', '1') -LogName 'wsl-import.txt' | Out-Null
    # checkout on Windows may use CRLF; stage a normalized trusted copy before
    # invoking Linux Bash. It is separate from both rootfs/checksum downloads.
    $runtimeWindows = Write-TrustedShell -FileName 'test-release-runtime.sh' -Source (Get-Content -LiteralPath (Join-Path $PSScriptRoot 'test-release-runtime.sh') -Raw)
    $runtimeLinux = Invoke-WslChecked -Arguments @('--distribution', $name, '--user', 'root', '--exec', 'wslpath', '-a', '-u', $runtimeWindows)
    $linuxLogs = Invoke-WslChecked -Arguments @('--distribution', $name, '--user', 'root', '--exec', 'wslpath', '-a', '-u', $LogDir)
    $trustedLinux = "/opt/pairmux-acceptance-$nonce"
    $setup = Write-TrustedShell -FileName 'setup.sh' -Source @'
set -euo pipefail
cd /
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl tmux python3
useradd --create-home --shell /bin/bash pairmux
printf '[user]\ndefault=pairmux\n' > /etc/wsl.conf
mkdir "$2"
cp -- "$1" "$2/test-release-runtime.sh"
chmod 0555 "$2/test-release-runtime.sh"
install -d -o pairmux -g pairmux /home/pairmux/acceptance-logs
test ! -e /home/pairmux/.local/bin/pairmux
'@
    $setupLinux = Invoke-WslChecked -Arguments @('--distribution', $name, '--user', 'root', '--exec', 'wslpath', '-a', '-u', $setup)
    Invoke-WslChecked -Arguments @('--distribution', $name, '--user', 'root', '--cd', '/', '--exec', 'bash', $setupLinux, $runtimeLinux, $trustedLinux) -LogName 'wsl-prerequisites.txt' | Out-Null
    Invoke-WslChecked -Arguments @('--terminate', $name) -LogName 'wsl-terminate-for-default-user.txt' | Out-Null
    $defaultUser = Invoke-WslChecked -Arguments @('--distribution', $name, '--exec', 'id', '-un') -LogName 'wsl-default-user.txt'
    if ($defaultUser -cne 'pairmux') { throw 'wsl.conf did not select a non-root default user; production installer would run as root' }
    $listing = Invoke-WslChecked -Arguments @('--list', '--verbose') -LogName 'wsl-list-proof.txt'
    if ($listing -notmatch ('(?m)^\s*\*?\s*' + [regex]::Escape($name) + '\s+\S+\s+1\s*$')) {
        throw 'Owned distribution is not demonstrably WSL1; no WSL2 fallback is allowed'
    }

    # The production script can call exit. Invoke a child Windows PowerShell so
    # this test's finally always runs, even on installer failures.
    $oldDryRun = $env:PAIRMUX_DRY_RUN
    $oldInstallDir = $env:PAIRMUX_INSTALL_DIR
    try {
        Remove-Item Env:PAIRMUX_DRY_RUN -ErrorAction SilentlyContinue
        Remove-Item Env:PAIRMUX_INSTALL_DIR -ErrorAction SilentlyContinue
        & powershell.exe -NoProfile -NonInteractive -File (Join-Path $repo 'install.ps1') -Distribution $name -Version $Version 2>&1 |
            Tee-Object -FilePath (Join-Path $LogDir 'windows-production-install.txt')
        if ($LASTEXITCODE -ne 0) { throw "Production install.ps1 failed ($LASTEXITCODE)" }
    } finally {
        $env:PAIRMUX_DRY_RUN = $oldDryRun
        $env:PAIRMUX_INSTALL_DIR = $oldInstallDir
    }
    $probe = Write-TrustedShell -FileName 'probe.sh' -Source @'
set -euo pipefail
cd "$1"
test "$(id -un)" = pairmux
test "$(id -u)" -ne 0
uname -a > "$HOME/acceptance-logs/uname.txt"
case "$(uname -r)" in *[Mm]icrosoft*) ;; *) printf 'not a Microsoft WSL kernel\n' >&2; exit 1 ;; esac
case "$(uname -r)" in *WSL2*|*wsl2*) printf 'WSL2 is not the requested WSL1 proof\n' >&2; exit 1 ;; esac
python3 -I -c 'import hashlib,sys; sys.exit(0 if hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest() == sys.argv[2] else "installed WSL binary does not match public release")' "$HOME/.local/bin/pairmux" "$3"
"$HOME/.local/bin/uv" --version > "$HOME/acceptance-logs/uv-version.txt"
bash "$1/test-release-runtime.sh" "$HOME/.local/bin/pairmux" "$2" "$HOME/acceptance-logs/runtime"
'@
    $probeLinux = Invoke-WslChecked -Arguments @('--distribution', $name, '--exec', 'wslpath', '-a', '-u', $probe)
    Invoke-WslChecked -Arguments @('--distribution', $name, '--cd', $trustedLinux, '--exec', 'bash', $probeLinux, $trustedLinux, $Version.Substring(1), $binaryHash) -LogName 'wsl-runtime.txt' | Out-Null
    Write-Host 'Real WSL1 production installer and Linux runtime acceptance passed'
} finally {
    try {
        if ($importAttempted) {
            # Only unregister the freshly generated name after confirming it exists.
            # Neither the default nor any pre-existing distribution is changed.
            $existing = @(& $wsl --list --quiet 2>$null | ForEach-Object { "$_".Replace([string][char]0, '').Trim() })
            if ($LASTEXITCODE -ne 0) { throw 'Cannot enumerate WSL distributions for owned cleanup' }
            if ($existing -contains $name) {
                try {
                    if ($linuxLogs) {
                        Invoke-WslChecked -Arguments @('--distribution', $name, '--user', 'root', '--exec', 'cp', '-R', '/home/pairmux/acceptance-logs/.', $linuxLogs) -LogName 'wsl-log-collection.txt' | Out-Null
                    }
                } finally {
                    Invoke-WslChecked -Arguments @('--unregister', $name) -LogName 'wsl-unregister-owned.txt' | Out-Null
                }
            }
        }
    } finally {
        $env:WSL_UTF8 = $oldUtf8
        Remove-Item -LiteralPath $work -Recurse -Force
    }
}
