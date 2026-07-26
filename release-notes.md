<!-- 发布约定：每次 Release 都保留“最近 5 个版本更新梗概”，覆盖当前版本和前 4 个版本。 -->

# Cursor助手 v0.0.73

本版本收口运行时与 Grep 契约：修复 Task 子代理在 Cursor UI 错显 Stopped、硬化 Shell 确定性拒绝熔断并补齐 FIFO 证据、按端点能力发送 `parallel_tool_calls` 并记录真实派发批次宽度、统一 provider pass 指标契约并对全部 debug 日志脱敏、闭合 Grep offset/head_limit 的 applied 回报契约并固定多 workspace 截断顺序、把自动压缩性能软阈值上调至 70%。

## Task 子代理 UI 状态修复

- 历史与内部投影保持真实 `RUNNING → SUCCESS` 时间线不变；仅在发给 Cursor 的 checkpoint 副本上把未终态 `RUNNING` 投影为 `BACKGROUNDED`（Cursor 3.12.17 会清理等待外部结果的普通 loading 卡片，只有 `BACKGROUNDED` checkpoint 能保持运行展示）。转换不落盘，reconnect 后经同一 checkpoint 出口再次兼容转换。
- 真实状态语义不变：真实 background ack 才持久化 `BACKGROUNDED`；结果映射 `SUCCESS/ERROR`；仅显式取消映射 `ABORTED`；同代终态与新代状态不被迟到事件降级。运行中的 Task 不再错显 Stopped，最终只出现一次 Completed。

## Shell 确定性熔断硬化与 FIFO 证据

- 确定性拒绝指纹统一为单一定义 `tool_name + canonical_args（规范化 command + cwd）+ validation_error_class`，pre-dispatch 校验拒绝、terminal 拒绝、reset 清零与账本重放全部经同一函数，时间线固定：第一次拒绝记账、第二次同指纹开路（附纠正指引）、第三次本地终止 provider 循环；后两次不派发 Shell exec。`git config` 仍被 inspect 只读白名单拒绝。
- shell FIFO 补齐可观测性（不改调度算法）：单调 queue/dispatch 事件序号、入队位置与队列深度、活跃数与上限、exec ID、入队与派发时的 command/cwd hash，可完整还原入队→出队→开始→完成顺序，并定位 cwd 漂移发生在服务端还是客户端。

## Responses 并行与真实 `parallel_width`

- `parallel_tool_calls` 改为端点/适配器能力判断：官方 Responses 预设端点（`/v1/responses`）+ 已验证模型族（gpt-5.6）自动开启；`/custom` 兼容端点默认不发送；extra params 显式设置始终优先（可强开/强关）。
- pass 指标的 `parallel_width` 改为“本 provider pass 成功接受并派发的唯一外部工具批次宽度”：在有效派发点按 tool_call_id 累计冻结，不再用 provider 结束瞬间的 pending 数量推算（快工具结束时 pending=0 曾导致宽度恒为 0），预派发拒绝不计入。同一 pass 的 4/8 个快速 Read 现在精确报告 4/8。
- 保留“同 pass 所有外部工具终态后只恢复一次”的恢复门与重试文本恢复防重语义。

## Provider 指标契约与 debug 日志脱敏

- `provider_pass_metrics` 收敛为单一收口入口：每个 pass 在其终态分支恰好发出一条字段齐全事件（编译耗时、回放消息数、估算/实际 tokens、工具结果字节、TTFT、pass 时长、外部等待、工具数、parallel_width、cache read/write），新增 `terminal_state` 覆盖 completed/failed/interrupted/retry_scheduled/cancelled。
- debug_recorder 成为统一脱敏落盘边界：`provider.jsonl` 不再落 request body 与 raw chunk；`runtime.jsonl` 不再落 `latest_user_text`；Bidi/RunSSE 不再保存可还原提示词或工具正文的 protobuf JSON/hex——统一替换为字节数 + SHA-256 摘要。debug 文件只保留模型、端点、状态、计数、长度、哈希、字段开关、错误分类与关联 ID；`context.json` 会话恢复不受影响。

## Grep 分页契约与确定性截断

- 闭合“请求参数—客户端 applied 回报”契约：请求显式传入 `offset/head_limit` 时，逐 workspace 比对三种结果（content/count/files）的 `offset_applied`/`head_limit_applied`；字段缺失或值不一致时不再静默返回首页，而是在 ToolResult 注入结构化 `[pagination_contract]` 警告（含请求值、实际值/缺失状态、output mode、workspace 与单位语义：content 按匹配项、count/files 按文件），旧客户端只告警不终止会话。工具描述同步降级为条件性承诺并要求检查该警告。
- 多 workspace 截断确定性：共享回放预算按 workspace 路径排序分配，active editor 固定最后处理；相同输入重复执行，保留的 workspace/文件/匹配与提示逐字一致。
- 截断提示透明化：区分并报告单条 2 KiB、每文件 100 匹配、全调用 300 匹配、全调用共享 32 KiB 四层限额，同时报告本结果进入前剩余额度、当前剩余额度与最终保留量，并标注 bridge 来源与上游 client/ripgrep 截断旗标；count/files 超过 300 项时输出保留数、总数与原因。bridge 32 KiB 与 forwarder 16 KiB 上限本版不变。

## 自动压缩 70% 软阈值

- 性能软阈值 `0.55 → 0.70`：200k 窗口下 140000 不触发、140001 才触发；有基线时仍需再增长至少 20% 窗口（40000 tokens）才允许再次触发。硬防溢出、preflight、pending、摘要生成与持久化逻辑不变；512 KiB 回放预算、缓存前缀稳定与迟滞语义不变。

## 最近 5 个版本更新梗概

### v0.0.73

- 修复 Task 子代理在 Cursor UI 错显 Stopped（客户端兼容投影 BACKGROUNDED，历史保持真实 RUNNING），硬化 Shell 确定性拒绝熔断并补齐 FIFO 排序证据。
- 按端点能力发送 `parallel_tool_calls`、在有效派发点记录真实 `parallel_width`，统一 provider pass 指标契约并对全部 debug 日志脱敏；闭合 Grep applied 分页契约、固定多 workspace 截断顺序，自动压缩软阈值上调至 70%。

### v0.0.72

- 修复 inspect Shell 合法命令在 pre-dispatch 阶段被拒绝而导致的 “Skipped git” 刷屏，并通过指纹熔断阻止确定性错误无限重试。
- 精简 Shell 调度与恢复状态机，加入 OpenAI Responses 并行工具调用、稳定回放预算、自动压缩软阈值和 provider pass 性能指标。

### v0.0.71

- 修复 Claude extended thinking、`pause_turn`、SSE 错误映射及历史回放兼容问题，避免对话提前结束或隐性 400。
- 为客户端工具与交互工具增加超时收口，修复 Plan 卡片渐进显示、thinking 参数兼容和 Grep 截断异常。

### v0.0.70

- 统一 Shell 活动迁移、两阶段 abort、终态所有权和指纹熔断，避免旧 deadline、双收口与重复拒绝循环。
- 对齐 inspect 权限、Task 模式投影和 subagent 派遣终态，并为运行日志补充构建版本与提交身份。

### v0.0.69

- 恢复稳定的请求缓存前缀，撤回会破坏 prompt cache 的 latest-only suffix 编译方式。
- 通过 awaiting-start 门和 FIFO 调度缓解并发 Shell 在 Cursor 终端分配阶段被 skipped 的问题。

## 发布资产

- `cursor-byok-0.0.73-windows-amd64.zip`
- `cursor-byok-0.0.73-macos-arm64.tar.gz`
- `cursor-byok-0.0.73-macos-amd64.tar.gz`
- `update.json`
