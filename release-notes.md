# Cursor助手 v0.0.84

## Task 子代理显示修复

- 同一 Task `CallID` 的 `PartialToolCall` 与 `ToolCallStarted` UI 事件统一使用可读的渠道标签（`luna`、`sol`、`terra`），防止 Cursor UI 误显示伪重复子代理。
- 底层 adapter ID 路由、执行模型和 `CallID` 幂等保持不变；仅 UI 发布副本改写，不代表真实执行两次。
