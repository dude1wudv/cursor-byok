# Cursor助手 v0.0.55

本版本修复子代理执行链路的终态收口、Responses reasoning 末尾重复 Thinking，以及 Task 派发时父上下文缺失的问题。本次发布仅提供 Windows amd64 资产。

## 重点修复

- 子代理控制面不再提前收口：`Throw`、Stopped 等控制事件只记录传输或控制状态，Task batch 继续等待权威的 `SubagentResult`；迟到控制事件不会覆盖已完成结果，父对话取消后迟到结果也不会重新激活父流。
- 末尾 reasoning/Thought 去重：Responses 的 `completed`/`incomplete` 终态不会在最终正文之后再次发布已经输出的 reasoning summary，provider metadata/signature 仍保留用于历史一致性。
- Task 派发上下文补齐：子代理首次 bootstrap 会收到派发时快照中的 plan 文件路径（`plan_file_path`/`plan_file_uri`，有值才传）、`owned_paths`、`related_paths` 和当前用户现象/目标/关键约束摘要。
- 派发上下文只在子代理首次 bootstrap 持久化并按内容去重；计划正文继续使用已有 `CurrentPlanText` 快照，不重复塞入每次 prompt，路径数组会排序、去空和去重。
- 子代理契约明确这些字段是派发时快照，路径仅作为定位线索，不能将后续文件变化误认为父计划更新。

## 兼容性

- Task 工具新增字段均为可选，旧调用继续兼容。
- 现有 `promptHash + subagentType + model` 启动关联键保持不变。
- 不改变 Cursor protobuf wire 字段；历史 replay 和 prompt cache 的稳定前缀保持不变。

## Windows 发布资产

- `cursor-byok-0.0.55-windows-amd64.zip`

压缩包中包含：

- `cursor-byok-0.0.55-windows-amd64.exe`
- Windows 运行所需的发布资源

推荐使用 Windows 10/11 amd64。升级前建议备份本地配置文件和会话历史目录；升级后检查模型配置及子代理角色权限。

## 验证记录

发布前运行：

- `go test ./...`
- `go build ./...`
- 前端 `yarn build`
- `git diff --check`

尚未替代真实环境验证的部分：

- 真实 OpenAI Responses SSE provider 流
- 多 sibling 并发 Task 的线上端到端回归
- resume 与 timeout 混合场景的真实 Cursor 客户端回归