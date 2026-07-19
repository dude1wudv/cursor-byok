# Cursor助手 v0.0.54

本版本是一次面向 Agent 执行链路的大版本更新，重点修复了思考流显示、子代理调度、计划协作和 Multitask 收口问题，并完善了 Windows 发布构建流程。

## 重点摘要

- 修复 GPT Responses reasoning summary 重复显示，消除 `**内容****内容**` 和中间四个星号问题。
- 修复普通正文被错误归类到 Thinking 折叠区的问题。
- 修复仅返回加密 reasoning 时显示 `Thinking is encrypted. Please wait a moment.` 的误导性占位。
- 子代理现在可以继承父对话的当前计划和 Todo 快照，并将结果可靠地回传给父代理。
- 新增子代理角色路由，避免简单探索任务无条件继承父代理的 Sol High。
- 修复 Multitask 子任务全部完成后父对话被重复唤醒的问题。
- 完善旧模型配置兼容、运行时配置透传和 Windows 发布构建校验。

## 思考流与 GPT Responses 修复

### 1. Reasoning summary 显式启用

Responses 请求在启用 reasoning 时显式发送 `reasoning.summary: "auto"`，不再依赖模型或中转服务的默认行为。这样可以更稳定地获得可展示的 reasoning summary，同时仍保留加密 reasoning 内容用于后续续跑。

### 2. Summary 增量和终态快照统一去重

统一处理以下事件：

- `response.reasoning_summary_part.added`
- `response.reasoning_summary_part.done`
- `response.reasoning_summary_text.delta`
- `response.reasoning_summary_text.done`
- `response.completed` / `response.incomplete` 中的 reasoning summary

适配器按 reasoning item 和 summary index 追踪已发送文本。增量事件继续追加，done 或 terminal snapshot 只发送尚未出现的后缀，避免同一段 summary 被重复发送。

### 3. 正文与 Thinking 分类边界修复

现在只有 provider 原生 reasoning 事件进入 Thinking：

- Responses 的 `reasoning_summary_*` 和 `reasoning_text.*` 进入 Thinking。
- Chat Completions 的 `reasoning_content` 进入 Thinking。
- Responses `output_text`、`output_text.delta`、普通 output content 进入正文。
- Chat Completions 的普通 `content` 进入正文。

停用了普通 content 的 `<think>...</think>` 兼容解析路径，避免模型正文中出现类似标签或跨 chunk 标签时被错误折叠到 Thinking 区域。

### 4. 移除误导性加密占位

当 provider 只返回 encrypted reasoning signature、没有公开 summary 时，不再向 Cursor 发送固定英文占位文本，也不创建空 Thinking 块。

同时继续保存以下信息，用于工具调用后的 Responses continuation：

- reasoning signature
- signature source
- provider item ID
- provider status
- provider summary

历史 reasoning、signature 和 replay 顺序保持不变，不影响已有会话上下文和 prompt cache。

## 子代理计划协作

### 1. 子代理读取父计划

父对话派发 Task 时，会冻结当前计划快照，包括：

- 当前 plan 文本
- plan registry
- Todo 列表
- 计划版本和稳定 hash
- 父 Todo / dispatch 关联信息

子代理首次启动时将快照注入自身结构化 prompt state，因此可以看到 `<current_plan>` 和 `<todo_list>`。Task prompt 仍作为独立的当前任务指令传入，不把动态计划文本重复拼接到每次请求中。

### 2. 子代理结果回写父计划

子代理不再直接并发写父对话 Todo，避免多个 sibling 互相覆盖。子代理返回结果时携带计划版本、dispatch ID、attempt、执行结果和验证信息；父代理完成验收后再通过幂等逻辑更新父 Todo。

以下结果不会重复推进父计划：

- 重复 Task result
- 迟到 result
- 已关闭 Task 的 result
- 属于旧 plan hash 的 result
- 属于旧 attempt 的 result
- 无法可靠匹配父 Todo 的 result

失败和超时结果仍会保留为 blocked/失败状态，不会被错误标记为已完成。

## 子代理角色化调度

模型配置中的“允许作为子代理模型”现在支持多选角色：

- `simple_explore`：简单快速探索
- `medium_explore`：中等定位探索
- `complex_debug`：复杂 Debug 探索

同一个模型可以同时承担多个角色，模型候选按配置列表顺序选择。

### 默认调度策略

| 角色 | 默认模型倾向 | 默认 reasoning effort | 适用场景 |
| --- | --- | --- | --- |
| simple_explore | Luna | Low | 文件定位、关键词搜索、简单事实收集 |
| medium_explore | Terra | Medium | 跨文件调用链、普通问题定位、兼容性调查 |
| complex_debug | Sol | Medium | 复杂状态机、跨模块修复、需要综合判断的 Debug |

调度规则：

1. Task 显式指定 `model` 时优先使用显式模型。
2. 未指定模型时，从已勾选该角色的模型中按配置顺序选择第一个。
3. 未指定 `thinking_effort` 时使用角色默认强度，不再无条件继承父代理的 High。
4. 定位和探索任务不再默认使用 Sol High。
5. 父代理仍负责最终结论、方案综合和高风险修改决策。

旧配置兼容：

- 旧配置 `subagentEnabled: true` 且没有角色列表时，自动迁移为三个角色全选。
- `subagentEnabled: false` 或缺失时迁移为空角色列表。
- 保存时继续维护旧布尔字段，避免旧版本运行时读取失败。

## 子代理二次派发限制

只有 `complex_debug` 子代理可以继续派发 Task，并且二次派发只能选择：

- `simple_explore`
- `medium_explore`

简单探索和中等探索子代理不会暴露 Task 工具；服务端也会进行最终校验，防止通过手工构造参数绕过限制。角色信息会写入 launch correlation、子会话状态和调度 metadata，resume 后仍然有效。

## Multitask 收口修复

为并行子任务增加批次级状态：

- batch generation
- Task member
- terminal source
- parent notified

真实完成、失败、无结果 fallback、lease timeout 和 maximum runtime timeout 都只能首次将成员推进终态。只有当同一批次从“仍有运行成员”转为“全部终态”时，父代理才会被唤醒一次。

重复 result、迟到 result、旧 timer 和重复 reconcile 不会再次触发父代理。显式 resume 会创建新 generation，因此新一轮任务完成后仍可以合法唤醒一次。

## 配置与运行时兼容

- `subagentRoles` 已贯通前端配置、YAML、服务端 Manager、legacy runtime snapshot 和运行时模型列表。
- Task 的 `task_role` 通过 JSON schema 和服务端 metadata 实现，不修改已有 Cursor protobuf wire 字段。
- 旧 Task result 和旧会话 replay 继续兼容。
- 计划快照、Task result 和批次 metadata 不记录 API key、模型正文或其他敏感信息。

## Windows 发布资产

本版本发布 Windows amd64 资产：

- `cursor-byok-0.0.54-windows-amd64.zip`

压缩包中包含：

- `cursor-byok-0.0.54-windows-amd64.exe`
- Windows 运行所需的发布资源

推荐使用 amd64 Windows 10/11。首次运行前请检查模型配置和子代理角色权限；旧配置会自动兼容迁移，但建议在“模型编辑”中确认 Luna、Terra、Sol 分别承担的角色。

## 验证记录

本版本已完成：

- `go test ./...`
- `go build ./...`
- 前端 `yarn build`
- 五种模式 Task schema 一致性检查
- 六套 prompt tools JSON 解析检查
- `git diff --check`

尚未替代真实环境验证的部分：

- 真实 OpenAI Responses SSE provider 流
- 多 sibling 并发 Task 的线上端到端回归
- resume 与 timeout 混合场景的真实 Cursor 客户端回归

## 升级注意事项

1. 升级前建议备份本地配置文件和会话历史目录。
2. 升级后检查模型配置中的“允许作为子代理模型”及三个角色能力。
3. 如果某个角色没有可用模型，Task 会返回明确错误，不会静默继承父代理模型。
4. 父代理的计划进度以父会话为准，子代理负责执行和报告，最终由父代理统一更新 Todo。