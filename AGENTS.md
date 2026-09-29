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
- 里程碑（如 P0、P1 完成）打 tag

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
