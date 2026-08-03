package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func withTempCursorSettings(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CURSOR_SETTINGS_PATH", path)
	return path
}

func readSettingsForTest(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCursorSettingsPatchRejectsInvalidJSONCWithoutDeletingFile(t *testing.T) {
	path := withTempCursorSettings(t, `{ "editor.fontSize": 14, /* unterminated`)
	before, _ := os.ReadFile(path)
	if err := WriteUserProxySettings("http://127.0.0.1:18080"); err == nil {
		t.Fatal("expected invalid JSONC error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("invalid settings changed: before=%q after=%q", before, after)
	}
}

func TestCursorUpdatePolicyRestoresOriginalValues(t *testing.T) {
	path := withTempCursorSettings(t, `{"editor.fontSize":14,"update.mode":"default","update.enableWindowsBackgroundUpdates":true}`)
	if err := ApplyCursorAutoUpdatePolicy(true); err != nil {
		t.Fatal(err)
	}
	got := readSettingsForTest(t, path)
	if got["update.mode"] != "manual" {
		t.Fatalf("update.mode=%v", got["update.mode"])
	}
	if runtime.GOOS == "windows" && got["update.enableWindowsBackgroundUpdates"] != false {
		t.Fatalf("background update=%v", got["update.enableWindowsBackgroundUpdates"])
	}
	if err := ApplyCursorAutoUpdatePolicy(false); err != nil {
		t.Fatal(err)
	}
	got = readSettingsForTest(t, path)
	if got["update.mode"] != "default" || got["editor.fontSize"] != float64(14) {
		t.Fatalf("settings not restored: %#v", got)
	}
	if runtime.GOOS == "windows" && got["update.enableWindowsBackgroundUpdates"] != true {
		t.Fatalf("background update not restored: %#v", got)
	}
}

func TestCursorUpdatePolicyDoesNotOverwriteExternalChange(t *testing.T) {
	path := withTempCursorSettings(t, `{"update.mode":"default"}`)
	if err := ApplyCursorAutoUpdatePolicy(true); err != nil {
		t.Fatal(err)
	}
	got := readSettingsForTest(t, path)
	got["update.mode"] = "none"
	data, _ := json.Marshal(got)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyCursorAutoUpdatePolicy(false); err != nil {
		t.Fatal(err)
	}
	if got := readSettingsForTest(t, path); got["update.mode"] != "none" {
		t.Fatalf("external value overwritten: %#v", got)
	}
}

func TestProxyAndUpdatePatchesRestoreIndependently(t *testing.T) {
	path := withTempCursorSettings(t, `{"editor.fontSize":14}`)
	if err := ApplyCursorAutoUpdatePolicy(true); err != nil {
		t.Fatal(err)
	}
	if err := WriteUserProxySettings("http://127.0.0.1:18080"); err != nil {
		t.Fatal(err)
	}
	if err := ClearUserProxySettings(); err != nil {
		t.Fatal(err)
	}
	got := readSettingsForTest(t, path)
	if got["update.mode"] != "manual" {
		t.Fatalf("update policy removed with proxy: %#v", got)
	}
	if _, ok := got["http.proxy"]; ok {
		t.Fatalf("proxy not restored: %#v", got)
	}
}
