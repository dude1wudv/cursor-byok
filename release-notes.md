# Cursor助手 v0.0.64

本版本同步 Cursor 3.12.17 执行协议，并修复 PowerShell/Shell 被误显示为 `Skipped`、运行中的子代理被误显示为 `Stopped` 的生命周期兼容问题。

## Cursor 3.12.17 协议

- 同步 `ShellArgs.conversation_id = 21`，确保 Shell 请求与 started/completed 投影使用同一会话关联。
- 同步 `ConversationStateStructure` 字段 30/31 及 `SubagentRunState`、`SubagentRunStatus`，为客户端提供可重放的子代理运行状态。

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

- Shell 请求始终携带 Cursor 需要的 `parsing_result`，复杂 PowerShell 语法保留原文并仅标记 `parsing_failed=true`。
- 同一 provider pass 的前台 Shell 采用 single-flight/FIFO，前一条收到真实终态后才派发下一条。
- `Skipped` 审批提示、空 payload、未知事件、transport close 和 stall 不再合成成功或失败终态；迟到的匹配执行结果仍可正常收口。
- 只有匹配的 exit/backgrounded、确认未执行的拒绝或显式取消才完成 Shell；权威拒绝后的队列会返回明确的本地 blocked 结果。

## 子代理生命周期

- Task 派发、后台运行及真实结果分别投影为 `RUNNING`、`BACKGROUNDED`、`SUCCESS/ERROR`，只有匹配当前运行代次的显式用户取消才投影为 `ABORTED`。
- 后台空回执与 `TASK_FINISHED` 无结果仅作为进度/完成提示，不再把仍运行的子代理显示为 `Stopped`。
- 匹配的迟到最终结果可幂等更新 checkpoint；错误 ID、旧 generation、transport close 与 lease/grace timeout 不再伪造终态。

## Windows 发布资产

- `cursor-byok-0.0.64-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`

适用于 Windows 10/11 amd64。
