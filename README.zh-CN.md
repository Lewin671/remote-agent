<div align="center">

# remote-agent

**在自己的电脑上运行 Codex 和 Claude Code，在任何设备上操作它们。**

tmux 里的原生 CLI 会话 · 端到端加密 · 自部署

[![最新版本](https://img.shields.io/github/v/release/Lewin671/remote-agent?label=release&color=1a7f37)](https://github.com/Lewin671/remote-agent/releases/latest)
[![许可证：AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue)](LICENSE)
![Daemon](https://img.shields.io/badge/daemon-macOS%20%7C%20Linux-555)
![客户端](https://img.shields.io/badge/clients-Web%20%7C%20Android%20%7C%20iOS-555)

[English](README.md) · 简体中文

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/demo-dark.zh-CN.svg">
  <img src="docs/assets/demo-light.zh-CN.svg" alt="动画演示：电脑上 tmux 里的 Claude Code 请求执行 go test；请求以密文经过中继；在手机上点允许；同一个会话接着运行；再从手机发出下一条消息。" width="960">
</picture>

</div>

**一圈看懂：** ① 照常在电脑上运行 `remote-agent claude`（或 `codex`）→ ② agent 请求权限时，请求在电脑上加密，中继只转发密文 → ③ 你在手机上点「允许」→ ④ 同一个 tmux 会话接着运行 → ⑤ 人在哪儿都能发下一条消息。

## 简介

remote-agent 让你离开电脑时，编程 agent 不用停下。你照常在电脑上启动 Codex 或 Claude Code，同一个会话随即出现在手机（Android 或 iOS）和浏览器里：查看进展、发送下一条消息、回答它的提问、允许或拒绝权限请求、中断它，或者打开实时终端。

| | |
|---|---|
| **原生 CLI** | agent 以自己的交互形态跑在 tmux 里，不经过 SDK，也不是另做的聊天界面。桌上的终端和手里的客户端操作的是同一个会话。 |
| **端到端加密** | 消息、工具调用、终端输出、图片、项目名和路径都在你的设备上加密。中继只存储和转发密文，从不持有密钥。 |
| **自部署** | 中继跑在你自己的服务器上，网页也由你自己托管。不需要第三方账户，中间也没有聊天平台。想先试试，可以用[公共中继](#在公共中继上试用)。 |
| **多台电脑，一处查看** | 每台电脑跑一个 daemon，客户端按项目汇总所有会话。 |

> [!NOTE]
> 客户端界面目前是简体中文。项目尚未到 1.0，变化较快，依赖它之前请先读[现状与限制](#现状与限制)。

## 工作方式

<p align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/architecture-dark.svg">
  <img src="docs/assets/architecture-light.svg" alt="你的设备（网页、Android App、iOS App）通过一台只看得到密文的中继连到你的电脑" width="880">
</picture>
</p>

| 组件 | 运行在 | 职责 |
|---|---|---|
| **Daemon**（`remote-agent`） | 每台放代码的电脑，macOS 或 Linux | 在 tmux 里托管 agent 会话，读取 transcript，通过 CLI 的 hook 转交审批，注入输入。同一个二进制也是命令行工具。 |
| **中继**（`remote-agent-server`） | 你掌控的一台 Linux 服务器 | 设备鉴权，存储和转发密文，发送推送通知。读不到内容。 |
| **网页** | 任意 HTTPS 静态托管 | 可安装的 PWA：对话视图、实时终端、审批、Web Push。 |
| **Android App** | Android 8.0 及以上 | 原生客户端，带原生终端，可在通知里直接审批。 |
| **iOS App**（预览） | iPhone 和 iPad，iOS 17 及以上 | SwiftUI 原生客户端，带原生终端、审批和图片。还没有分发，见[现状与限制](#现状与限制)。 |

账户密钥只在各台设备上，中继上没有。新设备通过扫二维码或输入配对码加入账户，双方核对一串相同的 emoji 后才交换密钥。

## 在公共中继上试用

维护者运营着公共中继 `ra.remote-agent.top` 和网页 `https://remote-agent.top`，方便你在自己部署之前先试用。在一台装有 tmux 3.2+、已登录 `codex` 或 `claude` 的 macOS 或 Linux 电脑上：

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh
remote-agent-service install                                     # 在后台运行 daemon
remote-agent account init --server https://ra.remote-agent.top   # 创建你的账户，不需要 token
remote-agent account pair --web https://remote-agent.top         # 显示给手机扫的二维码
remote-agent codex                                               # 或：remote-agent claude
```

和在别处一样，你的会话在这里也是端到端加密的：中继只看得到[安全模型](#安全模型)里列出的元数据，看不到内容。不过 `remote-agent.top` 上的网页由维护者提供，用它就意味着信任这份客户端；命令行和 Android App 不依赖它。这是试用服务：每个账户最多 8 台设备、2 台电脑、200 个会话，不保证可用性，滥用的账户可能被暂停。日常使用请照下文自己部署；迁移时在你自己的中继上新建账户即可。

## 快速开始

需要三样东西：服务器上的**中继**、静态托管上的**网页**、你电脑上的 **daemon**。每一步的细节见[自部署指南](docs/self-hosting.zh-CN.md)，简版如下。

**1. 中继**：一台装了 Docker、有域名指向它的 Linux 服务器：

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- server
# 在 remote-agent-server/.env 里设置 RELAY_DOMAIN 和 ALLOWED_ORIGINS，然后：
cd remote-agent-server && docker compose up -d --build
```

**2. 网页**：静态文件，可部署到 Cloudflare Pages 或任何 HTTPS 托管：

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- web
npx wrangler pages deploy remote-agent-web --project-name <你的项目名>
```

> [!IMPORTANT]
> 中继和网页要放在同一个注册域名下，例如 `ra.example.com` 和 `app.example.com`。部分拦截扩展会丢弃跨站的 WebSocket，网页就会能登录、却一直连不上。

**3. 你的电脑**：macOS 或 Linux，装有 tmux 3.2+ 和已登录的 `codex` 或 `claude`：

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh
remote-agent-service install                                # 让 daemon 在后台运行
remote-agent account init --server https://ra.example.com   # 会要求输入中继的 setup token
remote-agent account pair --web https://app.example.com     # 显示给手机扫的二维码
remote-agent codex                                          # 或：remote-agent claude
```

用手机扫二维码，刚才启动的会话就已经在里面了。

## 日常使用

从客户端开的会话和从终端开的会话是同一种会话。命令行常用操作：

| 命令 | 用途 |
|---|---|
| `remote-agent codex` / `remote-agent claude` | 在当前目录启动会话并进入 |
| `remote-agent ls` | 列出会话；ID 可以只写能区分的前几位 |
| `remote-agent attach <id>` | 进入一个正在运行的会话 |
| `remote-agent send <id> <内容>` | 不进入会话，直接发消息 |
| `remote-agent events <id> -f` | 持续查看会话事件 |
| `remote-agent doctor` | 检查已安装的 CLI 是否符合 remote-agent 的预期；升级 Codex 或 Claude Code 后运行一次 |
| `remote-agent account pair` | 给账户添加一台设备 |

全部命令见 `remote-agent --help`。

## 安全模型

中继被当作不可信的一方。即使服务器被攻破，泄露的也只是元数据，而不是内容。

| 数据 | 中继能否看到 |
|---|---|
| 消息、工具调用、终端输出、审批请求、图片 | 不能 |
| 项目名、路径、git 远端、会话标题、机器名 | 不能 |
| 账户密钥和会话密钥 | 不能 |
| 设备、机器和会话 ID，时间戳，密文大小 | 能，路由需要 |
| 会话是否结束、何时结束 | 能 |
| 你的设备的 IP 地址 | 能，和任何服务器一样 |

网页刻意**不**由中继提供：否则被攻破的中继可以替换页面里的 JavaScript，绕过加密。请把网页托管在别处，例如 Cloudflare Pages。安全漏洞请通过 [GitHub security advisories](https://github.com/Lewin671/remote-agent/security/advisories/new) 私下报告，不要发在公开的 issue 里。

## 下载与校验

每个 [Release](https://github.com/Lewin671/remote-agent/releases/latest) 都包含同一版本的全部组件：

| 文件 | 内容 | 平台 |
|---|---|---|
| `remote-agent-vX.Y.Z-<os>-<arch>.tar.gz` | daemon 和命令行，以及让它在后台运行的 `remote-agent-service` | macOS 和 Linux，arm64 和 amd64 |
| `remote-agent-server-vX.Y.Z-linux-<arch>.tar.gz` | 中继，附带含 HTTPS 的 Docker Compose 配置 | Linux，arm64 和 amd64 |
| `remote-agent-web-vX.Y.Z.tar.gz` | 网页的静态文件 | 任意 HTTPS 静态托管 |
| `remote-agent-vX.Y.Z.apk` | Android App | Android 8.0 及以上 |
| `SHA256SUMS` | 以上所有文件的校验和 | |

`install.sh` 会挑选合适的文件、按 `SHA256SUMS` 校验，并且无需 root 安装。想先看看它做什么，可以下载下来读一遍，再用 `sh install.sh` 运行。手动校验下载的文件：

```sh
sha256sum --check --ignore-missing SHA256SUMS   # macOS：shasum -a 256 --check --ignore-missing SHA256SUMS
```

Android App 始终用同一把密钥签名，证书的 SHA-256 指纹是：

```
8f:ea:c3:68:de:87:29:d5:25:ca:65:f7:40:36:83:27:17:76:68:af:e3:de:06:2a:df:09:3e:df:89:3f:20:cc
```

## 更新

- **Android App**：从本仓库的 Release 自动更新，见 **设置 › 版本**。
- **Daemon**：`remote-agent update check`、`remote-agent update` 和 `remote-agent update status`。自动更新默认关闭；用 `remote-agent update auto enable --time 04:00`（本机时间）为这台电脑开启，用 `remote-agent update auto disable` 关闭。更新时通过 HTTPS 校验大小和 SHA-256，原子替换二进制，tmux 会话不中断，新 daemon 起不来就回滚。改变协议或存储数据的升级不会自动进行，请按发布说明操作。
- **中继和网页**：用新版本重新运行安装脚本。

## 现状与限制

- **尚未到 1.0。** 不同版本的组件不保证互通，版本之间可能需要重建存储的数据。daemon、中继、网页和 App 请一起升级，升级前先读发布说明。
- **一个账户就是一个人。** 账户内没有团队、共享或权限分级：账户里的每台设备都持有同一把密钥。一台中继可以承载多个互相看不见的账户；你自己的中继默认只有你的账户，除非你[向他人开放](docs/self-hosting.zh-CN.md#向他人开放中继)。
- **自部署的中继上 Android 没有推送。** 发布的 App 只能通过维护者的 Firebase 项目接收推送。App 打开时一切正常；要在自己的中继上收到通知，请用支持 Web Push 的网页。想用自己的 Firebase 项目，需要从源码构建 App。
- **iOS App 还是预览版。** [`ios/`](ios) 里的原生客户端已经能看对话、开终端、审批和收发图片，但还没有 App Store 或 TestFlight 版本，也没有推送，目前只在模拟器上测过。可以用 Xcode 从源码构建（[iOS 客户端说明](docs/ios-client.md)），或者用 Safari 打开网页并添加到主屏幕。
- **不保证支持。** 欢迎在 [Issues](https://github.com/Lewin671/remote-agent/issues) 里报告问题。不要贴出账户密钥、setup token、配对链接或未脱敏的日志。

## 开发

源码结构：

| 路径 | 内容 |
|---|---|
| `cmd/remote-agent`、`cmd/remote-agent-server` | daemon／命令行和中继的入口（Go） |
| `internal/` | Go 代码：daemon、中继服务、加密、Codex 和 Claude Code 的接入、更新器 |
| `proto/`、`gen/` | 协议定义（Protocol Buffers、Connect）和生成的代码 |
| `web/` | 网页客户端（TypeScript、React、Vite） |
| `android/`、`ios/` | 原生客户端（Kotlin + Jetpack Compose；Swift + SwiftUI） |
| `deploy/` | 容器镜像，以及随中继发布的自部署模板 |
| `scripts/` | 构建、测试、发布和部署工具 |
| `docs/` | 项目边界、架构、外部工具实测、计划和验收记录 |

从源码构建和运行见[开发指南](docs/development.md)。设计文档从 [docs/README.md](docs/README.md) 读起，那里说明了每份文档负责什么。贡献者和编程 agent 请先读 [AGENTS.md](AGENTS.md)，里面是这个项目的工作规则。

## 许可证

remote-agent 是自由软件，以 [GNU Affero General Public License v3.0](LICENSE) 发布。如果你把修改后的版本作为网络服务运行，需要向这个服务的用户提供相应的源码。Release 压缩包里的 `THIRD_PARTY_NOTICES.txt` 包含所附第三方软件的许可证文本。
