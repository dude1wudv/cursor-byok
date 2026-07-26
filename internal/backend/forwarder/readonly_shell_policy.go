// readonly_shell_policy.go 定义 inspect（只读）子代理的受控 Shell 白名单策略。
// 安全边界在服务端 pre-dispatch 强制执行，不依赖提示词描述。
package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

// readonlyShellMaxBlockUntilMS 限定 inspect Shell 只允许短前台窗口，禁止 0（立即后台）。
const readonlyShellMaxBlockUntilMS = 120000

// gitReadonlySubcommands 是允许的只读 Git 证据链子命令。
var gitReadonlySubcommands = map[string]struct{}{
	"status": {}, "diff": {}, "log": {}, "show": {}, "blame": {}, "rev-parse": {},
	"merge-base": {}, "tag": {}, "branch": {}, "remote": {}, "ls-tree": {}, "ls-files": {},
}

// gitListOnlySubcommands 的位置参数会创建/删除引用，因此只允许纯 flag 的列表形式。
var gitListOnlySubcommands = map[string]struct{}{"tag": {}, "branch": {}, "remote": {}}

// readonlyShellSimpleExecutables 是白名单中的进程/端口/哈希查询命令；
// 值非空表示第一个参数必须精确命中（certutil 只允许哈希用途，阻断 LOLBIN 下载路径）。
var readonlyShellSimpleExecutables = map[string]string{
	"tasklist":  "",
	"netstat":   "",
	"ps":        "",
	"ss":        "",
	"lsof":      "",
	"sha256sum": "",
	"sha1sum":   "",
	"md5sum":    "",
	"shasum":    "",
	"certutil":  "-hashfile",
}

// applyReadonlyShellPolicy 校验 inspect 子代理的 Shell 参数，并返回注入只读保护后的 args。
func applyReadonlyShellPolicy(argsJSON []byte, workspacePaths []string) ([]byte, error) {
	args, err := runtimecore.DecodeArgsMap(argsJSON)
	if err != nil {
		return nil, fmt.Errorf("decode Shell args: %w", err)
	}
	if _, found := args["notify_on_output"]; found {
		return nil, fmt.Errorf("inspect Shell does not support notify_on_output")
	}
	profile := strings.TrimSpace(runtimecore.ReadStringArg(args, "profile"))
	if profile != "" && profile != "auto" {
		return nil, fmt.Errorf("inspect Shell only supports profile=auto")
	}
	if blockUntilMS, found, err := runtimecore.ReadFloat64Arg(args, "block_until_ms", "blockUntilMS"); err != nil {
		return nil, fmt.Errorf("inspect Shell block_until_ms must be a number")
	} else if found && (blockUntilMS <= 0 || blockUntilMS > readonlyShellMaxBlockUntilMS) {
		return nil, fmt.Errorf("inspect Shell block_until_ms must stay within a short foreground window (1-%d ms)", readonlyShellMaxBlockUntilMS)
	}
	if err := validateReadonlyShellWorkingDirectory(runtimecore.ReadStringArg(args, "working_directory", "workingDirectory"), workspacePaths); err != nil {
		return nil, err
	}
	tokens, simple := execbridge.ParseSimpleShellCommand(runtimecore.ReadStringArg(args, "command"))
	if !simple || len(tokens) == 0 {
		return nil, fmt.Errorf("inspect Shell only accepts a single simple command without pipes, redirection, quoting, variables, or command substitution")
	}
	rewrittenTokens, err := validateReadonlyShellCommandTokens(tokens)
	if err != nil {
		return nil, err
	}
	if rewrittenTokens == nil {
		return argsJSON, nil
	}
	args["command"] = strings.Join(rewrittenTokens, " ")
	rewritten, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode inspect Shell args: %w", err)
	}
	return rewritten, nil
}

// validateReadonlyShellWorkingDirectory 把 inspect Shell 绑定在会话工作区内。
func validateReadonlyShellWorkingDirectory(workingDirectory string, workspacePaths []string) error {
	trimmed := strings.TrimSpace(workingDirectory)
	if trimmed == "" {
		return nil
	}
	normalized := normalizeReadonlyShellPath(trimmed)
	for _, workspace := range workspacePaths {
		workspaceNormalized := normalizeReadonlyShellPath(workspace)
		if workspaceNormalized == "" {
			continue
		}
		if normalized == workspaceNormalized || strings.HasPrefix(normalized, workspaceNormalized+"/") {
			return nil
		}
	}
	return fmt.Errorf("inspect Shell working_directory must stay inside the workspace: %s", trimmed)
}

func normalizeReadonlyShellPath(path string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"))
	return strings.TrimRight(normalized, "/")
}

// validateReadonlyShellCommandTokens 返回非 nil 时表示需要用改写后的词元替换原命令。
func validateReadonlyShellCommandTokens(tokens []string) ([]string, error) {
	if strings.ContainsAny(tokens[0], "/\\") {
		return nil, fmt.Errorf("inspect Shell requires a bare executable name: %s", tokens[0])
	}
	executable := strings.TrimSuffix(strings.ToLower(tokens[0]), ".exe")
	if executable == "git" {
		return validateReadonlyGitCommand(tokens)
	}
	requiredFirst, allowed := readonlyShellSimpleExecutables[executable]
	if !allowed {
		return nil, fmt.Errorf("inspect Shell command %q is not in the read-only whitelist", tokens[0])
	}
	if requiredFirst != "" && (len(tokens) < 2 || !strings.EqualFold(tokens[1], requiredFirst)) {
		return nil, fmt.Errorf("inspect Shell %q is only allowed as %q", executable, executable+" "+requiredFirst)
	}
	return nil, nil
}

func validateReadonlyGitCommand(tokens []string) ([]string, error) {
	if len(tokens) < 2 {
		return nil, fmt.Errorf("inspect Shell git requires a read-only subcommand")
	}
	subcommand := strings.ToLower(tokens[1])
	if _, ok := gitReadonlySubcommands[subcommand]; !ok {
		return nil, fmt.Errorf("inspect Shell git subcommand %q is not read-only", tokens[1])
	}
	rest := tokens[2:]
	if _, listOnly := gitListOnlySubcommands[subcommand]; listOnly {
		for _, token := range rest {
			if !strings.HasPrefix(token, "-") {
				return nil, fmt.Errorf("inspect Shell git %s only allows list-style flags, positional arguments would mutate refs", subcommand)
			}
			if !isAllowedGitListFlag(token) {
				return nil, fmt.Errorf("inspect Shell git %s flag %q is not allowed", subcommand, token)
			}
		}
	} else {
		for _, token := range rest {
			lower := strings.ToLower(token)
			if lower == "--output" || strings.HasPrefix(lower, "--output=") || lower == "--ext-diff" {
				return nil, fmt.Errorf("inspect Shell git flag %q is not allowed", token)
			}
		}
	}
	// 注入 --no-pager --no-optional-locks，确保无分页器阻塞且不写 index 锁。
	return append([]string{tokens[0], "--no-pager", "--no-optional-locks"}, tokens[1:]...), nil
}

func isAllowedGitListFlag(token string) bool {
	lower := strings.ToLower(token)
	switch lower {
	case "-a", "-v", "-vv", "-l", "--all", "--list", "--verbose", "--show-current", "--merged", "--no-merged":
		return true
	}
	for _, prefix := range []string{"--sort=", "--format=", "--points-at=", "--contains=", "--column"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// enforceReadonlyShellPolicy 在 pre-dispatch 阶段对 inspect child 的 Shell 调用强制白名单，
// 校验通过时返回注入保护参数后的 invocation。
func (service *Service) enforceReadonlyShellPolicy(stream *ActiveStream, invocation runtimecore.ToolInvocation) (runtimecore.ToolInvocation, error) {
	if service == nil || stream == nil {
		return invocation, nil
	}
	stream.mu.Lock()
	workspacePaths := append([]string(nil), stream.WorkspacePaths...)
	stream.mu.Unlock()
	rewritten, err := applyReadonlyShellPolicy(invocation.ArgsJSON, workspacePaths)
	if err != nil {
		return invocation, err
	}
	invocation.ArgsJSON = rewritten
	return invocation, nil
}