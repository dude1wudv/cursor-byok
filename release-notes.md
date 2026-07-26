# Cursor助手 v0.0.70

本版本修复 Shell、Task/subagent 两条状态机的一致性缺陷，统一 inspect 权限契约，并补齐构建身份观测。

## Shell 生命周期

- Start/stdout/stderr 归一为单一活动迁移：递增活动代次、延长 foreground deadline、撤销恢复候选并重排监督定时器；活跃 Shell 不再被旧 deadline 错误本地收口。
- foreground deadline 改为两阶段收口：先向客户端发送 abort 并进入 `abort_requested`，短 grace 后仍无真实终态才以 timeout 语义关闭；不合成成功。
- 终态所有权在单一临界区内原子提交（tombstone、pending 删除、候选清理、batch terminal），消除本地恢复与迟到 Exit 的双收口窗口。
- legacy `ShellResult_Rejected("Skipped")` 与 stream Skipped 归一，同样进入有界 retry/recovery。
- Shell circuit 接线生效：同一 command/cwd/class 指纹累计 2 次 `permission/policy/capability` terminal 拒绝才开路；Skipped、transport、parse 不计；真实 Start 或成功会重置对应指纹。

## inspect 权限与受控 Shell

- 修复嵌套 `medium_explore` 派遣校验：canonical `access_mode=inspect` 即可通过，不再要求 legacy `readonly=true`。
- inspect（只读）子代理开放服务端强制的 Shell 白名单：单条简单命令、工作区绑定、短前台窗口；覆盖只读 Git 证据链（自动注入 `--no-pager --no-optional-locks`）、进程/端口查询与文件哈希；写入、网络、构建、脚本解释器在派发前拒绝。
- 仅暴露 `Shell`，不暴露 `AwaitShell/WriteShellStdin/ForceBackgroundShell`；提示词与工具描述同步更新为 `access_mode=inspect/act`。

## Task 卡片与历史投影

- started/completed/checkpoint 三处统一由 canonical capability 推导 `TaskMode`：inspect → Plan，act → Agent。
- `tool_call` history 保存原始 arguments；投影时优先用原始 arguments、其次用同 ID `tool_result.arguments` 回填既有已完成记录，均缺失时保持原值。

## Subagent 终态

- dispatch failure 区分两种语义：exec 未发布时补写 `SubagentRunState=ERROR` 后回收 reservation 并收口工具调用，消除 RUNNING 残留；exec 已发布后失败记录 `subagent_dispatch_uncertain`，保留 pending/lease 等待真实结果或 abort 确认。

## 观测

- 新增 `buildinfo.Commit`，由 Windows/macOS/Linux（含 Docker）构建注入，无法解析时为 `unknown`。
- 观测日志 baseEvent 统一附加 `build_version/build_commit`；Shell 恢复元数据记录活动代次、恢复阶段与终态所有者。

## 发布资产

- `cursor-byok-0.0.70-windows-amd64.zip`
- `cursor-byok-0.0.70-macos-arm64.tar.gz`
- `cursor-byok-0.0.70-macos-amd64.tar.gz`
- `update.json`