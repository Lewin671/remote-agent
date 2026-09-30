# remote-agent

在任何设备上远程操作跑在自己电脑 tmux 里的 AI 编程 CLI（Codex 和 Claude Code）。组成部分：Go 写的本地 daemon、Go 写的中继 server，以及 PWA 客户端和开发中的 Android 原生客户端。项目范围见 [docs/scope.md](docs/scope.md)，技术决策见 [docs/architecture.md](docs/architecture.md)，当前进度和下一步见 [docs/plan.md](docs/plan.md)。

有两件事是这个项目存在的理由，改动时请保住它们：

- **隐私**：server 只转发和存储密文，内容和密钥不应该出现在 server 上，也不要写进日志
- **原生 CLI**：agent 以原生交互形态跑在 tmux 里，电脑和手机操作的是同一个会话，而不是通过 SDK 驱动

其余的按常识和 Go 的惯例来就好，遇到拿不准的取舍，提出来讨论。

改动或新建 `docs/` 下的文档前，先读 [docs/README.md](docs/README.md)，里面规定了每类内容写在哪。

## 开发流程

前期所有改动直接进 `main`，不提 PR：

- 每次提交只做一件事，出问题时用 `git revert` 撤回
- 推送前 `go test ./...` 必须通过；先 `git fetch`，远端有新提交就 rebase 到最新的 `main` 上，禁止 force push
- 里程碑（如 P0、P1 完成）可以继续打里程碑 tag；产品发布用 `vMAJOR.MINOR.PATCH`

### 自动版本与发布

用户已授权 agent 在合适的开发节点自动更新版本并发布，不需要逐次确认，不使用定时任务。

- 一个用户任务的完整功能或一批相关修复完成、本地验收通过后，自动发布一次；不要给任务中间的小提交逐一发版。只有文档、界面稿或测试变化时不发产品版本
- 新功能升 minor，修复升 patch；1.0 以前的不兼容变化升 minor，1.0 起不兼容变化升 major。只有需要实际发布时才改 `version.properties`；各端共用产品版本，协议版本独立
- 在独立 worktree 中提交任务代码并完成验收后，自动执行 `scripts/release-task.sh patch|minor|major "变化与升级要求"`。它会 fetch/rebase、检查是否有产品变化、递增共享版本与 Android code、提交版本文件、跑检查、创建 tag 并原子推送。没有产品变化会跳过；需要单步处理时用 `python3 scripts/release.py prepare patch|minor|major`
- 版本提交 rebase 到最新 main 后重新跑 `go test ./...` 和改动涉及的检查。若别人已发布使版本不再适用，重新计算版本；冲突仍按下文规则停下报告
- 验证通过后，在版本提交上创建 annotated tag，并以 `git push --atomic origin HEAD:main refs/tags/vX.Y.Z` 一次推送提交和 tag，禁止 force push、移动或复用已推送的版本 tag
- GitHub 的 Release 工作流从 tag 构建所有端，Go/网页/Android 检查全部通过才发布正式签名 APK、Go 二进制和网页产物。失败不算发布完成：查看 Actions 错误并修复；代码修复发下一版，同一 tag 的暂时失败可通过工作流手动重跑
- 发布产物不代表部署线上服务，不自动升级本机 daemon，也不自动安装 APK。已有部署授权按原任务执行
- 手动 APK 工作流只生成独立 `.dev` 应用，推送关闭，短期保留，不更新正式版本、不创建 Release

server 部署并开始日常使用后，大改动改回用分支。

### 多 agent 并行

可能有多个 agent 同时开发，每个 agent 在自己的 `git worktree` 里干活，不在主工作区里改代码。这样暂存区、`go test` 和 rebase 都互不干扰。

```sh
# 开工：从最新的 main 建 worktree，放在仓库外的同级目录
git fetch
git worktree add -b wt/<任务名> ../remote-agent.worktrees/<任务名> origin/main

# 做完：在 worktree 里提交后，rebase 到最新的 main，测试通过再推
git fetch && git rebase origin/main
go test ./...
git push origin HEAD:main

# 收尾：确认已推送后清理，并把主工作区快进到最新
git worktree remove ../remote-agent.worktrees/<任务名>
git branch -D wt/<任务名>
git -C <主工作区> merge --ff-only origin/main
```

- 本地验收和测试都通过，就可以直接提交并推送，不用等用户确认
- `wt/<任务名>` 只是临时分支，不推到远端，推送目标始终是 `main`
- rebase 出现冲突时先停下来告诉用户，不要自行合并；rebase 后测试不过（别人的改动和你的不兼容）也一样
- 主工作区留给用户，agent 不在里面改动或提交；唯一例外是收尾时的快进，它只会前移到远端已有的提交，不动未提交的改动。快进失败就跳过，告诉用户原因
- 新 agent 通常从主工作区启动、读那里的 AGENTS.md，所以每次推送后都要快进，免得后来的 agent 读到旧规则
