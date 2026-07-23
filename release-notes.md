# Cursor助手 v0.0.65

本版本是基于 v0.0.64 的子代理唤醒热修复，仅修复同一 request ID 跨 run 复用时的 BidiAppend 序号换代问题；v0.0.64 的 Shell 与子代理生命周期修复保持不变。

## BidiAppend 跨 run 序号

- append 序号状态按 request ID 与 run epoch 隔离；新 run 可从 `append_seqno=1` 重新开始。
- 只有通过重复检查并真正建立新 turn 的 run 才切换 epoch；RunSSE 普通重连及同 run 重复请求不会重置序号。
- 新 epoch 建立后，旧 epoch 的迟到事件会被视为 stale，不会污染当前 run；stale 流量也不会延长旧 epoch 生命周期。
- BidiAppend 诊断日志增加 `epoch`、`current_next` 与 `disposition`，便于区分同 run 重复与跨 run 换代。

## 子代理父流程唤醒

- 同一 request ID 启动后续 run 时，从 1 开始的最终 `SubagentResult` 不再于解码前被 stale 判定丢弃。
- 匹配的子代理最终结果仍保持幂等：只写入一次 `tool_result`、发布一次 `ToolCallCompleted`，并通知父流程继续。

## Windows 发布资产

- `cursor-byok-0.0.65-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`

适用于 Windows 10/11 amd64。
