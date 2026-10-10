# 自部署指南

[English](self-hosting.md) · 简体中文

一套完整的部署有三部分，各在不同的机器上，大约二十分钟：

| 部分 | 放在哪 | 下文用的示例地址 |
|---|---|---|
| 中继（`remote-agent-server`） | 一台 Linux VPS | `https://ra.example.com` |
| 网页 | 静态托管，比如 Cloudflare Pages | `https://app.example.com` |
| daemon（`remote-agent`） | 每一台运行 Codex 或 Claude Code 的电脑 | |

中继和网页请放在**同一个注册域名**下（`ra.example.com` 和 `app.example.com`）。有些广告拦截扩展会拦掉发往其他站点的 WebSocket，表现为网页能登录但一直「未连接」。

## 1. 中继

**需要：** 一台 Linux VPS（arm64 或 amd64），装有 Docker 和 Compose 插件，开放 80 和 443 端口，中继域名已解析到它。中继本身很小，内存上限 128 MB。

```sh
sudo mkdir -p /opt/remote-agent && sudo chown "$(id -u)" /opt/remote-agent    # 以 root 操作时不需要
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- server /opt/remote-agent
```

安装脚本本身不会要求 root。如果不是以 root 运行，它最后会提示你把 `data` 目录交给中继使用的非特权用户：`sudo chown 65534:65534 /opt/remote-agent/data`，执行一次即可。

它把中继的文件放进 `/opt/remote-agent`，并生成带有随机 setup token 的 `.env`。编辑 `.env`：

```sh
RELAY_DOMAIN=ra.example.com               # 这台中继的域名
ALLOWED_ORIGINS=https://app.example.com   # 你的网页将要放在哪
```

然后启动：

```sh
cd /opt/remote-agent
docker compose up -d --build
docker compose logs -f        # 第一次启动时 Caddy 会申请 HTTPS 证书
```

- **中继的地址**就是 `https://ra.example.com` 这样不带路径的地址，中继不能放在子路径下。
- **已经有反向代理？** 删掉 `compose.yaml` 里的 `caddy` 服务，把中继域名反代到 `remote-agent-server` 容器的 8080 端口，注意要放行 WebSocket。
- **setup token** 只用来创建账户，一次。账户建好以后中继不再接受别的账户，token 也不再使用。但 `.env` 仍然要保密。
- **数据**在 `/opt/remote-agent/data`：一个存密文的 SQLite 数据库。备份方式是停掉中继后复制这个目录。解密它的密钥不在这里，在你自己的设备上（见第 3 步）。

## 2. 网页

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- web
```

静态文件在 `./remote-agent-web`，放到任何支持 HTTPS 的地方都可以。

**Cloudflare Pages**（推荐）：先建一个 Pages 项目，然后

```sh
npx wrangler login
npx wrangler pages deploy remote-agent-web --project-name <你的项目名> --branch main
```

再把 `app.example.com` 绑成项目的自定义域名。包里带有 `_headers` 文件，Pages 会照它设置响应头：严格的内容安全策略，以及带哈希的资源的长期缓存。

**其他静态托管**也可以，但有两个条件。文件要放在独立域名的根路径（`https://app.example.com/`，不能是子路径）：service worker、资源和配对链接都按根路径写的。托管平台不认 `_headers` 的话，请自己配置同样的响应头：页面的 meta 标签里也带着内容安全策略，但 `frame-ancestors` 和该文件里的其他保护只有作为真正的响应头才生效。

> [!IMPORTANT]
> 不要把网页放在中继所在的 VPS 上。中继看不到你的密钥，但提供页面 JavaScript 的一方看得到。两者放在不同的服务商，攻击者就必须同时攻破两处。

不管选哪个地址，都要写进中继的 `ALLOWED_ORIGINS`。

## 3. 你的电脑

**需要：** macOS 或 Linux，tmux 3.2 以上，`codex` 和/或 `claude` 已安装并登录。

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh
```

它把 `remote-agent` 和 `remote-agent-service` 装进 `~/.local/bin`（用 `BIN_DIR` 改位置）。然后：

```sh
remote-agent-service install
```

让 daemon 在后台运行，并在每次登录时自动启动（macOS 用 launchd，Linux 用 systemd 用户服务）。日常用 `remote-agent-service status | log | restart | stop | uninstall` 管理。重启 daemon 不会打断 tmux 里正在运行的会话。

把电脑连到你的中继，过程中会要求输入中继 `.env` 里的 setup token：

```sh
remote-agent account init --server https://ra.example.com
```

- CLI 需要的环境变量（比如代理）写进 `~/.remote-agent/agent.env`，一行一个 `KEY=VALUE`。
- 升级 Codex 或 Claude Code 之后运行 `remote-agent doctor`，检查它和 remote-agent 是否仍然配合正常。它会启动一个很短的真实会话，所以会用掉一点模型额度。
- 后台服务记住的是安装它时那个 shell 的 `PATH`。`codex`、`claude` 或 `tmux` 换了位置，就重新运行一次 `remote-agent-service install`。
- Linux 上，用户服务在你退出登录后会停止，除非打开 lingering：`loginctl enable-linger "$USER"`。
- `~/.remote-agent/account.json` 保存着这台电脑的密钥。请妥善保管；如果加入账户的所有设备都丢了，中继上的数据就无法解密。
- Codex 第一次在某个目录启动时会问是否信任该目录，需要在终端里回答。

## 4. 手机和其他设备

在电脑上：

```sh
remote-agent account pair --web https://app.example.com
```

它会显示二维码、链接和配对码。

- **网页：** 扫二维码或打开链接，确认两边显示的 emoji 相同。在浏览器菜单里把页面安装成应用，就有主屏幕图标和 Web Push 通知。
- **Android：** 从[最新 Release](https://github.com/Lewin671/remote-agent-releases/releases/latest) 安装 APK，打开后扫同一个二维码。以后*在 App 里*添加设备时，如果中继是 `ra.<你的域名>`，二维码会指向 `app.<你的域名>`，否则指向中继本身；用了别的命名时，请像上面那样从电脑上配对。App 内含 Google 的 Firebase 推送库，即使你的中继不发推送，它也可能连接 Google 注册推送。
- **另一台电脑：** 在那台电脑上装好 daemon，然后 `remote-agent account join --server https://ra.example.com --code <配对码>`。

现在启动一个会话，它会出现在所有设备上：

```sh
cd ~/code/my-project
remote-agent codex      # 或者：remote-agent claude
```

> [!CAUTION]
> `remote-agent account secret` 打印的是能解开所有会话的密钥。只粘贴到你自己的设备上，不要贴进 issue 或聊天里。

## 升级

所有部分升级到同一个版本，并先读发布说明：1.0 之前，某个版本可能要求各部分一起升级，或者重建存储的数据。

```sh
# 电脑
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh
# 中继：只替换二进制，你的 .env、compose.yaml 和 Caddyfile 保持不变
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- server /opt/remote-agent
cd /opt/remote-agent && docker compose up -d --build
# 网页
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- web
npx wrangler pages deploy remote-agent-web --project-name <你的项目名> --branch main
```

网页部署后，在每台设备上重新加载**两次**：第一次加载显示的仍是设备上已有的那份，同时在后台取回新的；第二次加载才运行新的。**设置**里能看到正在运行的版本。Android App 会在**设置 › 版本**里自己提示更新。要在各处安装某个指定版本，在 `sh` 前面设置 `REMOTE_AGENT_VERSION=vX.Y.Z`。

## 常见问题

| 现象 | 可能的原因 |
|---|---|
| 网页能登录但一直「未连接」 | 广告拦截扩展挡住了 WebSocket。把网页和中继放在同一个注册域名下，或在扩展里放行中继的域名。 |
| 网页完全连不上中继 | 网页地址没有写进 `ALLOWED_ORIGINS`，或者改完 `.env` 后没有重启中继。 |
| `account init` 提示需要有效的 setup token | 输入的 token 和中继 `.env` 里的 `RA_SETUP_TOKEN` 不一致。 |
| `account init` 提示设备属于另一个账户 | 这台中继已经有账户了。用 `account pair` / `account join` 加入它，或者停掉中继、删除 `data` 目录后重新开始。 |
| 客户端提示需要更新 | 它的协议版本和 daemon 的不一样。把所有部分升级到同一个版本。 |
| Android App 收不到通知 | 在自己的中继上是预期行为，见 [README](../README.zh-CN.md#现状与限制) 里的限制。 |
