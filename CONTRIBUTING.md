# 贡献与发布

## Git 远端约定

本仓库默认向维护者 fork `dude1wudv/cursor-byok` 提交和发布：

- `origin`：`https://github.com/dude1wudv/cursor-byok.git`，用于日常 `push`、标签和 GitHub Release。
- `upstream`：`https://github.com/leookun/cursor-byok.git`，仅用于查看或同步上游；未经明确授权不得向其推送。

所有 GitHub 网络操作必须经本机代理执行：

```powershell
$env:HTTP_PROXY = 'http://127.0.0.1:7890'
$env:HTTPS_PROXY = 'http://127.0.0.1:7890'
```

## 发布规则

发布从 `origin/main` 创建版本标签和 Release。默认只上传经本地核验的目标平台资产；不要触发或依赖云端构建，除非发布需求明确要求。