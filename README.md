<img  width="820"  alt="image" src="https://github.com/user-attachments/assets/2e1710b0-cdbd-4576-bd24-1614df016219" />

<img width="820"  alt="image" src="https://github.com/user-attachments/assets/00885453-6a91-4052-aadf-f686daeec881" />

<img  width="820"  alt="image" src="https://github.com/user-attachments/assets/a607be84-a738-4e33-9750-13352e74001c" />



## 为什么做这个项目

公司喜欢把 Agent 服务与模型绑定在一起，让用户只能在指定模型、指定订阅和指定计费方式下使用工具。

我希望打破这种绑定关系：模型应该可以自由选择。开发者应该能够把自己的模型 API 接入到任何 IDE、Chat、Agent 或开发工具中，也可以自托管整套服务，避免被单一平台锁定。

这个项目的目标，是让模型选择权重新回到用户手里。

## 路线图

[正式版路线图](https://github.com/leookun/cursor-byok/discussions/32)
[详细使用教程](https://dcne38qm5vlg.feishu.cn/wiki/JeP7wdGnziBXuikNaF5czWbrn8c)

## 后续

后续会继续扩展更多工具和使用场景，包括但不限于：

- 支持更多 IDE 接入
- 支持更多 Chat 类应用
- 支持更多 Agent 工具和工作流
- 提供更完善的自托管部署方式
- 持续优化不同模型 API 的兼容性
- 降低接入成本，让已有模型额度可以被更充分地利用

最终希望做到：让你的模型 API 可以自由接入到你想使用的任何工具中。

## plan-docs 波次计划 Skill

仓库内提供兼容 Cursor Agent Skills 的 `plan-docs`。它会在 Plan 模式中把复杂任务整理为依赖图和执行波次，并约束 Agent 只执行最早未完成波次：独立任务可以并行派遣 Subagent，共享文件或简单任务由主线完成。

安装到当前用户的所有 Cursor 项目：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-plan-docs.ps1
```

检查用户目录中的 Skill 是否与仓库一致：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-plan-docs.ps1 -Check
```

如果目标位置已有不同版本，先备份并覆盖：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-plan-docs.ps1 -Force
```

安装后重启 Cursor 或 Reload Window。可以输入 `/plan-docs` 显式调用，也可以在 Plan 模式中提出“生成并行波次计划”“按依赖拆分任务”等请求自动触发。

Skill 唯一源位于 `skills/plan-docs/`；`%USERPROFILE%/.cursor/skills/plan-docs` 只是安装产物，更新仓库后重新运行安装脚本即可同步。




## Star History

<a href="https://www.star-history.com/?repos=leookun%2Fcursor-byok&type=timeline&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=leookun/cursor-byok&type=timeline&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=leookun/cursor-byok&type=timeline&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=leookun/cursor-byok&type=timeline&legend=top-left" />
 </picture>
</a>
