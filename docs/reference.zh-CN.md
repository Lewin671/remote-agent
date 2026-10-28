# 参考

[English](reference.md) · 简体中文

[README](../README.zh-CN.md) 背后的细节：组件、常用命令、安全模型、下载与校验、更新。自己部署中继见[自部署指南](self-hosting.zh-CN.md)。

## 组件

| 组件 | 运行在 | 职责 |
|---|---|---|
| **Daemon**（`remote-agent`） | 每台放代码的电脑，macOS 或 Linux | 在 tmux 里托管 agent 会话，读取 transcript，通过 CLI 的 hook 转交审批，注入输入。同一个二进制也是命令行工具。 |
| **中继**（`remote-agent-server`） | 你掌控的一台 Linux 服务器 | 设备鉴权，存储和转发密文，发送推送通知。读不到内容。 |
| **网页** | 任意 HTTPS 静态托管 | 可安装的 PWA：对话视图、实时终端、审批、Web Push。 |
| **Android App** | Android 8.0 及以上 | 原生客户端，带原生终端，可在通知里直接审批；和网页、iOS 一样，可成为账户的唯一管理端（配对、删除、续租约和恢复）。 |
| **iOS App**（预览） | iPhone 和 iPad，iOS 17 及以上 | SwiftUI 原生客户端，带原生终端、审批和图片。还没有分发，见[现状与限制](../README.zh-CN.md#现状与限制)。 |

建账户的客户端持有账户根密钥，中继上没有。以后加入的设备通过管理端签出的成员状态加入，拿到内容密钥的 grant，不拿根密钥。根密钥只在协议 8 历史等旧材料里用，需要时由 `account secret` 另行保管。新设备通过扫二维码或输入配对码加入账户，双方核对一串相同的 emoji 后才交换密钥。

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
| `remote-agent account join --server URL --code CODE` | 用当前管理端显示的配对码加入账户 |
| `remote-agent account secret` | 打印旧格式历史的离线解密材料；不能授权新设备，也不能恢复管理权 |

全部命令见 `remote-agent --help`。

## 安全模型

中继不可信。当前管理端是一个 Client 成员（网页、Android 或 iOS），用设备密钥签成员状态；每台设备自己签自己的信封。添加设备只能由当前管理端发起和批准，双方先核对四个图案。删除会轮换内容密钥，中继收到新状态后立即吊销被删设备；保留中继档案，另行确认“删除这台电脑的记录”才清除。保留设备在 60 秒内后台装上。被删设备不能解开新内容；删除状态被执行电脑装上后也不能再授权，最坏等到它本地旧 head 的 72 小时租约过期（中继扣住删除状态时）。

保留端点的操作系统、客户端代码、随机数、私钥和本地安全状态必须可信且不能回滚。已下载内容不能擦除。当前管理端不能普通移除，也没有第二管理端；交接管理权由当前管理端批准并移除自己，保留可达历史。独立租约最长 72 小时，建议每天打开管理端并查看实际到期时间；过期后控制与新内容上传暂停，旧历史和本地 CLI 继续。管理端丢失时才用恢复码，诚实中继等待 48 小时，当前管理端可显式否决，普通续期不影响恢复。恢复码是紧急管理权凭证，不直接解开已存密文，但恢复成功会控制保留电脑；恶意中继持有码可绕过等待。保留电脑重试发布当前时期的 checkpoint，接任设备无需读取历史前缀即可看到当前会话。单台成员电脑被攻破会暴露账户范围的可达内容，但 Machine 身份不能向其他机器发送控制。成员链冲突时停止安全操作，由用户核对并明确重新加入；不自动选最长链。旧历史与图片明确标记未验证，只供阅读。完整边界见[安全边界](scope.md#设备加入与移除)，升级见[协议 10 指南](self-hosting.zh-CN.md#升级到手机管理端协议-10)。

任一 Client 都能成为唯一管理端，Machine 不能。网页清理网站数据或换浏览器会丢失管理密钥，恢复码必须离线保存；持久存储许可不是备份，日常建议用 App 管理。iOS 没有后台安全提醒，建议每天打开查看恢复申请；网页推送也不保证送达。

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
- **Daemon**：`remote-agent update check`、`remote-agent update` 和 `remote-agent update status`。Release daemon 用 `remote-agent-service install` 安装后台服务，或根目录 `install.sh` 升级已有服务时，默认在本机时间每天 04:00 自动更新。新装 `install.sh` 只提示服务安装步骤。用 `remote-agent update auto disable` 关闭，`remote-agent update auto enable --time 04:00` 开启或改时间。daemon 状态目录已有的 `update-auto.json` 会保留选择，包括显式关闭，重装和升级也不改变。运行服务安装命令或 Release 安装器时设置 `REMOTE_AGENT_AUTO_UPDATE=0` 可跳过启用，不改变已有选择。设置失败不影响安装成功，会提示手动开启；首次启用失败不留下选择文件，下次安装服务或升级时可重试。源码构建按已安装命令的渠道识别并静默跳过，不自更新。更新时通过 HTTPS 校验大小和 SHA-256，原子替换二进制，tmux 会话不中断，新 daemon 起不来就回滚。改变协议或存储数据的升级不会自动进行，请按发布说明操作。
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
