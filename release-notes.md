# Cursor助手 v0.0.85

## 子代理模型显示

- Task 子代理统一显示为完整模型名加思考强度，例如 `gpt-5.6-luna (max)`；未指定强度时仅显示完整模型名。
- UI 只展示正式 `ToolCallStarted` 事件，避免 partial 与 started 参数差异形成伪重复子代理。
- 展示副本与执行数据继续分离，不修改底层 adapter ID、`CallID`、history 或 checkpoint。

## 自然语言模型匹配

- 支持按已启用模型的 `gpt-5.6-<alias>` 短名派发，例如 `luna`、`sol`、`terra`。
- 用户说“派发 luna max”时，Task 会解析为 Luna adapter 与 `max` 思考强度。
- 兼容 `model=luna + thinking_effort=max`、`luna:max` 和 `luna max` 三种形式。
- 匹配仅限已启用模型；同名渠道会明确报歧义，不进行包含匹配、拼写猜测或静默回退。
