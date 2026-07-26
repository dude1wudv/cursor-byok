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

# Cursor助手 v0.0.71

本版本集中修复 Anthropic/Claude 适配链路的兼容缺陷：思考中对话提前结束、历史回放隐性 400、Plan 卡片不出现、非 Shell 工具调用永久卡死，以及 Grep 回放截断的数字矛盾与过度丢弃。

## 流生命周期（对话提前结束）

- idle watchdog 改为按任意 SSE 流量（含 ping 心跳）刷新；长 extended thinking 数分钟无内容 delta 不再被 4 分钟掐断。仅有心跳但始终不产出内容的病态流由 10 倍时长 hard cap 兜底。
- `pause_turn` 映射为受控未完成信号，forwarder 复用输出上限续写通道自动带历史继续，不再当正常结束收口。
- SSE 流内 `error` 事件按类型映射为等价 HTTP 状态码（`overloaded_error`→529、`rate_limit_error`→429、`api_error`→500、`timeout_error`→408），接入既有可重试分类。
- Anthropic scanner 单行缓冲从 1MiB 对齐到 64MiB，大工具参数不再撑爆流。

## 历史回放（隐性 400）

- 回放层合并相邻同角色消息并去重 thinking 块，满足 Anthropic 角色交替约束。
- 无有效 signature 的 thinking 不再回放为 thinking 块；新增 `redacted_thinking` 解析与原样回放。
- 校验 `tool_use`/`tool_result` 配对，compaction/rewind 后的孤儿 tool_result 降级为普通文本。

## 工具调用卡死

- 非 Shell 客户端执行工具（Read/Grep/Glob/写删/MCP/patch-edit 各阶段）新增结果超时 watchdog（普通 2 分钟、MCP 10 分钟），超时本地合成错误 tool_result 并继续回合；回包失配同样由该路径兜底。
- 交互工具纳入超时保护：机器执行类（WebSearch/WebFetch/SwitchMode）3 分钟，等待人工输入类（AskQuestion/CreatePlan）30 分钟兜底取消，不再静默挂起。
- Read/Grep/Glob 纳入 started 抑制名单，partial 行与 started 行不再在 UI 重复渲染。

## Plan 卡片

- 工具名变体（`create_plan`/`createPlan` 等）在适配器归一为 `CreatePlan`。
- CreatePlan 参数尚不可解析时先发空占位 partial，卡片立即弹出；前缀解析支持从不完整 JSON 提取已闭合的 todo 对象。
- 参数容错清洗：todo status 别名扩充（not_started/done/doing/wip 等）、未知 status 归一、字符串 todo 降级为 content-only、非法条目丢弃而非整卡失败。
- plan 提示词增补：必须通过 CreatePlan 提交计划、`plan` 字段先于 `todos` 输出、status 枚举约束。

## 请求构造兼容

- 渠道配置 `thinkingBudgetTokens` 时直接使用 legacy `{type:"enabled", budget_tokens}` 形态；adaptive 形态收到疑似兼容性 400 时自动降级 legacy 原地重试一次。
- thinking 开启时从最终请求体（含 extra params 合并结果）剔除 `temperature`/`top_p`。

## Grep 回放截断

- context 行（-A/-B/-C）不再消耗匹配数预算（仅计内容字节），修复 context 模式下后续小结果被整体丢弃。
- 截断 notice 改为陈述本结果实际保留字节与共享预算语义，消除「总量 646 却报 exceeded 32768」的矛盾。

## 发布资产

- `cursor-byok-0.0.71-windows-amd64.zip`
