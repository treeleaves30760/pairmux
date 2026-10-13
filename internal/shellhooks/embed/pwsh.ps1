# pairmux PowerShell shim: passed to `pwsh -NoExit -EncodedCommand` (or Windows
# PowerShell), so it runs after the user's profile and is not subject to the
# script execution policy. It wraps whatever prompt the profile installed and
# emits OSC 133 command-end (D;exit) and prompt-start (A) marks around it. The
# first prompt follows no command, so it emits A only.
if (-not $global:__pairmux_hooked) {
    $global:__pairmux_hooked = $true
    $global:__pairmux_first = $true
    $global:__pairmux_prompt = $function:prompt
    if (-not $Host.UI.SupportsVirtualTerminal) {
        # Windows PowerShell may leave VT processing off, which would print the
        # marks as text instead of passing them to the terminal.
        try {
            Add-Type -Namespace PairMux -Name Console -MemberDefinition @'
[DllImport("kernel32.dll")] public static extern System.IntPtr GetStdHandle(int h);
[DllImport("kernel32.dll")] public static extern bool GetConsoleMode(System.IntPtr h, out int m);
[DllImport("kernel32.dll")] public static extern bool SetConsoleMode(System.IntPtr h, int m);
'@
            $h = [PairMux.Console]::GetStdHandle(-11)
            $m = 0
            if ([PairMux.Console]::GetConsoleMode($h, [ref]$m)) {
                [void][PairMux.Console]::SetConsoleMode($h, $m -bor 4)
            }
        } catch { }
    }
    function global:prompt {
        $ok = $?
        $lec = $global:LASTEXITCODE
        $e = [char]27
        $b = [char]7
        $marks = ''
        if ($global:__pairmux_first) {
            $global:__pairmux_first = $false
        } else {
            $code = 1
            if ($ok) { $code = 0 } elseif ($lec -is [int] -and $lec -ne 0) { $code = $lec }
            $marks = "$e]133;D;$code$b"
        }
        $text = & $global:__pairmux_prompt
        $global:LASTEXITCODE = $lec
        "$marks$e]133;A$b$text"
    }
}
