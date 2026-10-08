# Windows PowerShell's legacy native argument binder differs from pwsh on Unix.
# Use a native argv-capturing WSL stub on Windows, without downloading anything.
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$work = Join-Path ([IO.Path]::GetTempPath()) ('pairmux-wsl-test-' + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
$previousPath = $env:PATH
$previousLog = $env:PAIRMUX_WSL_TEST_LOG
$previousMode = $env:PAIRMUX_WSL_TEST_MODE
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
    $env:PATH = $work + [IO.Path]::PathSeparator + $env:PATH
    $env:PAIRMUX_WSL_TEST_LOG = Join-Path $work 'argv.txt'
    $installDir = '/tmp/bin with spaces and apostrophe''s (test)'
    & powershell.exe -NoProfile -File (Join-Path $root 'install.ps1') `
        -Version v0.5.3 -InstallDir $installDir -Distribution Ubuntu
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
    $env:PAIRMUX_WSL_TEST_MODE = 'fail'
    & powershell.exe -NoProfile -File (Join-Path $root 'install.ps1') -Distribution Ubuntu
    if ($LASTEXITCODE -eq 0) { throw 'WSL failure was ignored' }
    # The child failure above is expected; do not leak it to the Actions shell.
    $global:LASTEXITCODE = 0
    Write-Host 'Windows legacy-native WSL argv tests passed (no network or installation)'
} finally {
    $env:PATH = $previousPath
    $env:PAIRMUX_WSL_TEST_LOG = $previousLog
    $env:PAIRMUX_WSL_TEST_MODE = $previousMode
    Remove-Item -LiteralPath $work -Recurse -Force
}
