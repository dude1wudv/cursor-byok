# Cursor助手 v0.0.59

本版本提升 GPT Responses 工具调用的完整性，并修复 Shell Rejected 关联、重复拒绝熔断与复杂 PowerShell 命令 metadata 误判问题。本次本地发布提供 Windows amd64 资产；推送标签后 GitHub Actions 将继续构建配置中的其他平台资产。

## GPT Responses 工具调用完整性

- 按 provider item/call 标识关联函数调用事件，兼容参数增量与完成快照，避免同一调用被拆分、重复或错配。
- 工具调用只有在名称、调用标识和完整 JSON 对象参数均有效时才会提交；残缺或冲突事件会明确失败，不再静默执行不完整参数。
- 模型调用摘要记录 function call 的事件、完成、丢弃与 pending 数量，便于定位上游协议异常。

## Shell Rejected 关联与熔断

- 同时校验 `exec_id` 与 `message_id`，拒绝关联不一致的执行结果，避免把 Rejected 状态记到错误的工具调用。
- Shell 派发记录 provider 标识以及命令、参数、工作目录指纹，确保拒绝状态可稳定关联到同一命令。
- 对相同 Shell 命令的重复 Rejected 进行本地阻断并设置有限熔断，防止模型反复提交同一被拒命令形成循环。

## PowerShell metadata 修复

- 含变量、管道、分号、引号、换行等复杂语法的 PowerShell 命令不再伪装成简单命令或生成不可靠的 parsing metadata。
- 简单命令仍保留最小 metadata，不影响普通 Shell 调用展示与审批。

## Windows 发布资产

- `cursor-byok-0.0.59-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`

适用于 Windows 10/11 amd64。
