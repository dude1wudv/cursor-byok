# Cursor助手 v0.0.68

本版本修复 GPT-5.6 / OpenAI Responses 与 Cursor Multitask 的兼容性问题，重点稳定 worker 收口、会话模型身份和工具协议。

## Multitask 收口

- 单个 coherent worker 返回非空成功结果且没有 sibling、阻塞或待协调工作时，直接完成当前 turn，不再触发父模型第二次 provider pass。
- 多 worker、失败、取消、后台脱离或阻塞仍保留父级 continuation，用于必要综合与异常处理。
- background ack 只释放前台等待，不再被视为 worker 最终结果。

## 会话模型固定

- 根会话在 `state.json` 持久化稳定的 `selected_model_adapter_id`，恢复请求不会因模型为空或使用 `default`、`auto`、`fast` 而回落到配置首项。
- 旧状态文件无需迁移即可读取；缺少新字段时会从最近可识别的根 RunRequest 恢复渠道，无法可靠判断时返回明确错误。
- 删除 Grok 到 Luna 的静默回退，避免模型身份错误被其他渠道掩盖。

## Responses 与提示词协议

- 保留 OpenAI Responses 的 `call_id`、reasoning item 和 typed SSE 回放，移除硬编码本机路径的永久调试写入。
- 当前用户请求、模式、Plan/Todo 快照和动态提醒改为 latest-only suffix，不再持续写入历史，稳定 provider 请求前缀。
- Multitask 初始工具缩减为协调所需集合；nested `medium_explore` 强制使用 `access_mode=inspect`。

## 工具卡与配置界面

- 自动 `task_role` 路由会先解析 worker 渠道，再发送进行中的 Task 工具卡；`model` 为空时不再丢卡。
- Task 卡片只表达 worker 使用的模型，不代表父会话模型切换。
- 模型配置页区分稳定渠道、provider model 和子代理角色，并明确配置首项只影响新会话默认选择。

## 发布资产

- `cursor-byok-0.0.68-windows-amd64.zip`
- `cursor-byok-0.0.68-macos-arm64.tar.gz`
- `cursor-byok-0.0.68-macos-amd64.tar.gz`
- `update.json`
