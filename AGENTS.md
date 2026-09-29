# remote-agent

在任何设备上远程操作跑在自己电脑 tmux 里的 AI 编程 CLI（先支持 Codex）。组成部分：Go 写的本地 daemon、Go 写的中继 server，以及一个 PWA 客户端。项目范围和设计取舍见 [docs/scope.md](docs/scope.md)。

有两件事是这个项目存在的理由，改动时请保住它们：

- **隐私**：server 只转发和存储密文，内容和密钥不应该出现在 server 上，也不要写进日志
- **原生 CLI**：agent 以原生交互形态跑在 tmux 里，电脑和手机操作的是同一个会话，而不是通过 SDK 驱动

其余的按常识和 Go 的惯例来就好，遇到拿不准的取舍，提出来讨论。
