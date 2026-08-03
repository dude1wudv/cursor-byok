# Cursor助手 v0.0.80

本版本以最新上游 `leookun/cursor-byok@639c452`（v0.0.44）为干净基线重新实现，不继承旧 fork 的 Shell、协议、缓存或子代理生命周期补丁。

## 可选子代理与角色路由

- 模型渠道可选择是否作为子代理模型，并配置 `simple_explore`、`medium_explore`、`complex_debug` 三种任务角色。
- Task 显式指定模型时优先使用该渠道；省略时按配置顺序选择第一个匹配角色的渠道。
- 子代理权限继续使用上游原生 `readonly` 语义，没有引入额外权限策略。
- 同一父任务的单个 provider pass 最多派发 4 个直接子代理，超过上限会在创建子代理前明确拒绝。

## 自定义 OpenAI endpoint path

- 自定义端点模式新增独立 `openAIEndpointPath`，可配置以 `/responses` 或 `/chat/completions` 结尾的相对路径。
- 路径参与渠道身份计算，并保留旧渠道 ID 的解析兼容。
- 拒绝绝对 URL、query、fragment 和目录穿越路径。

## 完整去广告

- 删除首页广告组件、广告事件、广告 bridge、下载与缓存服务、`/ad` 路由及后台刷新。
- 保留首页使用统计、作者入口、Cursor 控制面账号和更新功能。

## 发布资产

- `cursor-byok-0.0.80-windows-amd64.zip`
- `cursor-byok-0.0.80-macos-arm64.tar.gz`
- `cursor-byok-0.0.80-macos-amd64.tar.gz`
- `update.json`