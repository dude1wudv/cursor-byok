<!-- 发布约定：每次 Release 都保留“最近 5 个版本更新梗概”，覆盖当前版本和前 4 个版本。 -->

# Cursor助手 v0.0.72

本版本修复 "Skipped git" 刷屏的真正根因（v0.0.70 inspect 只读 Shell 白名单在 pre-dispatch 阶段拒绝合法命令）、按证据精简 v0.0.67-v0.0.70 累积的 shell 调度脚手架，并落地性能与并行工具调用改进。

## inspect Shell 契约修复（"Skipped git" 根因）

- schema 宣告过的可选字段不再触发拒绝：`notify_on_output` 静默剥离（inspect 本就强制短前台窗口）、非 auto `profile` 归一为 auto；同时从 inspect Shell schema 中删除这两个字段，模型不再填写。此前模型按 schema 带上 `notify_on_output` 即被整调用拒绝，UI 呈现为连环 "Skipped git" 且模型无限重试。
- 白名单专用引号感知分词器：`"path with spaces"`、`--format='%h %s'` 等带引号参数可通过（工具描述本身要求为含空格路径加引号）；引号外仍拒绝管道/重定向/变量/命令替换。git 改写重组时为含空格词元恢复引号。
- 只读 git 白名单扩充：`grep`（禁 `-O`/`--open-files-in-pager` pager 逃逸）、`describe`、`shortlog`、`cherry`、`count-objects`、`stash list`、`reflog [show]`。刻意排除 `config`（凭据面）、`worktree`（改状态）、`ls-remote`（网络）。
- pre-dispatch 校验拒绝接入既有指纹熔断（同一 command/cwd/class 第 2 次即开路，第 3 次起被拦截，达 local-block 上限终止 provider 循环），确定性校验错误不再空转；开路时错误文本附明确纠正指引。熔断状态 event-sourced 于历史，reconnect 后不重启空转。

## 调度层精简（按证据删除）

- 删除 v0.0.69 的 skip 重试机制（`retrySkippedShell`、`ShellStartedExecs`、`ShellRetryCountByToolCall`）——v0.0.69 awaiting-start 门之后本地历史中真实客户端 skip 为零，该机制从未有效触发。
- `ShellRecoveryCandidates` 影子表并入 `PendingExec`（`ShellRecoveryState/StateAt/Reason/Generation`），消除双账本交叉校验；每个 exec 只保留一支监督定时器（`shell_supervision`，同 key 重排即替换）。
- 保留经证实必要的机制：容量池 + FIFO、awaiting-start 门、活动代次、两阶段 abort（绝不合成成功）、tombstone 去重。无候选登记时本地收口仍拒绝执行。

## 并行工具调用

- OpenAI Responses：gpt-5.6 系列且 tools 非空时发送 `parallel_tool_calls: true`；未知兼容端点不发送；extra params 显式设置优先。
- Claude 无需请求变更（Anthropic 默认允许并行 tool_use）：新增适配器回归测试锁定"一条 assistant 消息 N 个 tool_use → N 个独立事件、乱序 stop 参数互不串扰"，以及 forwarder 恢复门测试（同 pass 多 PendingExecs 全终态才恢复一次）。
- Grep/Ls 工具描述补齐批量调用指引（全模式变体），独立只读检查在同一回复内批量发出。

## 上下文成本与缓存稳定

- 回放上限收紧：Read 24KiB、Grep/Glob/Ls 16KiB、Shell 32KiB、WebFetch/MCP 24KiB（Glob/Ls 此前无上限）。
- 单回合 512KiB 回放预算：通过只前进的 `replay_budget_boundary` 持久化边界实现——最新 8 个结果保持正常额度，更早结果投影时压缩至 4KiB；历史文件保存完整事实，回放跨 pass 逐字节一致，不复现 v0.0.68 latest-only 击穿 prompt cache 的老问题。
- 自动压缩新增 55% 窗口性能软阈值 + 20% 窗口再增长迟滞（`SoftCompactionBaselineTokens` 持久化）；原接近硬上限的防溢出触发保留。

## 性能观测

- 每个 provider pass 结束输出 `provider_pass_metrics` 结构化低敏指标：编译耗时、回放消息数、估算/实际 tokens、工具结果字节、TTFT、pass 时长、外部工具等待、工具数量、并行宽度、cache read/write tokens、终态；含 Anthropic `expected_cache_read`/前缀 hint 诊断。日志不含提示词、工具正文或密钥。

## 最近 5 个版本更新梗概

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

### v0.0.68

- 修复 GPT-5.6/OpenAI Responses 与 Cursor Multitask 的 worker 收口、会话模型固定和工具协议兼容问题。
- 保留 reasoning、`call_id` 和 typed SSE 回放，收紧 Multitask 初始工具并修正 Task 卡片模型路由。

## 发布资产

- `cursor-byok-0.0.72-windows-amd64.zip`
- `cursor-byok-0.0.72-macos-arm64.tar.gz`
- `cursor-byok-0.0.72-macos-amd64.tar.gz`
- `update.json`
