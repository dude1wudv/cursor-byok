# Cursor助手 v0.0.82

本版本修复自定义子代理模型的可读名称解析，并保留 v0.0.81 的调度、权限契约和思考强度能力。

## 自定义子代理模型

- `Task.model` 现在可使用模型显示名或上游 `modelID`，不再要求父代理暴露或传递内部 channel ID。
- 可读名称命中后仍转换为稳定 adapter ID 执行，保持现有渠道路由和思考强度 variant 行为。
- 多个启用渠道使用相同显示名或 `modelID` 时会明确拒绝歧义选择，避免静默路由到错误渠道。

## 回归兼容

- 继续兼容显式 adapter ID 调用。
- 保留 `low`、`medium`、`high`、`xhigh`、`max` 全部子代理思考强度。
- 保留第一级 4 个、第二级 2 个活跃槽位、终态释放槽位及第三级禁止派发规则。

## 子代理树级调度

- 根任务派发的第一级子代理硬上限为 4，提示词默认建议只拆分 2 个互不重叠方向。
- 第一级子代理最多继续派发 2 个第二级子代理；第二级子代理禁止继续派发第三级任务。
- 调度预算按子代理树深度持久化，不再因 provider pass 切换而重置。
- Task `CallID` 重试、回放或重复 provider 事件保持幂等，不重复派发或计数。
- 拒绝原因明确区分 `subagent_depth_limit` 与 `subagent_level_budget_exceeded`。

## 模型与思考强度

- 删除 `fast` 子代理模型选项及其默认模型别名语义。
- 完整保留 `RequestedModel` 的 `parameters`、`max_mode`、内建模型和 variant 标志。
- 统一归一化 `thinking_effort`、`reasoning_effort`、`thinking_intensity` 与模型 variant suffix。
- 显式 `max` 和 `max_mode=true` 会传入实际 `SubagentArgs.model_id` 变体，不再被父任务默认 `high` 覆盖。
- runtime debug 记录请求、解析、实际应用的模型和思考强度。

## 文件修改权限契约

- Task schema 和运行时 prompt 均明确注入 `readonly=true/false` 的执行边界。
- 可写任务要求在同一次子代理任务中完成修改与验证，而不是先只读调查再重复派发。
- 子代理结果要求报告是否修改文件、修改路径、验证命令和结果。

## 模型目录

- available-model 目录正确暴露思考强度 variants、默认值和 `max` 支持。
- 子代理渠道 tooltip 补充角色对应关系和默认思考强度。

## 发布资产

- `cursor-byok-0.0.82-windows-amd64.zip`
- `cursor-byok-0.0.82-macos-arm64.tar.gz`
- `cursor-byok-0.0.82-macos-amd64.tar.gz`
- `update.json`
