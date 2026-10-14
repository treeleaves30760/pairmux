# Offline tests for install.ps1 under Windows PowerShell's legacy native
# argument binder, which differs from pwsh on Unix. Both targets run against
# compiled stubs: an argv-capturing wsl.exe for the opt-in WSL target, and a uv.exe
# that records argv/environment and fakes a uv tool installation for the default
# native target. Nothing is downloaded and nothing is installed outside $work.
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$installer = Join-Path $root 'install.ps1'
$work = Join-Path ([IO.Path]::GetTempPath()) ('pairmux-wsl-test-' + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
$work = [IO.Path]::GetFullPath($work)
$previousPath = $env:PATH
$previousProfile = $env:USERPROFILE
$controlled = @(
    'PAIRMUX_WSL_TEST_LOG', 'PAIRMUX_WSL_TEST_MODE', 'PAIRMUX_UV_TEST_LOG', 'PAIRMUX_UV_TEST_TOOLS',
    'PAIRMUX_UV_TEST_PAIRMUX', 'PAIRMUX_UV_TEST_MODE', 'PAIRMUX_FAKE_VERSION', 'PAIRMUX_VERSION',
    'PAIRMUX_INSTALL_DIR', 'PAIRMUX_WSL_DISTRO', 'PAIRMUX_INSTALL_TARGET', 'PAIRMUX_DRY_RUN')
$previousControlled = @{}
foreach ($name in $controlled) {
    $previousControlled[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
    [Environment]::SetEnvironmentVariable($name, $null, 'Process')
}

# Runs install.ps1 as a script file in a child Windows PowerShell, with extra
# environment variables for that child only. Returns its exit code and stdout.
function Invoke-Installer {
    param([string[]]$Arguments = @(), [hashtable]$Environment = @{})
    $saved = @{}
    foreach ($name in @($Environment.Keys)) {
        $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $Environment[$name], 'Process')
    }
    try {
        $text = & powershell.exe -NoProfile -NonInteractive -File $installer @Arguments | Out-String
        $code = $LASTEXITCODE
    } finally {
        foreach ($name in @($saved.Keys)) {
            [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process')
        }
    }
    Write-Host $text
    return [pscustomobject]@{ ExitCode = $code; Output = $text }
}

# Decoded uv stub records: kind ('argv' or 'env') and the recorded text.
function Get-UvRecord {
    if (-not (Test-Path -LiteralPath $env:PAIRMUX_UV_TEST_LOG)) { return @() }
    return @(Get-Content -LiteralPath $env:PAIRMUX_UV_TEST_LOG | ForEach-Object {
        $kind, $payload = $_.Split(':', 2)
        [pscustomobject]@{ Kind = $kind; Text = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($payload)) }
    })
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

try {
    $stub = @'
using System;
using System.IO;
using System.Text;
public static class FakeWsl {
    public static int Main(string[] args) {
        if (args.Length == 2 && args[0] == "--list" && args[1] == "--quiet") {
            Console.WriteLine("Ubuntu"); return 0;
        }
        if (Array.IndexOf(args, "bash") >= 0) {
            string[] encoded = Array.ConvertAll(args, s => Convert.ToBase64String(Encoding.UTF8.GetBytes(s)));
            File.AppendAllText(Environment.GetEnvironmentVariable("PAIRMUX_WSL_TEST_LOG"), String.Join("|", encoded) + "\n");
            return Environment.GetEnvironmentVariable("PAIRMUX_WSL_TEST_MODE") == "fail" ? 42 : 0;
        }
        return 0;
    }
}
'@
    Add-Type -TypeDefinition $stub -OutputAssembly (Join-Path $work 'wsl.exe') -OutputType ConsoleApplication

    # --- WSL target (opt-in): the original argv/transport assertions ---------
    $env:PATH = $work + [IO.Path]::PathSeparator + $env:PATH
    $env:PAIRMUX_WSL_TEST_LOG = Join-Path $work 'argv.txt'
    $installDir = '/tmp/bin with spaces and apostrophe''s (test)'
    & powershell.exe -NoProfile -File $installer `
        -Target wsl -Version v0.5.3 -InstallDir $installDir -Distribution Ubuntu
    if ($LASTEXITCODE -ne 0) { throw 'Windows installer stub invocation failed' }
    $argv = (Get-Content $env:PAIRMUX_WSL_TEST_LOG -Raw).Trim().Split('|') | ForEach-Object {
        [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($_))
    }
    if ($argv.Count -ne 6 -or $argv[0] -ne '--distribution' -or $argv[1] -ne 'Ubuntu' -or
        $argv[2] -ne '--exec' -or $argv[3] -ne 'bash' -or $argv[4] -ne '-c') {
        throw ('Incorrect native WSL argv: ' + ($argv -join ', '))
    }
    $transport = $argv[5]
    if ($transport.Contains('"') -or $transport.Contains("`r")) { throw 'Unsafe legacy native transport' }
    if ($transport -notmatch 'printf %s ([A-Za-z0-9+/=]+) \| base64 -d >\$work/invoke.sh; bash \$work/invoke.sh$') {
        throw 'Transport did not decode fully before execution'
    }
    $invoke = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Matches[1]))
    if ($invoke.Contains("`r") -or -not $invoke.Contains('https://pairmux.treeleaves30760.com/install.sh') -or
        -not $invoke.Contains("--version 'v0.5.3'") -or -not $invoke.Contains('bash "$work/install.sh" "$@"')) {
        throw 'Decoded installer delegate did not preserve its arguments'
    }
    $quotedDir = "'" + $installDir.Replace("'", "'" + '"' + "'" + '"' + "'") + "'"
    if (-not $invoke.StartsWith('PAIRMUX_INSTALL_DIR=' + $quotedDir + ' bash -c ')) {
        throw 'Destination shell quoting was not preserved'
    }
    # The environment variable selects WSL too, and a WSL failure is reported.
    $result = Invoke-Installer -Arguments @('-Distribution', 'Ubuntu') `
        -Environment @{ PAIRMUX_INSTALL_TARGET = 'WSL'; PAIRMUX_WSL_TEST_MODE = 'fail' }
    Assert-True ($result.ExitCode -ne 0) 'WSL failure was ignored'
    Assert-True ((Get-Content $env:PAIRMUX_WSL_TEST_LOG).Count -eq 2) 'PAIRMUX_INSTALL_TARGET=WSL did not reach WSL'

    # A distribution alone never selects WSL, and unknown targets are refused.
    $wslCalls = (Get-Content $env:PAIRMUX_WSL_TEST_LOG).Count
    $result = Invoke-Installer -Arguments @('-Distribution', 'Ubuntu', '-DryRun')
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains('PAIRMUX_INSTALL_TARGET')) `
        'a distribution without the WSL target was not refused'
    $result = Invoke-Installer -Environment @{ PAIRMUX_INSTALL_TARGET = 'linux'; PAIRMUX_DRY_RUN = '1' }
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains("unknown install target 'linux'")) `
        'an unknown install target was not refused'
    Assert-True ((Get-Content $env:PAIRMUX_WSL_TEST_LOG).Count -eq $wslCalls) 'a refused run reached WSL'

    # --- native target (default): uv stub ------------------------------------
    $uvStub = @'
using System;
using System.Collections;
using System.Collections.Generic;
using System.IO;
using System.Text;
public static class FakeUv {
    static string Env(string name) { return Environment.GetEnvironmentVariable(name); }
    static void Record(string kind, string text) {
        File.AppendAllText(Env("PAIRMUX_UV_TEST_LOG"),
            kind + ":" + Convert.ToBase64String(Encoding.UTF8.GetBytes(text)) + "\n");
    }
    public static int Main(string[] args) {
        string tools = Env("PAIRMUX_UV_TEST_TOOLS");
        string toolEnv = Path.Combine(tools, "pairmux");
        string joined = String.Join(" ", args);
        Record("argv", String.Join("\n", args));
        if (joined == "tool dir --no-config") {
            Console.WriteLine(tools);
            return 0;
        }
        if (joined == "tool list --no-config --show-paths --color never") {
            if (Directory.Exists(toolEnv)) {
                Console.WriteLine("pairmux v0.7.0 (" + toolEnv + ")");
                Console.WriteLine("- pairmux.exe (" + File.ReadAllText(Path.Combine(toolEnv, "entrypoint.txt")) + ")");
            }
            Console.WriteLine("other-tool v1.0.0 (" + Path.Combine(tools, "other-tool") + ")");
            Console.WriteLine("- other.exe (" + Path.Combine(Env("UV_TOOL_BIN_DIR"), "other.exe") + ")");
            return 0;
        }
        if (args.Length > 2 && args[0] == "tool" && args[1] == "install") {
            List<string> inherited = new List<string>();
            foreach (DictionaryEntry entry in Environment.GetEnvironmentVariables()) {
                string key = ((string)entry.Key).ToUpperInvariant();
                if (key.StartsWith("UV_") || key.StartsWith("CARGO_DIST_") ||
                    key.StartsWith("INSTALLER_") || key == "CARGO_HOME") {
                    inherited.Add(key + "=" + (string)entry.Value);
                }
            }
            inherited.Sort(StringComparer.Ordinal);
            Record("env", String.Join("\n", inherited.ToArray()));
            if (Env("PAIRMUX_UV_TEST_MODE") == "fail") { return 7; }
            string bin = Env("UV_TOOL_BIN_DIR");
            string scripts = Path.Combine(toolEnv, "Scripts");
            Directory.CreateDirectory(bin);
            Directory.CreateDirectory(scripts);
            File.Copy(Env("PAIRMUX_UV_TEST_PAIRMUX"), Path.Combine(scripts, "pairmux.exe"), true);
            File.Copy(Env("PAIRMUX_UV_TEST_PAIRMUX"), Path.Combine(bin, "pairmux.exe"), true);
            File.WriteAllText(Path.Combine(toolEnv, "uv-receipt.toml"), "[tool]\n");
            File.WriteAllText(Path.Combine(toolEnv, "entrypoint.txt"), Path.Combine(bin, "pairmux.exe"));
            return 0;
        }
        return 64;
    }
}
'@
    $pairmuxStub = @'
using System;
public static class FakePairmux {
    public static int Main(string[] args) {
        if (args.Length == 1 && args[0] == "version") {
            Console.WriteLine(Environment.GetEnvironmentVariable("PAIRMUX_FAKE_VERSION"));
            return 0;
        }
        return 64;
    }
}
'@
    Add-Type -TypeDefinition $uvStub -OutputAssembly (Join-Path $work 'uv.exe') -OutputType ConsoleApplication
    Add-Type -TypeDefinition $pairmuxStub -OutputAssembly (Join-Path $work 'fake-pairmux.exe') -OutputType ConsoleApplication
    $env:PAIRMUX_UV_TEST_LOG = Join-Path $work 'uv.txt'
    $env:PAIRMUX_UV_TEST_TOOLS = Join-Path $work 'uv-tools'
    $env:PAIRMUX_UV_TEST_PAIRMUX = Join-Path $work 'fake-pairmux.exe'
    $env:PAIRMUX_FAKE_VERSION = '0.7.0'
    $binDir = Join-Path $work 'native bin'
    $toolEnv = Join-Path $env:PAIRMUX_UV_TEST_TOOLS 'pairmux'
    $expectedInstall = @('tool', 'install', '--no-config', '--default-index', 'https://pypi.org/simple',
        '--no-sources', '--no-build', '--python', '>=3.9', '--upgrade', '--reinstall', '--no-cache')

    # Dry run and invalid pins never reach uv (or the network).
    $result = Invoke-Installer -Arguments @('-DryRun', '-Version', 'v0.7.0-rc.1', '-InstallDir', $binDir)
    Assert-True ($result.ExitCode -eq 0) 'native dry run failed'
    Assert-True ($result.Output.Contains('requirement:  pairmux==0.7.0rc1') -and
        $result.Output.Contains('native Windows') -and $result.Output.Contains("tool bin:     $binDir")) `
        'native dry run did not describe the install'
    $result = Invoke-Installer -Arguments @('-Version', 'v1.2', '-InstallDir', $binDir)
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains('invalid version: v1.2')) 'invalid pin accepted'
    Assert-True ((Get-UvRecord).Count -eq 0) 'dry run or invalid pin invoked uv'

    # Fresh install: inherited uv settings are hidden from uv, argv is exact,
    # and the pinned version is verified against the installed executable.
    $hostile = @{
        UV_INDEX_URL = 'https://example.invalid/simple'; UV_DEFAULT_INDEX = 'https://example.invalid/simple'
        UV_CONFIG_FILE = (Join-Path $work 'evil-uv.toml'); UV_TOOL_BIN_DIR = (Join-Path $work 'elsewhere')
        UV_NO_CACHE = '0'; CARGO_HOME = (Join-Path $work 'cargo'); INSTALLER_NO_MODIFY_PATH = '0'
        CARGO_DIST_FORCE_INSTALL_DIR = (Join-Path $work 'elsewhere')
    }
    $result = Invoke-Installer -Arguments @('-Version', 'v0.7.0', '-InstallDir', $binDir) -Environment $hostile
    Assert-True ($result.ExitCode -eq 0) 'native install failed'
    $records = Get-UvRecord
    $argvRecords = @($records | Where-Object { $_.Kind -eq 'argv' } | ForEach-Object { $_.Text })
    Assert-True ($argvRecords.Count -eq 2 -and $argvRecords[0] -ceq "tool`ndir`n--no-config") `
        ('unexpected uv calls: ' + ($argvRecords -join ' | '))
    Assert-True ($argvRecords[1] -ceq (($expectedInstall + 'pairmux==0.7.0') -join "`n")) `
        ('unexpected uv install argv: ' + $argvRecords[1])
    $envRecords = @($records | Where-Object { $_.Kind -eq 'env' } | ForEach-Object { $_.Text })
    Assert-True ($envRecords.Count -eq 1 -and $envRecords[0] -ceq ('UV_TOOL_BIN_DIR=' + $binDir)) `
        ('uv saw inherited settings: ' + ($envRecords -join ' | '))
    Assert-True (Test-Path -LiteralPath (Join-Path $binDir 'pairmux.exe') -PathType Leaf) 'pairmux.exe not installed'
    Assert-True ($result.Output.Contains("pairmux 0.7.0 is ready at $binDir\pairmux.exe")) 'success was not reported'

    # Reinstall over the uv-managed copy: the receipt listing is inspected first.
    Remove-Item -LiteralPath $env:PAIRMUX_UV_TEST_LOG
    $result = Invoke-Installer -Arguments @('-InstallDir', $binDir)
    Assert-True ($result.ExitCode -eq 0) 'reinstall over the managed copy failed'
    $argvRecords = @(Get-UvRecord | Where-Object { $_.Kind -eq 'argv' } | ForEach-Object { $_.Text })
    Assert-True ($argvRecords.Count -eq 3 -and
        $argvRecords[1] -ceq "tool`nlist`n--no-config`n--show-paths`n--color`nnever" -and
        $argvRecords[2] -ceq (($expectedInstall + 'pairmux') -join "`n")) `
        ('unexpected reinstall calls: ' + ($argvRecords -join ' | '))

    # Failures: a mismatched version, and uv itself failing.
    $result = Invoke-Installer -Arguments @('-Version', 'v0.7.0', '-InstallDir', $binDir) `
        -Environment @{ PAIRMUX_FAKE_VERSION = '0.6.9' }
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains('reports 0.6.9; expected 0.7.0')) `
        'a version mismatch was accepted'
    $result = Invoke-Installer -Arguments @('-InstallDir', $binDir) -Environment @{ PAIRMUX_UV_TEST_MODE = 'fail' }
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains("PAIRMUX_INSTALL_TARGET = 'wsl'")) `
        'a uv failure was ignored'

    # Never replace files uv does not own: a foreign pairmux.exe at the
    # destination, a receipt pointing at a foreign file, and an ambiguous path.
    $installCount = @(Get-UvRecord | Where-Object { $_.Kind -eq 'env' }).Count
    $foreignBin = Join-Path $work 'foreign bin'
    New-Item -ItemType Directory -Path $foreignBin | Out-Null
    [IO.File]::WriteAllText((Join-Path $foreignBin 'pairmux.exe'), 'not uv-managed')
    $result = Invoke-Installer -Arguments @('-InstallDir', $foreignBin)
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains('refusing to overwrite')) 'a foreign pairmux.exe was replaced'
    Assert-True ([IO.File]::ReadAllText((Join-Path $foreignBin 'pairmux.exe')) -ceq 'not uv-managed') 'foreign file changed'
    $entrypoint = Join-Path $toolEnv 'entrypoint.txt'
    $managedEntry = [IO.File]::ReadAllText($entrypoint)
    $victim = Join-Path $foreignBin 'pairmux.exe'
    [IO.File]::WriteAllText($entrypoint, $victim)
    $result = Invoke-Installer -Arguments @('-InstallDir', $binDir)
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains('refusing to replace')) 'a foreign receipt entrypoint was accepted'
    [IO.File]::WriteAllText($entrypoint, (Join-Path $work 'bin (x)\pairmux.exe'))
    $result = Invoke-Installer -Arguments @('-InstallDir', $binDir)
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains('ambiguous name/path delimiter')) 'an ambiguous listing was accepted'
    [IO.File]::WriteAllText($entrypoint, $managedEntry)
    Assert-True (@(Get-UvRecord | Where-Object { $_.Kind -eq 'env' }).Count -eq $installCount) 'a refused run invoked uv tool install'

    # Without uv on PATH: a previously bootstrapped %USERPROFILE%\.local\bin\uv.exe
    # is used; a lone uvx.exe there blocks the bootstrap before any download.
    $profileDir = Join-Path $work 'profile'
    $uvHome = Join-Path $profileDir '.local\bin'
    New-Item -ItemType Directory -Path $uvHome | Out-Null
    $systemPath = @("$env:SystemRoot\System32", $env:SystemRoot, "$env:SystemRoot\System32\WindowsPowerShell\v1.0") -join ';'
    [IO.File]::WriteAllText((Join-Path $uvHome 'uvx.exe'), 'placeholder')
    $result = Invoke-Installer -Arguments @('-InstallDir', $binDir) -Environment @{ PATH = $systemPath; USERPROFILE = $profileDir }
    Assert-True ($result.ExitCode -ne 0 -and $result.Output.Contains('refusing to overwrite') -and
        -not $result.Output.Contains('bootstrapping')) 'a uv bootstrap would overwrite an existing uvx.exe'
    Copy-Item -LiteralPath (Join-Path $work 'uv.exe') -Destination (Join-Path $uvHome 'uv.exe')
    $result = Invoke-Installer -Arguments @('-InstallDir', $binDir) -Environment @{ PATH = $systemPath; USERPROFILE = $profileDir }
    Assert-True ($result.ExitCode -eq 0) 'the previously bootstrapped uv.exe was not used'
    $result = Invoke-Installer -Arguments @('-DryRun') -Environment @{ USERPROFILE = $profileDir }
    Assert-True ($result.Output.Contains("tool bin:     $uvHome")) 'the default tool bin is not %USERPROFILE%\.local\bin'

    # Piped mode (irm | iex): no script file, so the caller's session must keep
    # its environment and must not be exited by a failure.
    $quotedInstaller = $installer.Replace("'", "''")
    $quotedBin = $binDir.Replace("'", "''")
    $piped = @"
`$ErrorActionPreference = 'Stop'
Remove-Item -LiteralPath Env:UV_TOOL_BIN_DIR -ErrorAction SilentlyContinue
`$env:UV_INDEX_URL = 'https://example.invalid/simple'
`$env:CARGO_HOME = 'kept'
`$env:PAIRMUX_INSTALL_DIR = '$quotedBin'
Get-Content -Raw -LiteralPath '$quotedInstaller' | Invoke-Expression
if (`$env:UV_INDEX_URL -cne 'https://example.invalid/simple' -or `$env:CARGO_HOME -cne 'kept' -or `$env:UV_TOOL_BIN_DIR) { exit 11 }
`$env:PAIRMUX_VERSION = 'v1.2'
try { Get-Content -Raw -LiteralPath '$quotedInstaller' | Invoke-Expression; exit 12 } catch {
    if ("`$_" -like '*pairmux installation failed: invalid version*') { exit 20 }
    exit 13
}
"@
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($piped))
    & powershell.exe -NoProfile -NonInteractive -EncodedCommand $encoded | Out-Host
    if ($LASTEXITCODE -ne 20) { throw "piped-mode checks failed (exit $LASTEXITCODE)" }

    # The child failures above are expected; do not leak them to the Actions shell.
    $global:LASTEXITCODE = 0
    Write-Host 'Windows installer tests passed: native uv and opt-in WSL targets (no network or installation)'
} finally {
    $env:PATH = $previousPath
    $env:USERPROFILE = $previousProfile
    foreach ($name in $controlled) {
        [Environment]::SetEnvironmentVariable($name, $previousControlled[$name], 'Process')
    }
    Remove-Item -LiteralPath $work -Recurse -Force
}
