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
