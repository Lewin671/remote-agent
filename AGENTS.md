# remote-agent

在任何设备上远程操作跑在自己电脑 tmux 里的 AI 编程 CLI（先支持 Codex）。组成部分：Go 写的本地 daemon、Go 写的中继 server，以及一个 PWA 客户端。项目范围和设计取舍见 [docs/scope.md](docs/scope.md)。

有两件事是这个项目存在的理由，改动时请保住它们：

- **隐私**：server 只转发和存储密文，内容和密钥不应该出现在 server 上，也不要写进日志
- **原生 CLI**：agent 以原生交互形态跑在 tmux 里，电脑和手机操作的是同一个会话，而不是通过 SDK 驱动

其余的按常识和 Go 的惯例来就好，遇到拿不准的取舍，提出来讨论。

## 开发流程

前期直接在 `main` 上开发，不开分支、不提 PR：

- 每次提交只做一件事，出问题时用 `git revert` 撤回
- 推送前 `go test ./...` 必须通过；先 `git fetch`，远端有新提交就 rebase 到最新的 `main` 上，禁止 force push
- 里程碑（如 P0、P1 完成）打 tag

server 部署并开始日常使用后，大改动改回用分支。

### 多 agent 并行

可能有多个 agent 同时在这个工作区里开发：

- 自己的改动在本地验收通过、且和别人的改动不冲突，就可以直接提交并推送，不用等用户确认
- 只提交自己改的文件，`git add` 逐个写明路径，不用 `git add -A`、`git add .`、`git commit -a`
- 工作区里别人未提交的改动不要碰，也不要 stash、checkout、reset 掉
- 冲突指：要改的文件别人也有未提交的改动，或者 rebase 时出现冲突。遇到这种情况先停下来告诉用户，不要自行合并
