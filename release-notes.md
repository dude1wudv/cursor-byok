# Cursor助手 v0.0.61

本版本修复 Cursor Shell 协议兼容问题，并收敛前台子代理 Task 的 checkpoint 投影，避免重复 reasoning 泄漏成无锚点 `Thinking`。

## Cursor Shell 兼容

- 所有 Shell 请求始终携带 Cursor 需要的 `parsing_result`。
- 复杂 PowerShell 语法使用 `parsing_failed=true`，不再伪造简单命令解析结果，也不改写原始命令。
- Cursor 拒绝 Shell 后在当前 turn 打开有限 circuit，阻止模型继续派发不同命令并产生多张 `Skipped` 卡；下一 turn 与非 Shell 工具不受影响。
- transport incomplete 继续走原有 recovery，不误判为权限或策略拒绝。

## Subagent Task UI

- 非持久化 Task 的 reasoning 不再提前写入稳定 checkpoint turns。
- pending Task 只通过 `PendingToolCalls` 携带一次 reasoning 与原 call ID，避免 Task 卡片下方出现重复裸 `Thinking`。
- Task 最终 success/error/abort 映射和父会话恢复链保持不变。

## Windows 发布资产

- `cursor-byok-0.0.61-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`

适用于 Windows 10/11 amd64。
