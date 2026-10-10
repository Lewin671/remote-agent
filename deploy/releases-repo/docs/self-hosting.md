# Self-hosting guide

English · [简体中文](self-hosting.zh-CN.md)

A complete setup has three parts, each on a different machine, and takes about twenty minutes:

| Part | Where | Example address used below |
|---|---|---|
| Relay (`remote-agent-server`) | A Linux VPS | `https://ra.example.com` |
| Web client | A static host, such as Cloudflare Pages | `https://app.example.com` |
| Daemon (`remote-agent`) | Each computer where you run Codex or Claude Code | |

Put the relay and the web client under **the same registrable domain** (`ra.example.com` and `app.example.com`). Some content blockers stop WebSockets to a different site, and the web client then signs in but stays "not connected".

## 1. The relay

**You need:** a Linux VPS (arm64 or amd64) with Docker and its Compose plugin, ports 80 and 443 open, and a DNS record for your relay domain pointing at it. The relay itself is small: it is limited to 128 MB of memory.

```sh
sudo mkdir -p /opt/remote-agent && sudo chown "$(id -u)" /opt/remote-agent    # not needed as root
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- server /opt/remote-agent
```

The installer itself never asks for root. Unless you run it as root, it ends by telling you to hand the `data` directory to the relay's unprivileged user: `sudo chown 65534:65534 /opt/remote-agent/data`. Do that once.

This puts the relay's files in `/opt/remote-agent` and writes a `.env` with a new random setup token. Edit `.env`:

```sh
RELAY_DOMAIN=ra.example.com               # this relay's domain
ALLOWED_ORIGINS=https://app.example.com   # where your web client will be
```

Then start it:

```sh
cd /opt/remote-agent
docker compose up -d --build
docker compose logs -f        # Caddy obtains the HTTPS certificate on first start
```

- **The relay's address** is the bare origin, `https://ra.example.com`, with no path after it; the relay cannot live under a subpath.
- **Already running a reverse proxy?** Remove the `caddy` service from `compose.yaml` and proxy your relay domain to the `remote-agent-server` container on port 8080. WebSockets must pass through.
- **The setup token** creates the account, once. After the account exists the relay accepts no other account, and the token is no longer used. Keep `.env` private all the same.
- **Your data** is in `/opt/remote-agent/data`: a SQLite database of ciphertext. Back it up by stopping the relay and copying the directory. The keys that decrypt it are not there: they are on your devices (see step 3).

## 2. The web client

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- web
```

This leaves the static files in `./remote-agent-web`. Host them anywhere that serves HTTPS.

**Cloudflare Pages** (recommended): create a Pages project, then

```sh
npx wrangler login
npx wrangler pages deploy remote-agent-web --project-name <your project> --branch main
```

and add `app.example.com` as the project's custom domain. The bundle includes a `_headers` file, which Pages applies: a strict Content-Security-Policy and long-lived caching for hashed assets.

**Any other static host** works too, with two conditions. Serve the files at the root of their own origin (`https://app.example.com/`, not a subpath): the service worker, assets and pairing links assume it. And if the host does not read `_headers`, configure the same response headers yourself: the page carries its Content-Security-Policy in a meta tag as well, but `frame-ancestors` and the other protections in that file only work as real headers.

> [!IMPORTANT]
> Do not serve the web client from the relay's VPS. The relay never sees your keys, but whoever serves the page's JavaScript could. Keeping the two on separate providers means an attacker has to break both.

Whatever address you choose must be listed in the relay's `ALLOWED_ORIGINS`.

## 3. Your computer

**You need:** macOS or Linux, tmux 3.2 or newer, and `codex` and/or `claude` installed and logged in.

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh
```

This installs `remote-agent` and `remote-agent-service` in `~/.local/bin` (set `BIN_DIR` to change that). Then:

```sh
remote-agent-service install
```

runs the daemon in the background, now and at every login (launchd on macOS, systemd user service on Linux). `remote-agent-service status | log | restart | stop | uninstall` manage it. Restarting the daemon does not interrupt sessions running in tmux.

Connect the computer to your relay. This asks for the setup token from the relay's `.env`:

```sh
remote-agent account init --server https://ra.example.com
```

- Environment variables your CLIs need, such as proxies, go in `~/.remote-agent/agent.env`, one `KEY=VALUE` per line.
- After upgrading Codex or Claude Code, run `remote-agent doctor` to check that it still works with remote-agent. It starts a short real session, so it uses a little of your model quota.
- The service remembers the `PATH` of the shell that installed it. If `codex`, `claude` or `tmux` move, run `remote-agent-service install` again.
- On Linux, a user service stops when you log out unless lingering is on: `loginctl enable-linger "$USER"`.
- `~/.remote-agent/account.json` holds this computer's keys. Keep it private; if you lose every device that has joined the account, the relay's data cannot be decrypted.
- The first time Codex starts in a directory it asks whether to trust it; answer in the terminal.

## 4. Your phone and other devices

On the computer:

```sh
remote-agent account pair --web https://app.example.com
```

This shows a QR code, a link and a pairing code.

- **Web:** scan the QR code or open the link, and confirm that both sides show the same emoji. In the browser's menu, install the page as an app to get a home-screen icon and Web Push notifications.
- **Android:** install the APK from the [latest release](https://github.com/Lewin671/remote-agent-releases/releases/latest), open it and scan the same QR code. When you later add a device *from the app*, its QR code points at `app.<your domain>` if your relay is `ra.<your domain>`, and at the relay itself otherwise; with other names, pair from the computer as above. The app includes Google's Firebase messaging library and may contact Google to register for pushes, even though your relay sends none.
- **Another computer:** install the daemon there, then `remote-agent account join --server https://ra.example.com --code <pairing code>`.

Now start a session and it appears on every device:

```sh
cd ~/code/my-project
remote-agent codex      # or: remote-agent claude
```

> [!CAUTION]
> `remote-agent account secret` prints the key that decrypts every session. Paste it only into your own devices, never into an issue or a chat.

## Upgrading

Upgrade every part to the same release, and read the release notes first: before 1.0, a release may require all parts to move together or stored data to be recreated.

```sh
# computer
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh
# relay: replaces the binary only, your .env, compose.yaml and Caddyfile stay
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- server /opt/remote-agent
cd /opt/remote-agent && docker compose up -d --build
# web client
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- web
npx wrangler pages deploy remote-agent-web --project-name <your project> --branch main
```

After deploying the web client, reload it **twice** on each device: the first load still shows the copy the device already has while it fetches the new one in the background, and the second load runs it. **Settings** shows the version that is running. The Android app offers the update itself in **Settings › Version**. To install a specific release everywhere, set `REMOTE_AGENT_VERSION=vX.Y.Z` before `sh`.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| The web client signs in but stays "not connected" | A content blocker is stopping the WebSocket. Host the web client and relay under the same registrable domain, or allow your relay's domain in the blocker. |
| The web client cannot reach the relay at all | Its address is missing from `ALLOWED_ORIGINS`, or the relay was not restarted after editing `.env`. |
| `account init` says a valid setup token is required | The token does not match `RA_SETUP_TOKEN` in the relay's `.env`. |
| `account init` says the device belongs to another account | This relay already has an account. Join it with `account pair` / `account join`, or start over by stopping the relay and removing its `data` directory. |
| A client asks you to update | Its protocol version differs from the daemon's. Upgrade every part to the same release. |
| No notifications in the Android app | Expected on your own relay; see the limits in the [README](../README.md#status-and-limits). |
