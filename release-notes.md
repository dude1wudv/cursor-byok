# Cursor助手 v0.0.66

本版本在 v0.0.65 的子代理与 BidiAppend 修复基础上，统一 OpenAI 请求的客户端标识，并同步 Task 工具 schema。

## Codex Desktop 请求标识

- OpenAI Chat Completions 与 Responses 请求使用 Codex 原生 `Codex Desktop/<版本>` User-Agent 前缀。
- 启动后从本机已安装的 Codex Desktop 内置 CLI 自动读取版本；读取失败时使用构建时验证的版本回退值。

## Task schema

- 根模式的 Task 工具要求显式填写 `access_mode`，并限制为 `inspect` 或 `act`。

## BidiAppend 跨 run 序号

- append 序号状态按 request ID 与 run epoch 隔离；新 run 可从 `append_seqno=1` 重新开始。
- 只有通过重复检查并真正建立新 turn 的 run 才切换 epoch；RunSSE 普通重连及同 run 重复请求不会重置序号。
- 新 epoch 建立后，旧 epoch 的迟到事件会被视为 stale，不会污染当前 run；stale 流量也不会延长旧 epoch 生命周期。
- BidiAppend 诊断日志增加 `epoch`、`current_next` 与 `disposition`，便于区分同 run 重复与跨 run 换代。

## 子代理父流程唤醒

- 同一 request ID 启动后续 run 时，从 1 开始的最终 `SubagentResult` 不再于解码前被 stale 判定丢弃。
- 匹配的子代理最终结果仍保持幂等：只写入一次 `tool_result`、发布一次 `ToolCallCompleted`，并通知父流程继续。

## Windows 发布资产

- `cursor-byok-0.0.66-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`

适用于 Windows 10/11 amd64。
