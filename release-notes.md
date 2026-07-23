# Cursor助手 v0.0.63

本版本修复子代理操作任务被误派为只读的问题，并包含 OpenAI Responses 与 Cursor Shell 协议兼容改进。

## 子代理派发

- Task 使用必填 `access_mode` 区分只读调查与实际操作。
- Shell、进程启动、编辑、构建、测试和部署任务统一使用 `act + generalPurpose`，首次派发即可获得执行工具。
- 只读 `generalPurpose` 仍可通过 `inspect` 使用；旧 `readonly` 请求保持兼容，冲突参数会在执行前明确拒绝。
- Task 工具描述只公开后端真实支持的子代理类型。

## OpenAI Responses 兼容

- GPT Responses 请求不再发送部分上游不支持的 `max_output_tokens`。
- 非 GPT Responses 请求保留原有输出上限参数。
- 请求诊断参数与实际 outbound JSON 使用同一兼容规则。

## Cursor Shell 兼容

- Shell 请求始终携带 Cursor 需要的 `parsing_result`。
- 复杂 PowerShell 语法使用 `parsing_failed=true`，不改写原始命令。
- Cursor 拒绝 Shell 后在当前 turn 打开有限 circuit，阻止连续产生 `Skipped` 卡。
- transport incomplete 保持原有 recovery 行为。

## Windows 发布资产

- `cursor-byok-0.0.63-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`

适用于 Windows 10/11 amd64。
