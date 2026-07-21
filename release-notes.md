# Cursor助手 v0.0.56

本版本修复 `Using Skill` 卡片在调用未知或隐藏 Skill 工具后永久 pending 的问题。本次发布仅提供 Windows amd64 资产。

## 修复内容

- 根因：未知或隐藏工具无法编码为 canonical ToolCall；旧逻辑仍发送空的 `tool_call_started`，客户端无法关联对应终态，因而一直显示执行中。
- fallback：此类调用不再发送空的 started/completed 事件，而是写入工具错误结果和 `unsupported_tool_invocation` 元数据，并触发流 reconcile，让模型继续处理或给出可见错误。
- OpenAI Responses 在 `response.incomplete` 早于工具参数完成事件时，会显式收口残留的 tool accumulator，并交由同一 pre-dispatch fallback 处理，避免工具意图滞留。

## Windows 发布资产

- `cursor-byok-0.0.56-windows-amd64.zip`
  - 内含 `cursor-byok-0.0.56-windows-amd64.exe`

适用于 Windows 10/11 amd64。