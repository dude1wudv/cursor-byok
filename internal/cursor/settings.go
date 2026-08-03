package cursor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"cursor/internal/appdata"
	"cursor/internal/logger"
)

const (
	cursorSettingsPatchGroupProxy  = "proxy"
	cursorSettingsPatchGroupUpdate = "update"
)

var cursorSettingsPatchMu sync.Mutex

var injectedCursorProxySettings = map[string]any{
	"http.proxySupport":                      "on",
	"cursor.general.disableHttp2":            true,
	"http.experimental.systemCertificatesV2": true,
}

type cursorSettingBackup struct {
	Present     bool `json:"present"`
	Original    any  `json:"original,omitempty"`
	LastWritten any  `json:"lastWritten,omitempty"`
}

type cursorSettingsPatchState struct {
	Groups map[string]map[string]cursorSettingBackup `json:"groups"`
}

// EnsureCACertFile 用于处理与 EnsureCACertFile 相关的逻辑。
func EnsureCACertFile(certPEM []byte, currentPath string) (string, error) {
	certPath := appdata.CACertFilePath()
	if samePath(strings.TrimSpace(currentPath), certPath) {
		if _, err := os.Stat(certPath); err == nil {
			logger.Infof("ensureCACertFile: reusing path=%s", certPath)
			return certPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return "", fmt.Errorf("创建证书配置目录失败: %w", err)
	}

	if existing, err := os.ReadFile(certPath); err == nil && bytes.Equal(existing, certPEM) {
		logger.Infof("ensureCACertFile: unchanged path=%s", certPath)
		return certPath, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取内置 CA 证书失败: %w", err)
	}

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", fmt.Errorf("写入内置 CA 证书失败: %w", err)
	}
	sum := sha256.Sum256(certPEM)
	logger.Infof(
		"ensureCACertFile: wrote path=%s sha256=%s size=%d",
		certPath,
		strings.ToUpper(hex.EncodeToString(sum[:])),
		len(certPEM),
	)
	return certPath, nil
}

func samePath(left string, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// SetSystemNodeExtraCACerts 用于处理与 SetSystemNodeExtraCACerts 相关的逻辑。
func SetSystemNodeExtraCACerts(caCertPath string) error {
	caCertPath = strings.TrimSpace(caCertPath)
	if caCertPath == "" {
		return errors.New("CA 证书路径为空")
	}
	if err := os.Setenv("NODE_EXTRA_CA_CERTS", caCertPath); err != nil {
		return fmt.Errorf("设置进程环境变量失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "setenv", "NODE_EXTRA_CA_CERTS", caCertPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("写入 macOS 用户环境变量失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		// Linux 发行版环境变量持久化方式差异较大，这里先确保当前进程生效。
		logger.Infof("setSystemNodeExtraCACerts: linux detected, applied to current process only")
	default:
		return fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	logger.Infof("setSystemNodeExtraCACerts: NODE_EXTRA_CA_CERTS=%s", caCertPath)
	return nil
}

// ClearSystemNodeExtraCACerts 用于处理与 ClearSystemNodeExtraCACerts 相关的逻辑。
func ClearSystemNodeExtraCACerts() error {
	if err := os.Unsetenv("NODE_EXTRA_CA_CERTS"); err != nil {
		return fmt.Errorf("清理进程环境变量失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "unsetenv", "NODE_EXTRA_CA_CERTS").CombinedOutput()
		if err != nil {
			return fmt.Errorf("清理 macOS 用户环境变量失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		logger.Infof("clearSystemNodeExtraCACerts: linux detected, cleared in current process only")
	default:
		return fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	logger.Infof("clearSystemNodeExtraCACerts: NODE_EXTRA_CA_CERTS cleared")
	return nil
}

// WriteUserProxySettings 通过统一补丁层写入代理设置，并保留用户原值以便精确恢复。
func WriteUserProxySettings(proxyURL string) error {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return errors.New("代理地址为空")
	}
	updates := make(map[string]any, len(injectedCursorProxySettings)+2)
	for key, value := range injectedCursorProxySettings {
		updates[key] = value
	}
	updates["http.proxy"] = proxyURL
	updates["http.proxyKerberosServicePrincipal"] = proxyURL
	return applyCursorSettingsPatch(cursorSettingsPatchGroupProxy, updates)
}

// ClearUserProxySettings 仅恢复仍等于本程序最后写入值的代理键，避免覆盖用户随后修改。
func ClearUserProxySettings() error { return restoreCursorSettingsPatch(cursorSettingsPatchGroupProxy) }

// ApplyCursorAutoUpdatePolicy 应用或恢复 Cursor 自动更新策略。关闭策略不会影响代理设置。
func ApplyCursorAutoUpdatePolicy(disable bool) error {
	if !disable {
		return restoreCursorSettingsPatch(cursorSettingsPatchGroupUpdate)
	}
	updates := map[string]any{"update.mode": "manual"}
	if runtime.GOOS == "windows" {
		updates["update.enableWindowsBackgroundUpdates"] = false
	}
	return applyCursorSettingsPatch(cursorSettingsPatchGroupUpdate, updates)
}

func applyCursorSettingsPatch(group string, updates map[string]any) error {
	cursorSettingsPatchMu.Lock()
	defer cursorSettingsPatchMu.Unlock()
	settingsPath, err := resolveCursorSettingsPath()
	if err != nil {
		return err
	}
	settings, originalData, err := readCursorSettings(settingsPath)
	if err != nil {
		return err
	}
	statePath := cursorSettingsPatchStatePath(settingsPath)
	state, err := readCursorSettingsPatchState(statePath)
	if err != nil {
		return err
	}
	if state.Groups == nil {
		state.Groups = make(map[string]map[string]cursorSettingBackup)
	}
	backups := state.Groups[group]
	if backups == nil {
		backups = make(map[string]cursorSettingBackup)
		state.Groups[group] = backups
	}
	for key, desired := range updates {
		backup, tracked := backups[key]
		if !tracked {
			original, present := settings[key]
			backup = cursorSettingBackup{Present: present, Original: original}
		}
		backup.LastWritten = desired
		backups[key] = backup
		settings[key] = desired
	}
	if err := writeCursorSettingsIfChanged(settingsPath, settings, originalData); err != nil {
		return err
	}
	return writeCursorSettingsPatchState(statePath, state)
}

func restoreCursorSettingsPatch(group string) error {
	cursorSettingsPatchMu.Lock()
	defer cursorSettingsPatchMu.Unlock()
	settingsPath, err := resolveCursorSettingsPath()
	if err != nil {
		return err
	}
	statePath := cursorSettingsPatchStatePath(settingsPath)
	state, err := readCursorSettingsPatchState(statePath)
	if err != nil {
		return err
	}
	backups := state.Groups[group]
	if len(backups) == 0 {
		return nil
	}
	settings, originalData, err := readCursorSettings(settingsPath)
	if err != nil {
		return err
	}
	for key, backup := range backups {
		current, present := settings[key]
		if !present || !reflect.DeepEqual(current, backup.LastWritten) {
			continue
		}
		if backup.Present {
			settings[key] = backup.Original
		} else {
			delete(settings, key)
		}
	}
	delete(state.Groups, group)
	if err := writeCursorSettingsIfChanged(settingsPath, settings, originalData); err != nil {
		return err
	}
	return writeCursorSettingsPatchState(statePath, state)
}

func readCursorSettings(settingsPath string) (map[string]any, []byte, error) {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("读取 Cursor 配置失败: %w", err)
		}
		return make(map[string]any), nil, nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return make(map[string]any), data, nil
	}
	settings, err := decodeCursorSettingsJSONC(data)
	if err != nil {
		return nil, nil, fmt.Errorf("解析 Cursor 配置失败，已保留原文件: %w", err)
	}
	return settings, data, nil
}

func writeCursorSettingsIfChanged(settingsPath string, settings map[string]any, originalData []byte) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("创建 Cursor 配置目录失败: %w", err)
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(originalData) > 0 && bytes.Equal(originalData, encoded) {
		return nil
	}
	return atomicWriteCursorFile(settingsPath, encoded, 0o644)
}

func cursorSettingsPatchStatePath(settingsPath string) string {
	return settingsPath + ".cursor-byok-state.json"
}

func readCursorSettingsPatchState(path string) (cursorSettingsPatchState, error) {
	state := cursorSettingsPatchState{Groups: make(map[string]map[string]cursorSettingBackup)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, fmt.Errorf("读取 Cursor 设置补丁状态失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("解析 Cursor 设置补丁状态失败: %w", err)
	}
	if state.Groups == nil {
		state.Groups = make(map[string]map[string]cursorSettingBackup)
	}
	return state, nil
}

func writeCursorSettingsPatchState(path string, state cursorSettingsPatchState) error {
	if len(state.Groups) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("清理 Cursor 设置补丁状态失败: %w", err)
		}
		return nil
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 设置补丁状态失败: %w", err)
	}
	encoded = append(encoded, '\n')
	return atomicWriteCursorFile(path, encoded, 0o600)
}

func atomicWriteCursorFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceCursorSettingsFile(tempPath, path); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}
	return nil
}

// resolveCursorSettingsPath 用于处理与 resolveCursorSettingsPath 相关的逻辑。
func resolveCursorSettingsPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("CURSOR_SETTINGS_PATH")); override != "" {
		return filepath.Clean(override), nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "settings.json"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if strings.TrimSpace(appData) == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "settings.json"), nil
	case "linux":
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if strings.TrimSpace(configDir) == "" {
			configDir = filepath.Join(homeDir, ".config")
		}
		return filepath.Join(configDir, "Cursor", "User", "settings.json"), nil
	default:
		return "", fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}
}

// decodeCursorSettingsJSONC 用于处理与 decodeCursorSettingsJSONC 相关的逻辑。
func decodeCursorSettingsJSONC(data []byte) (map[string]any, error) {
	result := make(map[string]any)
	normalized, err := normalizeJSONC(data)
	if err != nil {
		return nil, err
	}
	normalized = bytes.TrimSpace(normalized)
	if len(normalized) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(normalized, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// normalizeJSONC 用于处理与 normalizeJSONC 相关的逻辑。
func normalizeJSONC(data []byte) ([]byte, error) {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	withoutComments, err := stripJSONCComments(data)
	if err != nil {
		return nil, err
	}
	return stripJSONCTrailingCommas(withoutComments), nil
}

// stripJSONCComments 用于处理与 stripJSONCComments 相关的逻辑。
func stripJSONCComments(data []byte) ([]byte, error) {
	out := make([]byte, 0, len(data))
	inString := false
	inLineComment := false
	inBlockComment := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				out = append(out, ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(data) && data[i+1] == '/' {
				inBlockComment = false
				i++
				continue
			}
			if ch == '\n' {
				out = append(out, ch)
			}
			continue
		}
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == '/' && i+1 < len(data) {
			next := data[i+1]
			if next == '/' {
				inLineComment = true
				i++
				continue
			}
			if next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}
		out = append(out, ch)
	}

	if inBlockComment {
		return nil, errors.New("JSONC 块注释未闭合")
	}
	return out, nil
}

// stripJSONCTrailingCommas 用于处理与 stripJSONCTrailingCommas 相关的逻辑。
func stripJSONCTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}

		if ch == ',' {
			j := i + 1
			for j < len(data) && isJSONWhitespace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}

		out = append(out, ch)
	}

	return out
}

// isJSONWhitespace 用于处理与 isJSONWhitespace 相关的逻辑。
func isJSONWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

// ProxyURLFromListenAddr 用于处理与 ProxyURLFromListenAddr 相关的逻辑。
func ProxyURLFromListenAddr(listenAddr string) string {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		return "http://127.0.0.1:8080"
	}

	// :8189 -> 127.0.0.1:8189
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port)
	}

	return "http://" + addr
}
