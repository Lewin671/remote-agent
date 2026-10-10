<div align="center">

# remote-agent

**在任何设备上操作跑在你自己电脑上的 AI 编程 CLI。**<br>
Codex 和 Claude Code 以原生形态跑在 tmux 里。端到端加密。自己部署。

[![最新版本](https://img.shields.io/github/v/release/Lewin671/remote-agent-releases?label=release&color=1a7f37)](https://github.com/Lewin671/remote-agent-releases/releases/latest)
![平台](https://img.shields.io/badge/daemon-macOS%20%7C%20Linux-555)
![客户端](https://img.shields.io/badge/clients-Web%20%7C%20Android-555)

[English](README.md) · 简体中文

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture-dark.svg">
  <img src="assets/architecture-light.svg" alt="你的设备通过一台只看得到密文的中继连到你的电脑" width="880">
</picture>

</div>

## 它做什么

你照常在电脑上启动 Codex 或 Claude Code。离开电脑以后，同一个会话就在手机上：看 agent 做了什么、发下一句话、允许或拒绝一次权限请求、打断它，或者直接打开实时终端。

- **原生 CLI，不是套壳。** agent 以原本的交互形态跑在 tmux 里，桌上的终端和手里的 App 是同一个会话。
- **端到端加密。** 消息、工具调用、终端输出、项目名和路径都在你自己的设备上加密。中继只存储和转发密文，不持有任何密钥。
- **自己部署。** 中继跑在你自己的 VPS 上，网页也由你自己托管。不需要在我们这里注册账户，中间也没有第三方聊天平台。
- **多台电脑，一个视图。** 每台电脑跑一个 daemon，客户端把所有会话按项目分组。

> [!NOTE]
> 这是一个还在快速变化的早期项目，依赖它之前请先看[现状与限制](#现状与限制)。

## 快速开始

需要三样东西：VPS 上的**中继**、任意静态托管上的**网页**、代码所在电脑上的 **daemon**。每一步的细节见[自部署指南](docs/self-hosting.zh-CN.md)，简版如下：

**1. 中继**：一台装了 Docker 的 Linux VPS，和一个解析到它的域名：

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- server
# 在 remote-agent-server/.env 里填好 RELAY_DOMAIN 和 ALLOWED_ORIGINS，然后：
cd remote-agent-server && docker compose up -d --build
```

**2. 网页**：一份静态文件，放到 Cloudflare Pages 或任何支持 HTTPS 的静态托管：

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- web
npx wrangler pages deploy remote-agent-web --project-name <你的项目名>
```

**3. 你的电脑**：macOS 或 Linux，装有 tmux 3.2 以上，`codex` 或 `claude` 已登录：

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh
remote-agent-service install                                  # 让 daemon 在后台运行
remote-agent account init --server https://ra.example.com     # 会要求输入中继的 setup token
remote-agent account pair --web https://app.example.com       # 显示给手机扫的二维码
remote-agent codex                                            # 或者：remote-agent claude
```

用手机扫二维码，刚启动的会话就出现在手机上了。

## 下载

每个 [Release](https://github.com/Lewin671/remote-agent-releases/releases/latest) 里各组件都是同一个版本：

| 文件 | 内容 | 运行环境 |
|---|---|---|
| `remote-agent-vX.Y.Z-<os>-<arch>.tar.gz` | daemon 和命令行，以及让它在后台运行的 `remote-agent-service` | macOS 和 Linux，arm64 和 amd64 |
| `remote-agent-server-vX.Y.Z-linux-<arch>.tar.gz` | 中继，附带含 HTTPS 的 Docker Compose 配置 | Linux，arm64 和 amd64 |
| `remote-agent-web-vX.Y.Z.tar.gz` | 网页（可安装为 PWA），静态文件 | 任何支持 HTTPS 的静态托管 |
| `remote-agent-vX.Y.Z.apk` | Android App | Android 8.0 及以上 |
| `SHA256SUMS` | 以上所有文件的校验值 | |

`install.sh` 会选对文件、按 `SHA256SUMS` 核对后再安装，不需要 root。想先看看它做什么？下载下来读一遍，再用 `sh install.sh` 运行。手动核对下载的文件：

```sh
sha256sum --check --ignore-missing SHA256SUMS      # macOS：shasum -a 256 --check --ignore-missing SHA256SUMS
```

Android App 始终用同一把密钥签名，证书的 SHA-256 指纹是：

```
8f:ea:c3:68:de:87:29:d5:25:ca:65:f7:40:36:83:27:17:76:68:af:e3:de:06:2a:df:09:3e:df:89:3f:20:cc
```

App 从这个仓库自己更新：**设置 › 版本**。

## 中继能看到什么

中继被当作不可信的一方。VPS 被攻破时，泄露的是元数据，不是内容。

| | 中继看得到吗 |
|---|---|
| 消息、工具调用、终端输出、审批请求、图片 | 否 |
| 项目名、路径、git 远程地址、会话标题、机器名 | 否 |
| 账户密钥和会话密钥 | 否 |
| 设备 ID、机器 ID、会话 ID、时间戳、密文长度 | 是，路由需要 |
| 会话有没有结束、什么时候结束 | 是 |

网页故意**不由中继提供**：否则中继被攻破后可以替换页面的 JavaScript、绕过加密。请把它托管在别处，比如 Cloudflare Pages。

## 现状与限制

- **目前闭源。** 这个仓库只发布二进制和文档，源码没有公开，所以上面关于加密的说法无法从这个仓库独立核实，请自行判断。
- **1.0 之前。** 不同版本的组件不保证能互通，存储的数据在版本之间可能需要重建。daemon、中继、网页和 App 请一起升级，升级前先读发布说明，里面写明了升级要求。
- **一台中继一个账户。** 一台中继只服务一个人的设备，没有团队、共享和多账户。
- **自己的中继上没有 Android 推送。** 发布的 App 只能接收经维护者的 Firebase 项目发出的推送，你的中继用不了它。App 打开时一切正常；需要通知时用网页，它在任何中继上都支持 Web Push。App 里仍然带着 Firebase 的推送库，并会到这个仓库检查更新。
- **没有 iOS App。** 在 Safari 里用网页，并添加到主屏幕。
- **不保证支持。** 欢迎在 [Issues](https://github.com/Lewin671/remote-agent-releases/issues) 里报告问题；不要把账户密钥、setup token 或配对链接贴进去。

## 许可

本仓库 Release 中的文件免费提供给个人使用，按现状提供，不附带任何形式的担保。其中包含的开源软件，许可证全文见各压缩包里的 `THIRD_PARTY_NOTICES.txt`。
