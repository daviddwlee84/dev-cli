package desktop

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRecycleSinkRejectsPermanentDisposition(t *testing.T) {
	// Compile the actual embedded helper and exercise its pre-effect callback.
	// No filesystem deletion or Windows shell operation runs in this test.
	script, _, found := strings.Cut(recycleScript, "[DevRecycle]::Run(")
	if !found {
		t.Fatal("helper entry point changed")
	}
	script += `
$sink = New-Object RecycleSink
if ($sink.PreDeleteItem(0, $null) -ge 0) { throw 'permanent disposition accepted' }
if ($sink.PreDeleteItem(128, $null) -ne 0) { throw 'recycle disposition rejected' }
if ($sink.PostDeleteItem(128, $null, 0, $null) -ge 0) { throw 'missing recycle receipt accepted' }
Write-Output 'sink-ok'
`
	body, err := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil || !strings.Contains(string(body), "sink-ok") {
		t.Fatalf("recycle sink: %v: %s", err, body)
	}
}

func TestRecycleHelperReadsUTF8Paths(t *testing.T) {
	script, _, found := strings.Cut(recycleScript, "[DevRecycle]::Run(")
	if !found {
		t.Fatal("helper entry point changed")
	}
	script += "\n[Console]::Out.Write([Console]::In.ReadToEnd())\n"
	path := "C:\\Users\\測試\\Try ' literal"
	cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Stdin = strings.NewReader(path)
	body, err := cmd.CombinedOutput()
	if err != nil || string(body) != path {
		t.Fatalf("UTF-8 round trip: %q: %v", body, err)
	}
}
