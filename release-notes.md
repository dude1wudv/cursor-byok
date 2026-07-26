# Cursor助手 v0.0.69

本版本恢复 v0.0.67 的请求缓存行为，并修复并发 Shell 在 Cursor 终端分配阶段被 skipped 的问题。

## 请求缓存

- 当前请求、模式、Plan/Todo 和动态提醒重新作为 prompt context 持久化在首次出现的位置。
- 移除 v0.0.68 的 latest-only suffix 编译路径；同一 turn 后续 provider pass 只追加 history，不移动已发送前缀。
- 保留 `prompt_cache_key`、`LatestRequestPrefix` 和 `StableMessageCount` 的现有行为，不增加额外缓存策略。

## Shell 分配

- 同一 run 仍可并行运行最多 8 个已启动 Shell，但任意时刻只发送一个尚未收到 Cursor Start 的新 exec。
- Start 到达后立即按 FIFO 放行下一项；Exit、Backgrounded、拒绝和恢复收口都会释放分配状态。
- 仅对未见 Start/stdout/stderr、存在其他运行中终端且尚未重派的 skipped 使用新 exec/message ID 重派一次。
- Shell dispatch、queue、Start、skipped、retry 和终态均记录脱敏关联信息；命令只记录 hash。

## 保留的 v0.0.68 行为

- OpenAI Responses reasoning、`call_id`、typed SSE 和交错工具结果回放保持不变。
- 根会话模型固定、Multitask worker 收口、角色化工具权限和工具集调整保持不变。

## 发布资产

- `cursor-byok-0.0.69-windows-amd64.zip`
- `cursor-byok-0.0.69-macos-arm64.tar.gz`
- `cursor-byok-0.0.69-macos-amd64.tar.gz`
- `update.json`
