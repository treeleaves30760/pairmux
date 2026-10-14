#Requires -Version 7.0
<# Real native-Windows acceptance of a published release on an ephemeral
   windows-2025 runner: public win_amd64 wheel through uv into an isolated tool
   directory, byte identity with the checksummed zip, healthy doctor, and a live
   built-in (ConPTY) terminal round trip. No mock fallback, no local builds. #>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$LogDir,
    [Parameter(Mandatory)][string]$ProvenancePath,
    [Parameter(Mandatory)][string]$Uv,
    [Parameter(Mandatory)][string]$Python
)
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
if (-not $IsWindows) { throw 'native Windows acceptance requires the Windows hosted runner' }
if ($Version -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
    throw 'Version must be canonical stable vX.Y.Z'
}
$expected = $Version.Substring(1)
foreach ($tool in @($Uv, $Python)) {
    if (-not [IO.Path]::IsPathRooted($tool) -or -not (Test-Path -LiteralPath $tool -PathType Leaf)) {
        throw "tool path must be an absolute existing file: $tool"
    }
}
$repo = Split-Path -Parent $PSScriptRoot
$LogDir = [IO.Path]::GetFullPath($LogDir)
New-Item -ItemType Directory -Path $LogDir -Force | Out-Null
$provenance = Get-Content -LiteralPath $ProvenancePath -Raw | ConvertFrom-Json
if ($provenance.tag -cne $Version -or $provenance.selected.target -cne 'windows-amd64' -or
    $provenance.shape -cne 'macos-linux-windows') {
    throw 'selected public Windows release provenance does not match'
}
if ([string]$provenance.selected.binary_sha256 -cnotmatch '^[a-f0-9]{64}$') { throw 'missing selected binary SHA256' }

$nonce = [Guid]::NewGuid().ToString('N')
$work = Join-Path $env:RUNNER_TEMP "pmx-win-$nonce"
$tools = Join-Path $work 'tools'
$bin = Join-Path $work 'bin'
foreach ($directory in @($work, $tools, $bin, (Join-Path $work 'cache'), (Join-Path $work 'state'),
        (Join-Path $work 'run'), (Join-Path $work 'cwd'))) {
    New-Item -ItemType Directory -Path $directory | Out-Null
}
$exe = Join-Path $bin 'pairmux.exe'
$name = "acceptance-$($nonce.Substring(0, 12))"
$marker = "pairmux-public-runtime-$nonce"

# Runs a native command with extra environment for that child only, logs its
# output, and returns the text and exit code. JSON envelopes are read from
# stdout alone (stderr stays in the job log); tool output is logged combined.
# Inherited UV_* settings are removed while uv runs, like the installers do.
function Invoke-Logged {
    param([string]$FilePath, [string[]]$Arguments, [string]$LogName, [hashtable]$Environment = @{},
        [switch]$ScrubUv, [switch]$StdoutOnly)
    $saved = @{}
    $names = @($Environment.Keys)
    if ($ScrubUv) {
        $names += @(Get-ChildItem -Path Env: | Where-Object { $_.Name -match '^UV_' } | ForEach-Object { $_.Name })
    }
    foreach ($key in $names) { $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
    try {
        foreach ($key in $names) { [Environment]::SetEnvironmentVariable($key, $null, 'Process') }
        foreach ($key in @($Environment.Keys)) {
            [Environment]::SetEnvironmentVariable($key, [string]$Environment[$key], 'Process')
        }
        if ($StdoutOnly) {
            $output = @(& $FilePath @Arguments | ForEach-Object { "$_" })
        } else {
            $output = @(& $FilePath @Arguments 2>&1 | ForEach-Object { "$_" })
        }
        $code = $LASTEXITCODE
    } finally {
        foreach ($key in @($saved.Keys)) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
    }
    $text = $output -join "`n"
    [IO.File]::WriteAllText((Join-Path $LogDir $LogName), $text + "`n", [Text.UTF8Encoding]::new($false))
    return [pscustomobject]@{ Code = $code; Text = $text }
}

# The same three-field rule as test-release-runtime.sh: a pairmux.v1 envelope
# that is ok, has the expected status and no error, for the expected terminal.
function Assert-Envelope {
    param([pscustomobject]$Result, [string]$Status, [string]$Terminal, [string]$LogName)
    if ($Result.Code -ne 0) { throw "$LogName exited $($Result.Code)" }
    try { $reply = $Result.Text | ConvertFrom-Json } catch { throw "$LogName is not a single JSON envelope" }
    if ($reply.schema -cne 'pairmux.v1' -or $reply.ok -isnot [bool] -or -not $reply.ok -or
        $reply.status -cne $Status -or $null -ne $reply.error) {
        throw "$LogName is not a successful pairmux.v1 '$Status' envelope"
    }
    if ($Terminal -and $reply.terminal -cne $Terminal) { throw "$LogName names the wrong terminal" }
    return $reply
}

# Isolated pairmux state, built-in server run directory and endpoint; the
# built-in backend is the only one on Windows and is named explicitly anyway.
$runtime = @{
    PAIRMUX_BACKEND = 'builtin'; PAIRMUX_SOCKET = "acceptance-$nonce"
    PAIRMUX_STATE_DIR = (Join-Path $work 'state'); PAIRMUX_TMPDIR = (Join-Path $work 'run')
}
try {
    $uvVersion = Invoke-Logged -FilePath $Uv -Arguments @('--version') -LogName 'uv-version.txt'
    if ($uvVersion.Code -ne 0 -or $uvVersion.Text -cnotmatch '^uv 0\.11\.16(\s|$)') { throw 'uv 0.11.16 is required' }
    @("tag=$Version", "uv=$Uv", "python=$Python", 'index=https://pypi.org/simple',
        "tool_dir=$tools", "tool_bin=$bin", "socket=$($runtime.PAIRMUX_SOCKET)") |
        Set-Content -LiteralPath (Join-Path $LogDir 'windows-provenance.txt')

    # Public PyPI only, wheels only, isolated tool/bin/cache, no uv config.
    $install = Invoke-Logged -FilePath $Uv -LogName 'uv-install.txt' -ScrubUv -Arguments @(
        'tool', 'install', '--no-config', '--default-index', 'https://pypi.org/simple', '--no-sources',
        '--no-build', '--no-python-downloads', '--python', $Python, '--no-cache', "pairmux==$expected") `
        -Environment @{ UV_TOOL_DIR = $tools; UV_TOOL_BIN_DIR = $bin; UV_CACHE_DIR = (Join-Path $work 'cache') }
    if ($install.Code -ne 0) { throw "uv tool install failed ($($install.Code)); see uv-install.txt" }
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw "uv did not install $exe" }

    # The trusted helper compares the installed bytes with the checksummed zip's
    # pairmux.exe and requires the exact version.
    $identity = Invoke-Logged -FilePath $Python -LogName 'installed-identity-check.txt' -Arguments @(
        '-I', (Join-Path $repo 'scripts/release-acceptance.py'), '--installed', $exe,
        '--installed-version', $expected, '--provenance', $ProvenancePath,
        '--out-dir', (Join-Path $LogDir 'installed-identity'))
    if ($identity.Code -ne 0) { throw 'installed pairmux.exe differs from the checksummed public release' }

    $doctor = Invoke-Logged -FilePath $exe -Arguments @('--json', 'doctor') -LogName 'doctor.json' -Environment $runtime -StdoutOnly
    Assert-Envelope -Result $doctor -Status 'ok' -LogName 'doctor.json' | Out-Null
    $created = Invoke-Logged -FilePath $exe -LogName 'new.json' -Environment $runtime -StdoutOnly `
        -Arguments @('--json', 'new', '--name', $name, '--cwd', (Join-Path $work 'cwd'))
    Assert-Envelope -Result $created -Status 'created' -Terminal $name -LogName 'new.json' | Out-Null
    $ran = Invoke-Logged -FilePath $exe -LogName 'run.json' -Environment $runtime -StdoutOnly `
        -Arguments @('--json', 'run', $name, "echo $marker", '--timeout', '30s')
    $reply = Assert-Envelope -Result $ran -Status 'done' -Terminal $name -LogName 'run.json'
    if (($reply.exit_code -isnot [long] -and $reply.exit_code -isnot [int]) -or $reply.exit_code -ne 0) {
        throw 'run did not report exit code 0'
    }
    $lines = @([string]$reply.output -split '\r?\n' | Where-Object { $_.TrimEnd() -ceq $marker })
    if ($lines.Count -ne 1) { throw 'run output does not contain the unique marker line exactly once' }
    $killed = Invoke-Logged -FilePath $exe -Arguments @('--json', 'kill', $name) -LogName 'kill.json' -Environment $runtime -StdoutOnly
    Assert-Envelope -Result $killed -Status 'killed' -Terminal $name -LogName 'kill.json' | Out-Null
    Write-Host "native Windows acceptance passed for public pairmux $expected (uv wheel, doctor, built-in round trip)"
} finally {
    # Owned cleanup only: this test's endpoint, then its temporary tree. The
    # built-in server exits by itself once it has no terminals.
    if (Test-Path -LiteralPath $exe -PathType Leaf) {
        Invoke-Logged -FilePath $exe -Arguments @('--json', 'kill', '--all') -LogName 'cleanup.json' -Environment $runtime -StdoutOnly |
            Out-Null
    }
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
