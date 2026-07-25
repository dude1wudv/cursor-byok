# Cursor助手 v0.0.67

本版本为 Shell 执行增加按 run 冻结的弹性并发、独立异常恢复与显式解释器 profile。

## 弹性 Agent Terminal

- 每个 run 默认最多同时执行 8 个前台 Shell；`shellMaxConcurrentPerRun` 可在用户 YAML 配置为 1–32。
- 配置在 run 启动时冻结，热更新只影响后续 run。
- 未达到上限的 Shell 立即获得独立 Agent Terminal/PT​​Y；超过上限后按 FIFO 等待。
- 等待中的 Shell 在真正 dispatch 前不会发布 started 或执行中 checkpoint；任一 active 完成后会连续补满可用容量。

## Shell 输出与异常恢复

- 并发 stdout/stderr 通过带 `call_id` 与 `model_call_id` 的 keyed tool delta 发送，不再同时发送旧裸 Shell delta。
- skipped、transport close 与 Shell control throw 按 exec 独立进入 1.5 秒 grace；迟到的真实 Exit/Backgrounded 优先。
- grace 到期只收口匹配同一 `exec_id + message_id + generation` 的调用；tombstone 防止迟到事件重复结果。
- 单个 Shell 的拒绝、权限错误或无终态不会阻断同一 run 的其他 active Shell。

## 显式 Shell profile

- Shell 新增可选 `profile`：`auto`、`powershell`、`pwsh`、`cmd`、`git-bash`、`wsl`。
- `auto` 保持 Cursor 默认解释器兼容行为。
- 显式 profile 使用固定解释器安全启动器，通过 Base64/标准输入传递原始命令，避免 `$`、引号和多行命令被中间层再次解析。
- Windows launcher 使用不带路径引号的可执行命令名，兼容 PowerShell、cmd、Git Bash 与 WSL 宿主终端；macOS/Linux 使用原生 POSIX Base64 管道，不依赖 PowerShell。
- `cmd` 与 `wsl` 仅在 Windows 提供；`git-bash` 在 Windows 使用 Git for Windows Bash，在 macOS/Linux 使用系统 Bash；`powershell`/`pwsh` 仅在对应解释器已安装时可用。
- 请求的解释器不可用时，在进入 pending/active 调度前返回明确工具错误，不静默降级。

## 发布资产

- `cursor-byok-0.0.67-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`
  - 适用于 Windows 10/11 amd64。
- `cursor-byok-0.0.67-macos-arm64.tar.gz`
  - 内含 arm64 `Cursor助手.app`
  - 适用于 Apple Silicon Mac。
- `cursor-byok-0.0.67-macos-amd64.tar.gz`
  - 内含 x86_64 `Cursor助手.app`
  - 适用于 Intel Mac。