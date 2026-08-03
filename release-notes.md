<!-- 发布约定：每次 Release 都保留“最近 5 个版本更新梗概”，覆盖当前版本和前 4 个版本。 -->

# Cursor助手 v0.0.80

本版本合并上游最新变更，并修复 Sub2API、DeepSeek 等 OpenAI-compatible Responses 渠道因不支持扩展缓存字段而返回 HTTP 400 的兼容性问题。

## 上游合并与兼容性

- 合并上游 `upstream/main@639c452`，保留本地 Shell、子代理、Await、BYOK、配置保护和缓存诊断逻辑。
- 同步生成 Go proto、前端 bindings 与 dist，纳入 Cursor account、debugger、i18n 等上游能力。

## Responses 缓存兼容修复

- GPT 系列的 OpenAI-compatible 请求体和 User-Agent 回归 `upstream/main@639c452` 写法：保留工具原顺序，仅发送 `reasoning.effort`、`reasoning.encrypted_content` 与稳定的 `prompt_cache_key`，不再自动注入 `reasoning.summary`、`parallel_tool_calls`、`service_tier`、`prompt_cache_options` 或嵌套 `cache_control`。
- DeepSeek、MiniMax、Grok 等非 GPT 模型不再携带 `prompt_cache_key`；历史 replay 或额外参数中的不兼容显式缓存扩展会在发往上游前被清理。
- 保留稳定 replay 前缀和 `partial_cache_plateau` 诊断，用上游可接受的隐式前缀缓存继续解决缓存连续性问题。
- 修复 `KvClientMessage.set_blob_result` 未路由到 checkpoint Blob ACK 处理器的问题；客户端已返回的 Blob 写入成功结果不再被忽略，第二次请求不会在 10 秒后误报 `checkpoint_sync_error: 2 checkpoint blob writes timed out` 并中断 provider。
- 新增 GPT 上游请求形状、Sub2API/DeepSeek 缓存兼容和 checkpoint Blob ACK 路由回归测试。

# Cursor助手 v0.0.79

本版本修复新版 Cursor 子代理派发与并发收口兼容，增强 GPT Responses 缓存连续性诊断，并新增安全、可恢复的 Cursor 自动更新控制。

## 子代理派发与并发隔离

- Task 派发记录显式区分 requested/effective thinking effort 及来源；子会话启动时以父 Task 的有效值覆盖 Cursor 模型默认值，覆盖 disabled/low/medium/high/xhigh/max、角色默认和父级继承。
- 父会话屏障由“最新 batch”提升为同一父请求全部 live child：跨 provider pass 的兄弟子代理全部 terminal 或明确 background-detach 后才允许父代理恢复。
- 子代理 finalization key 加入 provider generation；取消记录 user_cancel/new_message_supersede/explicit_cancel_subagent/orphan_disconnect 等来源，迟到或错代结果只幂等收口对应成员。

## 只读 Git 与 Skipped 恢复

- policy 与 Cursor exec bridge 统一使用引号感知的单命令解析，带空格 Windows 路径和 `--` pathspec 不再被桥接层误标为 `ParsingFailed`。
- 新增严格只读的 `show-ref`、`for-each-ref`、`rev-list`、`name-rev`、`symbolic-ref`、`cat-file`；继续拒绝网络/写操作、`-c`/`--config-env`、external diff/textconv、`diff --no-index`、管道和重定向。
- 安全命令的有限 Skip recovery 与 bridge 分类保持一致，真实失败和未知执行状态仍保持可见。

## GPT Responses 缓存连续性

- OpenAI Responses 请求新增低敏 cache frontier：按 instructions 段、input、tools、reasoning/include 和额外参数记录哈希、字节数、最长稳定前缀及首个变化路径，不记录正文、密钥或图片内容。
- `prompt_cache_key` 继续稳定为 root/child conversation 各自的 `cursor:<conversation_id>`，显式 override 优先；工具定义按名称确定性排序。
- Plan 与 Agent 使用相同的 provider 工具 superset，真实权限仍由 pre-dispatch gate 强制，Plan 中 Write/Patch/Delete 继续被拒绝。`provider_pass_metrics` 新增 cache break、mode/tools/replay boundary 和 prefix match 诊断。

## Cursor 自动更新开关

- 新增 `disableCursorAutoUpdate` 配置和设置页开关；启用后写入 `update.mode=manual`，Windows 同时写入 `update.enableWindowsBackgroundUpdates=false`，保留手动更新入口。
- Cursor `settings.json` 改为串行原子补丁：无效 JSONC 直接报错并保留原文件；关闭开关或停止代理时只恢复仍等于本程序最后写入值的键，不覆盖用户后续修改。
- 代理设置与更新策略分组保存、独立恢复；停止本地代理不会改变自动更新策略。

# Cursor助手 v0.0.78

本版本抑制新版 Cursor 对只读 inspect Shell 的 UI 刷屏：`git status` / `tasklist` 等安全命令被客户端 `Skipped by Cursor` 时，不再以 Rejected 错误态反复显示 “Skipped git/tasklist”。

## Skipped git/tasklist UI 抑制

- 扩展只读 inspect 白名单判定（`IsSafeInspectShellCommand`），覆盖 `git status --short`、`tasklist` 及常见只读 git 变体。
- Shell recovery 对安全 inspect 的最终 Skipped 投影为 silent/backgrounded success（`shell_id=0`），模型仍收到 “status is unknown” 文本，但 live UI 不再渲染 Rejected。
- Checkpoint 投影新增 `sanitizeSkippedGitTasklistEntries`：剥离 silent inspect 的 tool_call/tool_result，reconnect 后不再回放 “Skipped ...”。
- 非安全命令（如 `git commit`/`git push`）的 Skipped 仍保持显式 Rejected/unknown，便于发现真实失败。

# Cursor助手 v0.0.77

本版本完成以下核心修复：
- Invalid turn 原因分类：新增 turn_finalized.reason 字段，支持 cancelled/provider_error/client_disconnect/timeout 等分类，证明 791 个 invalid turn 中大部分为正常取消。
- Lint 基线清理：fix 98 个 lint 问题（errcheck/staticcheck/unused 等）。
- CI 覆盖增强：新增 Go 测试、go vet、golangci-lint、Cursor 3.13.21+ 端到端 invalid-turn 统计与协议兼容性验证。
- Subagent 思考强度兼容新 Cursor：更新 thinking_effort 派发逻辑，支持 Cursor 3.13.21+ 的 effort 选择，避免 fallback 到 default minimum。

# Cursor助手 v0.0.76

本版本完成 Cursor 3.13.21 本地模式兼容性收口：支持 BidiAppend 二进制载荷与新版 exec/protocol 字段，区分 Shell 未启动、已运行和后端不可用终态，隔离子代理父代理唤醒与子代理完成，并收紧 Await、MCP structured content 与 latest-only prompt context 的处理边界。

## Bidi 二进制载荷与新版协议字段

- BidiAppend 优先解码新版 `data_binary`，兼容旧版 hex `data`；两种载荷同时出现且不一致时拒绝请求并保留冲突证据，避免静默解析为空。
- 补齐 Cursor 3.13.21 的 exec 结果分支、Shell hook context、sandbox unsupported、输出裁剪、Await/SubagentAwait、交互与 checkpoint 扩展字段，并通过 descriptor field-number 回归测试锁定编号。
- `client_supports_send_to_user` 等能力字段进入 metadata，旧客户端继续走兼容 fallback，不改变已支持的 legacy wire 格式。

## Shell 后端不可用与恢复语义

- `Skipped` 或无 Exit 的 stream close 在确认 `Start/stdout/stderr/hook_context` 前进入 recovery candidate/uncertain，不再直接完成或伪造 exit-0 成功。
- 仅服务端可证明尚未启动且安全的只读命令允许有限重试；迟到 Exit 按 `exec_id/message_id/attempt/generation` 隔离，已观察到执行活动的命令禁止重放。
- `ShellStreamHookContext` 与 `ShellSandboxUnsupported` 进入统一终态判定，明确区分 transport 未发送、backend unavailable、permission/sandbox failure 和真实命令退出。

## 子代理 Await 与上下文边界

- 拆分父代理可继续与子代理真实终态：background ack 只释放父代理等待，不写入子代理完成结果；只有权威 success/error/aborted 才能单次 finalization。
- `Await`/`SubagentAwait` 的 `still_running` 只续租或安排轮询，不重复唤醒 provider；迟到结果按 agent、tool call、generation 和 lease 关联。
- MCP structured content 保留原始结构，request context 的 dynamic/latest-only 内容不污染 replay history，稳定契约继续参与持久化与压缩。

## 上一版本 v0.0.75 详细记录

## Shell opening 背压与安全恢复

- 拆分 opening 与 running 租约：同一会话最多只有一个 Shell 等待原生 `Start/stdout/stderr`，收到真实活动后才开放下一项；running 默认并发降至 8，避免向 Cursor 原生执行扩展突发分配大量终端。
- `Skipped`、transport close 与 control throw 先进入短 grace，迟到活动可以恢复原 attempt；新 attempt 前请求中止旧 attempt，并继续按 `exec_id/message_id` 隔离迟到事件。
- 自动重试仅允许服务端可验证的只读命令；写入、部署、提交以及无法分类的命令不自动重放，达到 attempt 上限后也会生成唯一模型可见终态。

## 持久化优先与后台终端租约

- Shell 终态先持久化 `tool_result`，成功后才清理 pending、写 tombstone、释放租约并推进队列；持久化失败会保留并重放原始 Exit 或 `backgrounded` 终态，不再丢失退出信息或 `shell_id`。
- pre-dispatch 拒绝使用显式 Shell rejected 结果，区分“未发送”“transport skipped”和“已执行失败”。
- 后台 `shell_id` 在 `AwaitShell`/`WriteShellStdin` 时续租；终态句柄按最后观察时间保留并回收，客户端确认 shell 不存在时立即失效。

## 上一版本 v0.0.74 详细记录

本版本重构 Cursor Shell 调度，并修复 BidiAppend 重连时 append 代际未接管：默认 Shell 并发提升至 32；未启动即被客户端 `Skipped` 的 transport 不再直接失败，而是在同一逻辑 tool call 下退避重排队；等待项通过 started + checkpoint 保持 pending/loading，旧 attempt 的迟到事件按 transport generation 隔离。

## BidiAppend 重连代际接管

- 重连后的 `seq=1` 只有在 run/prewarm 成功解码并成功分发后才提交新 append epoch；duplicate run 虽不重复启动 provider，也会完成代际接管。
- 代际提交从仅新 run 的业务路径移到 BidiAppend 成功分发边界；解码或分发失败仍回滚候选，旧 epoch 的迟到消息继续隔离。
- `bidi_append_epoch_switched` 增加旧 epoch/next、新 epoch/next、duplicate run 与 reconnect 等非敏感证据，便于确认后续 `seq=2` 进入新代而非被判 stale。

## Shell 32 并发与 Skipped 重排队

- `shellMaxConcurrentPerRun` 默认值由 8 提升到 32；服务端队列持续填充可用槽位，等待项不占 active transport 配额。
- Shell 首次入队先发布唯一一次 `ToolCallStarted` 与 checkpoint，再下发 transport；排队和重试期间逻辑卡片保持 pending，最终只发布一次 completed。
- 仅对从未观察到 `Start/stdout/stderr` 的 `Skipped` 安全重派：每次生成新的 `exec_id/message_id`，最多 5 次，采用 250ms 起步、4s 封顶的有限指数退避并进入队尾，避免重试风暴和队首阻塞。
- retired attempt 的迟到事件由 stream 生命周期 tombstone 吸收；若已有启动或输出证据则禁止重派，继续走原有 abort/recovery，避免有副作用命令重复执行。
- 运行时事件新增逻辑 Shell ID、transport attempt、retry deadline 和可重试判定，便于还原 waiting→dispatch→skipped→retry→terminal 链路。

## 上一版本 v0.0.73 详细记录

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

## Anthropic 思考强度修复

- 移除渠道解析中硬编码的 4096 thinking budget：`ThinkingBudgetTokens` 不再随渠道默认下发，Anthropic adaptive thinking 完全由 `output_config.effort`（默认 xhigh）决定，思考深度不再被固定预算截断；新增适配器与渠道解析回归测试锁定该行为。

## 自动压缩 70% 软阈值

- 性能软阈值 `0.55 → 0.70`：200k 窗口下 140000 不触发、140001 才触发；有基线时仍需再增长至少 20% 窗口（40000 tokens）才允许再次触发。硬防溢出、preflight、pending、摘要生成与持久化逻辑不变；512 KiB 回放预算、缓存前缀稳定与迟滞语义不变。

## 最近 5 个版本更新梗概

### v0.0.78

- 只读 inspect Shell 被 Cursor 最终 Skipped 时改用 silent/backgrounded UI 投影，模型侧仍保留结果未知语义。
- 安全分类严格拒绝管道、重定向、命令连接符等复杂语法，并识别服务端注入的 `git --no-pager --no-optional-locks`。
- Checkpoint 仅剥离带显式 `shell_id=0` sentinel 的合成结果，避免误删真实无 ID 后台 Shell。

### v0.0.77

- 增加 invalid turn 原因分类，清理 lint 基线并扩展 Go/vet/golangci-lint 与 Cursor 3.13.21+ CI 覆盖。
- 更新 Subagent `thinking_effort` 派发逻辑，兼容新版 Cursor 的思考强度选择。
- 历史 Git 标签存在错误指向，本次仅保留版本变更梗概，不改写既有 v0.0.77 发布。

### v0.0.76

- 兼容 Cursor 3.13.21 本地模式的 `data_binary`、exec oneof、Shell hook/sandbox、Await/SubagentAwait 与 checkpoint 扩展字段。
- 修复 Shell `Skipped`、stream close 和迟到 Exit 的终态误判，禁止在未确认执行结果时伪造成功或重复执行副作用命令。
- 分离父代理继续与子代理完成语义，保留 detached child lease/correlation；收紧 MCP structured content 和 latest-only prompt context 持久化边界。

### v0.0.75

- Shell opening 按会话串行握手、running 默认并发降至 8；迟到活动可接管 uncertain attempt，避免终端分配突发造成 `Skipped` 放大。
- 仅可证明只读的命令允许有限重试；副作用未知时不重放，attempt 上限与 pre-dispatch 拒绝均产生明确唯一终态。
- `tool_result` 持久化先于 pending/tombstone 清理；原始终态可重放，后台 `shell_id` 采用续租、失效和保留期回收。

### v0.0.74

- 修复 BidiAppend 重连后 duplicate run 未提交新 epoch，确保后续 `seq=2` 进入新代；保留失败回滚、旧代隔离和非敏感切换证据。
- Shell 默认并发提升至 32；逻辑 tool call 与 transport attempt 解耦，pre-start `Skipped` 采用新 transport 身份有限退避重排队。
- waiting 期间保持 started/checkpoint pending，旧 attempt 迟到事件被隔离；已启动命令禁止重派，避免重复执行。

## 发布资产

- `cursor-byok-0.0.79-windows-amd64.zip`
- `cursor-byok-0.0.79-macos-arm64.tar.gz`
- `cursor-byok-0.0.79-macos-amd64.tar.gz`
- `update.json`
