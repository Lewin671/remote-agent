# 参考

[English](reference.md) · 简体中文

[README](../README.zh-CN.md) 背后的细节：组件、常用命令、安全模型、下载与校验、更新。自己部署中继见[自部署指南](self-hosting.zh-CN.md)。

## 组件

| 组件 | 运行在 | 职责 |
|---|---|---|
| **Daemon**（`remote-agent`） | 每台放代码的电脑，macOS 或 Linux | 在 tmux 里托管 agent 会话，读取 transcript，通过 CLI 的 hook 转交审批，注入输入。同一个二进制也是命令行工具。 |
| **中继**（`remote-agent-server`） | 你掌控的一台 Linux 服务器 | 设备鉴权，存储和转发密文，发送推送通知。读不到内容。 |
| **网页** | 任意 HTTPS 静态托管 | 可安装的 PWA：对话视图、实时终端、审批、Web Push。 |
| **Android App** | Android 8.0 及以上 | 原生客户端，带原生终端，可在通知里直接审批；当前手机成员是账户的管理端（配对、删除、续租约和恢复）。 |
| **iOS App**（预览） | iPhone 和 iPad，iOS 17 及以上 | SwiftUI 原生客户端，带原生终端、审批和图片。还没有分发，见[现状与限制](../README.zh-CN.md#现状与限制)。 |

建账户的那台手机持有账户根密钥，中继上没有。以后加入的设备通过手机签出的成员状态加入，拿到内容密钥的 grant，不拿根密钥。根密钥只在协议 8 历史等旧材料里用，需要时由 `account secret` 另行保管。新设备通过扫二维码或输入配对码加入账户，双方核对一串相同的 emoji 后才交换密钥。

## 命令

从客户端开的会话和从终端开的会话是同一种会话。命令行常用操作：

| 命令 | 用途 |
|---|---|
| `remote-agent codex` / `remote-agent claude` | 在当前目录启动会话并进入 |
| `remote-agent ls` | 列出会话；ID 可以只写能区分的前几位 |
| `remote-agent attach <id>` | 进入一个正在运行的会话 |
| `remote-agent send <id> <内容>` | 不进入会话，直接发消息 |
| `remote-agent events <id> -f` | 持续查看会话事件 |
| `remote-agent doctor` | 检查已安装的 CLI 是否符合 remote-agent 的预期；升级 Codex 或 Claude Code 后运行一次 |
| `remote-agent account join --server URL --code CODE` | 用 Android App 显示的配对码加入账户 |
| `remote-agent account secret` | 打印旧格式历史的离线解密材料；不能授权新设备，也不能恢复管理权 |

全部命令见 `remote-agent --help`。

## 安全模型

中继不可信。当前手机成员就是管理端，用设备密钥签成员状态；每台设备自己签自己的信封。添加设备只能由手机发起和批准，双方先核对四个图案。删除会轮换内容密钥，中继收到新状态后立即吊销被删设备；被删的是电脑时，还删除它在中继上的机器、会话、事件和图片。保留设备在 60 秒内后台装上。被删设备不能解开新内容；删除状态被执行电脑装上后也不能再授权，最坏等到它本地旧 head 的 72 小时租约过期（中继扣住删除状态时）。

保留端点的操作系统、客户端代码、随机数、私钥和本地安全状态必须可信且不能回滚。已下载内容不能擦除。管理端手机不能普通移除，也没有第二管理端。成员状态的租约是 72 小时：至少这么久打开一次 App，否则远程控制暂停，查看和本地 CLI 继续可用。恢复码在 48 小时后把管理权转到新手机，期间旧手机可以否决；恢复状态把旧手机移出成员表。恢复码只签恢复声明，新手机读不到恢复前的密文；保留电脑仍在运行，会把当前的设备名、账户偏好、会话元数据、会话密钥和当前状态快照用新内容密钥重新发布，所以还在进行的会话能看到，更早的历史只有原来有密钥的设备能读。旧历史与图片明确标记未验证，只供阅读。完整前提见[安全边界](scope.md#设备加入与移除)，升级见[协议 10 指南](self-hosting.zh-CN.md#升级到手机管理端协议-10)。

| 数据 | 中继能否看到 |
|---|---|
| 消息、工具调用、终端输出、审批请求、图片 | 不能 |
| 项目名、路径、git 远端、会话标题、机器名 | 不能 |
| 账户/设备私钥、恢复码、内容密钥和会话密钥 | 不能 |
| 设备、机器和会话 ID，公钥，成员关系，状态 epoch 和租约时间，时间戳，密文大小 | 能，公开签名状态和路由元数据 |
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

## 源码结构

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

从源码构建和运行见[开发指南](development.md)。
