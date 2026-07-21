# Cursor助手 v0.0.58

本版本修复 Kimi 等 OpenAI 兼容 `chat/completions` 流在回答中途被普通 EOF 截断后误判成功的问题，并增加安全、有限的纯文本续写恢复。本次本地发布提供 Windows amd64 资产；推送标签后 GitHub Actions 将继续构建配置中的其他平台资产。

## 稳定性修复

- `chat/completions` 流必须收到非空 `finish_reason` 才视为正常完成；仅收到半段内容后连接关闭会被识别为 incomplete stream。
- EOF、解析失败或流空闲超时时不再提交仅存在于内存中的残缺工具调用，避免误执行不完整参数。
- 对无工具副作用的文本或思考断流执行最多 3 次短退避续写，并要求从已有 assistant 末尾继续且不重复已输出内容。
- 检测到已提交、pending 或 partial 工具调用时禁止自动重放，保留已有输出并返回明确失败终态。

## 可观测性

- 模型调用摘要新增终态、`[DONE]`、事件数、最后有效事件时间、watchdog 与错误分类字段。
- provider 恢复元数据记录失败分类、尝试次数、文本/思考/工具状态以及新旧 `model_call_id`，便于串联定位断流过程。

## Windows 发布资产

- `cursor-byok-0.0.58-windows-amd64.zip`
  - 内含 `cursor-byok-windows-amd64.exe`

适用于 Windows 10/11 amd64。
