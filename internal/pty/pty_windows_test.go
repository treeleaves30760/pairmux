//go:build windows

package pty

import "testing"

func TestComposeCommandLine(t *testing.T) {
	cases := []struct {
		path string
		args []string
		want string
	}{
		{`C:\Program Files\PowerShell\7\pwsh.exe`, []string{"-NoLogo", "-Command", `Write-Output "a b"`},
			`"C:\Program Files\PowerShell\7\pwsh.exe" -NoLogo -Command "Write-Output \"a b\""`},
		// cmd.exe gets its /c command verbatim inside the quotes /s strips.
		{`C:\Windows\system32\cmd.exe`, []string{"/s", "/c", `dir "C:\Program Files"`},
			`C:\Windows\system32\cmd.exe /s /c "dir "C:\Program Files""`},
		{`C:\Windows\system32\CMD.EXE`, []string{"/v:on", "/k"}, `C:\Windows\system32\CMD.EXE /v:on /k`},
	}
	for _, c := range cases {
		if got := composeCommandLine(c.path, c.args); got != c.want {
			t.Errorf("composeCommandLine(%q, %q)\n got %s\nwant %s", c.path, c.args, got, c.want)
		}
	}
}
