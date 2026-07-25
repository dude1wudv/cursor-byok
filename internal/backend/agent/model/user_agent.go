package modeladapter

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"sync"
)

const (
	codexDesktopUserAgentPrefix = "Codex Desktop/"
	fallbackCodexDesktopVersion = "0.146.0-alpha.3"
	codexBackendURLMarker       = "https://chatgpt.com/backend-api/"
	// AnthropicClaudeCodeUserAgent 用于 Anthropic provider 的 Claude Code UA 兼容。
	AnthropicClaudeCodeUserAgent = "claude-cli/1.0.25"
)

var (
	codexDesktopVersionPattern = regexp.MustCompile(`([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)$`)
	codexDesktopUserAgentOnce  sync.Once
	codexDesktopUserAgent      string
)

// CodexDesktopUserAgent returns the Codex Desktop-compatible OpenAI User-Agent.
// The installed bundled CLI is inspected once per process; the known version is
// retained as a fallback so missing or unreadable Codex installations never block requests.
func CodexDesktopUserAgent() string {
	codexDesktopUserAgentOnce.Do(func() {
		version := fallbackCodexDesktopVersion
		for _, path := range installedCodexExecutables() {
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			if info, err := file.Stat(); err != nil || info.Size() > 512<<20 {
				_ = file.Close()
				continue
			}
			detected := codexDesktopVersionFromReader(file)
			_ = file.Close()
			if detected != "" {
				version = detected
				break
			}
		}
		codexDesktopUserAgent = codexDesktopUserAgentPrefix + version
	})
	return codexDesktopUserAgent
}

func installedCodexExecutables() []string {
	var candidates []string
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("codex.exe"); err == nil {
			candidates = append(candidates, path)
		}
		if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
			matches, _ := filepath.Glob(filepath.Join(programFiles, "WindowsApps", "OpenAI.Codex_*", "app", "resources", "codex.exe"))
			sort.Sort(sort.Reverse(sort.StringSlice(matches)))
			candidates = append(candidates, matches...)
		}
	} else if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/Applications/Codex.app/Contents/Resources/codex")
	}
	return candidates
}

func codexDesktopVersionFromReader(reader io.Reader) string {
	const versionPrefixLimit = 96
	overlap := versionPrefixLimit + len(codexBackendURLMarker)
	buffer := make([]byte, 256*1024+overlap)
	kept := 0
	marker := []byte(codexBackendURLMarker)

	for {
		read, err := reader.Read(buffer[kept:])
		window := buffer[:kept+read]
		if markerIndex := bytes.Index(window, marker); markerIndex >= 0 {
			prefixStart := max(0, markerIndex-versionPrefixLimit)
			if match := codexDesktopVersionPattern.FindSubmatch(window[prefixStart:markerIndex]); len(match) == 2 {
				return string(match[1])
			}
		}
		if err != nil {
			return ""
		}
		kept = min(len(window), overlap)
		copy(buffer[:kept], window[len(window)-kept:])
	}
}
