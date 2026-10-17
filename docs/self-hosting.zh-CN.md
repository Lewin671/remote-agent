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

**需要：** 一台 Linux VPS（arm64 或 amd64），装有 Docker 和 Compose 插件，开放 80 和 443 端口，中继域名已解析到它。中继本身很小，内存上限 256 MB。

```sh
sudo mkdir -p /opt/remote-agent && sudo chown "$(id -u)" /opt/remote-agent    # 以 root 操作时不需要
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- server /opt/remote-agent
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
- **setup token** 用来创建你的账户。默认情况下中继只容纳这一个账户，拒绝其他账户；`.env` 仍然要保密。想让别人也在你的中继上建账户，见[向他人开放中继](#向他人开放中继)。
- **数据**在 `/opt/remote-agent/data`：一个存密文的 SQLite 数据库。备份方式是停掉中继后复制这个目录。解密它的密钥不在这里，在你自己的设备上（见第 3 步）。

### 向他人开放中继

一台中继可以容纳多个账户，账户之间互不相知：每个账户只看得到自己的设备、会话和设置；和你的一样，它们的内容也是中继读不了的密文。在 `.env` 里写：

```sh
RA_SIGNUP=open          # 任何人都能建账户，需要完成一次工作量证明
RA_MAX_ACCOUNTS=100     # 这台中继最多容纳的账户数
```

然后执行 `docker compose up -d`（需要 v0.41.0 及以后的 `compose.yaml`，它会把这些设置传给中继）。开放注册后，`remote-agent account init` 不需要 setup token 就能建账户：电脑花一秒左右完成工作量证明，同一个地址每小时只能建几个账户。这样建的账户使用试用额度：8 台设备、2 台电脑、200 个会话、256 MB 事件；用 setup token 建的账户可以有 32 台设备、16 台电脑。磁盘剩余空间不足 2 GB 时，所有账户都不能再添加设备、会话、事件、图片或设置，直到腾出空间。

中继按 Caddy 报告的地址计数，自带的 `compose.yaml` 已经用 `RA_TRUSTED_PROXIES` 配好。如果放在你自己的反向代理后面，把 `RA_TRUSTED_PROXIES` 设成代理所在的地址段，否则所有账户看起来都来自代理这一个地址。

中继运行时查看和管理账户：

```sh
docker compose exec remote-agent-server /remote-agent-server admin -db /data/remote-agent.db accounts
docker compose exec remote-agent-server /remote-agent-server admin -db /data/remote-agent.db suspend <账户>   # 另有 resume、delete、plan <账户> owner|trial
```

`delete` 会永久删除一个账户以及中继为它存的所有数据，它的设备在半分钟内断开。只有这个账户删除过的设备 ID 会保留下来，这些密钥永远不能再登记。中继自己从不删除账户。

## 2. 网页

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- web
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
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh
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

- 从手机或网页新开的会话由你自己的登录 shell 启动（加载 `.zshrc`、`.profile` 等），环境每次现算：升级 nvm、装新命令、`codex`/`claude` 换了位置都在下一个会话生效，不用重新安装服务。
- CLI 需要的环境变量（比如代理）写进 `~/.remote-agent/agent.env`，一行一个 `KEY=VALUE`；这里设置的值会覆盖 shell 配置算出来的结果。
- 升级 Codex 或 Claude Code 之后运行 `remote-agent doctor`，检查它和 remote-agent 是否仍然配合正常。它会启动一个很短的真实会话，所以会用掉一点模型额度。
- 后台服务记住的是安装它时那个 shell 的 `PATH`，daemon 自己据此找 `codex`/`claude`（`doctor` 用它，版本检查跟着会话实际跑的那个）。从终端开的会话跑的是那个终端的 `PATH` 里找到的 CLI，找不到就不启动。`tmux` 换了位置，或想让 daemon 找到另一个 `codex`/`claude`，就重新运行一次 `remote-agent-service install`。
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
- **Android：** 从[最新 Release](https://github.com/Lewin671/remote-agent/releases/latest) 安装 APK，打开后扫同一个二维码。以后*在 App 里*添加设备时，如果中继是 `ra.<你的域名>`，二维码会指向 `app.<你的域名>`，否则指向中继本身；用了别的命名时，请像上面那样从电脑上配对。App 内含 Google 的 Firebase 推送库，即使你的中继不发推送，它也可能连接 Google 注册推送。
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
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh
# 中继：只替换二进制，你的 .env、compose.yaml 和 Caddyfile 保持不变
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- server /opt/remote-agent
cd /opt/remote-agent && docker compose up -d --build
# 网页
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- web
npx wrangler pages deploy remote-agent-web --project-name <你的项目名> --branch main
```

网页部署后，在每台设备上重新加载**两次**：第一次加载显示的仍是设备上已有的那份，同时在后台取回新的；第二次加载才运行新的。**设置**里能看到正在运行的版本。Android App 会在**设置 › 版本**里自己提示更新。要在各处安装某个指定版本，在 `sh` 前面设置 `REMOTE_AGENT_VERSION=vX.Y.Z`。

### 升级到 v0.41.0

v0.41.0 换了中继数据的存储格式，以便容纳多个账户；它不会在旧版本的数据库上启动，而是提示原因后退出。以 root 身份（`sudo -i`）在中继所在的机器上迁移一次数据库：`data` 目录属于中继自己的用户。新版的 `compose.yaml` 会把新设置传给中继，并加上健康检查，所以也要替换；如果你改过自己的那份，照着旁边留下的副本把改动搬过去。

```sh
cd /opt/remote-agent
docker compose down                                # 数据和证书都保留
cp -a data data.before-v0.41.0                    # 出问题时用来回退
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | REMOTE_AGENT_VERSION=v0.41.0 sh -s -- server /opt/remote-agent
mv compose.yaml compose.yaml.before-v0.41.0
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/v0.41.0/deploy/self-host/compose.yaml -o compose.yaml
docker compose build remote-agent-server
# 新格式的空数据库
docker compose run --rm --no-deps --entrypoint /remote-agent-server remote-agent-server admin -db /data/new.db accounts
# 把你的账户和它存的全部数据搬进去
docker run --rm -i -v "$PWD/data:/data" alpine:3 sh -eu <<'EOF'
apk add -q sqlite
cd /data
test -f new.db
# Out of WAL mode, so that each database is one file, complete in itself.
sqlite3 remote-agent.db "PRAGMA journal_mode = DELETE" >/dev/null
sqlite3 new.db "PRAGMA journal_mode = DELETE" >/dev/null
sqlite3 remote-agent.db "SELECT writefile('/tmp/account-key', public_key) FROM account" >/dev/null
ACCT=$(sha256sum /tmp/account-key | cut -c1-32)
sqlite3 -bail new.db >/dev/null <<SQL
ATTACH 'remote-agent.db' AS old;
BEGIN;
INSERT INTO accounts (id, public_key, plan, created_ms, last_active_ms, registration_floor_ms)
  SELECT '$ACCT', public_key, 'owner', created_ms, created_ms,
    COALESCE((SELECT freshness_floor_ms FROM old.registration_policy), 0) FROM old.account;
INSERT INTO devices (id, account_id, public_key, kind, registration, signature, created_ms, meta, meta_revision, last_seen_ms)
  SELECT id, '$ACCT', public_key, kind, registration, signature, created_ms, meta, meta_revision, last_seen_ms FROM old.devices;
INSERT INTO revoked_devices (id, account_id, revoked_ms) SELECT id, '$ACCT', revoked_ms FROM old.revoked_devices;
INSERT INTO tokens (hash, account_id, device_id, expires_ms) SELECT hash, '$ACCT', device_id, expires_ms FROM old.tokens;
INSERT INTO machines (id, account_id, meta, meta_revision, last_seen_ms) SELECT id, '$ACCT', meta, meta_revision, last_seen_ms FROM old.machines;
INSERT INTO sessions (account_id, session_id, machine_id, key, meta, meta_revision, last_seq, updated_ms, ended_ms)
  SELECT '$ACCT', session_id, machine_id, key, meta, meta_revision, last_seq, updated_ms, ended_ms FROM old.sessions;
INSERT INTO events (account_id, session_id, seq, envelope) SELECT '$ACCT', session_id, seq, envelope FROM old.events;
INSERT INTO images (account_id, session_id, image_id, machine_id, envelope, bytes, created_ms, expires_ms)
  SELECT '$ACCT', session_id, image_id, machine_id, envelope, bytes, created_ms, expires_ms FROM old.images;
INSERT INTO account_prefs (account_id, object_id, revision, envelope, updated_ms) SELECT '$ACCT', object_id, revision, envelope, updated_ms FROM old.account_prefs;
INSERT INTO push_subs (device_id, account_id, subscription, kinds) SELECT device_id, '$ACCT', subscription, kinds FROM old.push_subs;
INSERT INTO fcm_targets (device_id, account_id, token, kinds) SELECT device_id, '$ACCT', token, kinds FROM old.fcm_targets;
INSERT OR REPLACE INTO kv (key, value) SELECT key, value FROM old.kv;
UPDATE accounts SET event_bytes = (SELECT COALESCE(SUM(LENGTH(envelope)), 0) FROM events);
COMMIT;
PRAGMA journal_mode = DELETE;
SQL
rm -f remote-agent.db-shm new.db-shm
mv remote-agent.db old-layout.db
mv new.db remote-agent.db
chown 65534:65534 remote-agent.db old-layout.db
echo "moved account $ACCT"
EOF
docker compose up -d
docker compose exec remote-agent-server /remote-agent-server admin -db /data/remote-agent.db accounts
```

最后一条命令会列出你的账户，以及它的设备数和会话数，所有设备保持登录。确认没问题后，删掉 `data/old-layout.db` 和 `data.before-v0.41.0`。如果要回退：停掉中继，把 `data.before-v0.41.0` 改回 `data`，再用同样的方式安装 v0.40.0。

## 常见问题

| 现象 | 可能的原因 |
|---|---|
| 网页能登录但一直「未连接」 | 广告拦截扩展挡住了 WebSocket。把网页和中继放在同一个注册域名下，或在扩展里放行中继的域名。 |
| 网页完全连不上中继 | 网页地址没有写进 `ALLOWED_ORIGINS`，或者改完 `.env` 后没有重启中继。 |
| `account init` 提示需要有效的 setup token | 输入的 token 和中继 `.env` 里的 `RA_SETUP_TOKEN` 不一致。 |
| 中继启动后退出，提示数据库是单账户中继的格式 | 按[升级到 v0.41.0](#升级到-v0410) 迁移数据库。 |
| `account init` 提示中继没有空间容纳另一个账户 | 这台中继已经有它的账户了。用 `account pair` / `account join` 加入它，或者让它容纳更多账户（见[向他人开放中继](#向他人开放中继)）。 |
| 客户端提示需要更新 | 它的协议版本和 daemon 的不一样。把所有部分升级到同一个版本。 |
| Android App 收不到通知 | 在自己的中继上是预期行为，见 [README](../README.zh-CN.md#现状与限制) 里的限制。 |
