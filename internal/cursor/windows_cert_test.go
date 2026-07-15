//go:build windows

package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsSystemExecutableDoesNotDependOnPath(t *testing.T) {
	root := t.TempDir()
	certutilPath := filepath.Join(root, "System32", "certutil.exe")
	if err := os.MkdirAll(filepath.Dir(certutilPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certutilPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", "")
	t.Setenv("SystemRoot", root)
	t.Setenv("windir", "")

	got, err := windowsSystemExecutable("System32", "certutil.exe")
	if err != nil {
		t.Fatal(err)
	}
	if got != certutilPath {
		t.Fatalf("windowsSystemExecutable() = %q, want %q", got, certutilPath)
	}
}

func TestWindowsSystemExecutableFallsBackToWindir(t *testing.T) {
	root := t.TempDir()
	powerShellPath := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if err := os.MkdirAll(filepath.Dir(powerShellPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(powerShellPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SystemRoot", "")
	t.Setenv("windir", root)

	got, err := windowsSystemExecutable("System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	if got != powerShellPath {
		t.Fatalf("windowsSystemExecutable() = %q, want %q", got, powerShellPath)
	}
}

func TestWindowsSystemExecutableReportsMissingSystemRoot(t *testing.T) {
	t.Setenv("SystemRoot", "")
	t.Setenv("windir", "")

	_, err := windowsSystemExecutable("System32", "certutil.exe")
	if err == nil || !strings.Contains(err.Error(), "无法确定 Windows 系统目录") {
		t.Fatalf("windowsSystemExecutable() error = %v", err)
	}
}
