# Cursor助手 v0.0.57

本版本增强多级子代理的长上下文探索能力，并修复 Shell/Git 被跳过或返回旧式结果后执行状态无法收口的问题。本次本地发布提供 Windows amd64 资产；推送标签后 GitHub Actions 将继续构建配置中的其他平台资产。

## 功能增强

- `medium_explore` 二级代理可在长文件、多文件或跨模块调查中派发一个只读三级 `longContextRead` / `explore` 代理。
- 服务端强制限制三级代理的类型、角色、只读权限、派发数量与最大深度，避免权限绕过和任务树失控。
- 三级代理仅回传候选文件、关键行段、模块关系和未确认点；二级代理必须精准读取相关区段并交叉验证。
- 子代理合同在 child 会话中去重，避免重复持久化导致上下文膨胀和前缀缓存退化。

## 稳定性修复

- 执行桥兼容 legacy `ShellResult` 的成功、失败、超时、启动错误、拒绝和权限失败终态。
- `Skipped git`、空 Shell 事件和未知 Shell 事件会生成明确终态，不再遗留 pending exec。
- 保留 transport close 的短暂恢复窗口与完全无事件时的前台超时兜底。
- 增加 Shell 终态来源记录，便于区分 skipped、legacy result、transport close 和协议恢复。

## Windows 发布资产

- `cursor-byok-0.0.57-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`

适用于 Windows 10/11 amd64。
