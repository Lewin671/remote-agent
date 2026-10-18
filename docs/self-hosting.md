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

**You need:** a Linux VPS (arm64 or amd64) with Docker and its Compose plugin, ports 80 and 443 open, and a DNS record for your relay domain pointing at it. The relay itself is small: it is limited to 256 MB of memory.

```sh
sudo mkdir -p /opt/remote-agent && sudo chown "$(id -u)" /opt/remote-agent    # not needed as root
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- server /opt/remote-agent
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
- **The setup token** creates your account. By default the relay holds that one account and refuses any other; keep `.env` private all the same. To let other people create accounts on your relay, see [Opening the relay to others](#opening-the-relay-to-others).
- **Your data** is in `/opt/remote-agent/data`: a SQLite database of ciphertext. Back it up by stopping the relay and copying the directory. The keys that decrypt it are not there: they are on your devices (see step 3).

### Opening the relay to others

A relay can hold many accounts, which know nothing of each other: each sees only its own devices, sessions and settings, and like yours, their content is ciphertext the relay cannot read. In `.env`:

```sh
RA_SIGNUP=open          # anyone may create an account, with a proof of work
RA_MAX_ACCOUNTS=100     # at most this many accounts on the relay
```

then `docker compose up -d` (with the `compose.yaml` of v0.41.0 or later, which passes these settings to the relay). With open signup, `remote-agent account init` creates an account without a setup token: the computer spends a second or so on a proof of work, and the relay creates only a few accounts an hour for each address. Such accounts are on a trial plan, with room for 8 devices, 2 computers, 200 sessions and 256 MB of events; accounts made with the setup token have room for 32 devices and 16 computers. The relay checks its free disk space every minute; while it is below 2 GB, no account can add devices, sessions, events, images or settings until there is room again.

The relay counts addresses as Caddy reports them, which the bundled `compose.yaml` arranges with `RA_TRUSTED_PROXIES`. Behind a proxy of your own, set `RA_TRUSTED_PROXIES` to the proxy's address range, or every account looks as if it comes from the proxy.

See and manage the accounts while the relay runs:

```sh
docker compose exec remote-agent-server /remote-agent-server admin -db /data/remote-agent.db accounts
docker compose exec remote-agent-server /remote-agent-server admin -db /data/remote-agent.db suspend <account>   # resume, delete, plan <account> owner|trial
```

`delete` removes an account and everything the relay stored for it, for good, and its devices are disconnected within half a minute. Only the IDs of devices the account removed stay, so that those keys can never register again. The relay never deletes accounts by itself.

## 2. The web client

```sh
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- web
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
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh
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

- Sessions you start from your phone or the web run through your login shell (loading `.zshrc`, `.profile` and so on), and their environment is computed fresh each time: an nvm upgrade, a newly installed command or a moved `codex`/`claude` applies to the next session without reinstalling the service.
- Environment variables your CLIs need, such as proxies, go in `~/.remote-agent/agent.env`, one `KEY=VALUE` per line; what you set there overrides what the shell configuration produces.
- After upgrading Codex or Claude Code, run `remote-agent doctor` to check that it still works with remote-agent. It starts a short real session, so it uses a little of your model quota.
- The service remembers the `PATH` of the shell that installed it; that decides which `codex`/`claude` the daemon itself finds (`doctor` uses it, and version checks follow what sessions run). A session started from a terminal runs the CLI its terminal's `PATH` finds, and does not start if there is none. If `tmux` moves, or you want the daemon to find a different `codex`/`claude`, run `remote-agent-service install` again.
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
- **Android:** install the APK from the [latest release](https://github.com/Lewin671/remote-agent/releases/latest), open it and scan the same QR code. When you later add a device *from the app*, its QR code points at `app.<your domain>` if your relay is `ra.<your domain>`, and at the relay itself otherwise; with other names, pair from the computer as above. The app includes Google's Firebase messaging library and may contact Google to register for pushes, even though your relay sends none.
- **Another computer:** install the daemon there, then `remote-agent account join --server https://ra.example.com --code <pairing code>`.

Now start a session and it appears on every device:

```sh
cd ~/code/my-project
remote-agent codex      # or: remote-agent claude
```

> [!CAUTION]
> `remote-agent account secret` prints root material for legacy history; it does not include the authority or current membership secrets. Keep it offline; it cannot authorize a new device. Enroll new devices by pairing, never paste it into an issue or a chat.

## Upgrading

Before restarting a layout-3 relay, apply the [protocol-8 device enrollment database upgrade](#upgrading-device-enrollment-protocol-8). Older single-account databases first need the [v0.41.0 layout conversion](#upgrading-to-v0410), followed by the layout-3 upgrade.

Upgrade every part to the same release, and read the release notes first: before 1.0, a release may require all parts to move together or stored data to be recreated.

```sh
# computer
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh
# relay: replaces the binary only, your .env, compose.yaml and Caddyfile stay
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- server /opt/remote-agent
cd /opt/remote-agent && docker compose up -d --build
# web client
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | sh -s -- web
npx wrangler pages deploy remote-agent-web --project-name <your project> --branch main
```

After deploying the web client, reload it **twice** on each device: the first load still shows the copy the device already has while it fetches the new one in the background, and the second load runs it. **Settings** shows the version that is running. The Android app offers the update itself in **Settings › Version**. To install a specific release everywhere, set `REMOTE_AGENT_VERSION=vX.Y.Z` before `sh`.

### Upgrading to v0.41.0

v0.41.0 stores the relay's data in a new layout, one that holds many accounts, and does not start on the database of an earlier release: it says so and exits. Move the database once, on the relay's machine, as root (`sudo -i`): the `data` directory belongs to the relay's own user. The release's `compose.yaml` passes the new settings to the relay and adds a healthcheck, so it replaces yours too; if you changed yours, carry your changes over from the copy kept beside it.

```sh
cd /opt/remote-agent
docker compose down                                # keeps your data and certificates
cp -a data data.before-v0.41.0                    # to go back to, if anything goes wrong
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/main/install.sh | REMOTE_AGENT_VERSION=v0.41.0 sh -s -- server /opt/remote-agent
mv compose.yaml compose.yaml.before-v0.41.0
curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent/v0.41.0/deploy/self-host/compose.yaml -o compose.yaml
docker compose build remote-agent-server
# an empty database in the new layout
docker compose run --rm --no-deps --entrypoint /remote-agent-server remote-agent-server admin -db /data/new.db accounts
# your account and everything it stored, moved into it
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

The last command lists your account, with the number of its devices and sessions. Every device stays signed in. Once all is well, remove `data/old-layout.db` and `data.before-v0.41.0`. To go back instead: stop the relay, put `data.before-v0.41.0` back as `data`, and install v0.40.0 the same way.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Relay startup reports layout 3 / layout 4 | Run the [manual enrollment upgrade](#upgrading-device-enrollment-protocol-8) before restarting. |
| The web client signs in but stays "not connected" | A content blocker is stopping the WebSocket. Host the web client and relay under the same registrable domain, or allow your relay's domain in the blocker. |
| The web client cannot reach the relay at all | Its address is missing from `ALLOWED_ORIGINS`, or the relay was not restarted after editing `.env`. |
| `account init` says a valid setup token is required | The token does not match `RA_SETUP_TOKEN` in the relay's `.env`. |
| The relay exits saying the database has the layout of a relay with a single account | Move the database as described in [Upgrading to v0.41.0](#upgrading-to-v0410). |
| `account init` says the relay has no room for another account | This relay already has its account. Join it with `account pair` / `account join`, or let it hold more accounts (see [Opening the relay to others](#opening-the-relay-to-others)). |
| A client asks you to update | Its protocol version differs from the daemon's. Upgrade every part to the same release. |
| No notifications in the Android app | Expected on your own relay; see the limits in the [README](../README.md#status-and-limitations). |



## Upgrading device enrollment (protocol 8)

Protocol 8 requires database layout 4. Upgrade the relay, daemons, web client and apps together. Already enrolled devices and conversation history remain valid; unfinished pairings must be started again. An account secret can no longer enroll a device, including when every device is lost. Keep at least one enrolled device with its local state.

Protocol 9 additionally requires the [security domain upgrade](#upgrading-the-device-security-domain-protocol-9). For an existing layout-3 relay, stop the relay and make a private backup of `data` first. On the relay host, run the following once against `data/remote-agent.db` with SQLite. Check `PRAGMA user_version` is **3** before running it; do not apply this SQL to any other layout.

```sql
BEGIN IMMEDIATE;
CREATE TABLE pairing_admissions (
  pairing_id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id),
  approver_device_id TEXT NOT NULL,
  candidate_device_id TEXT NOT NULL,
  candidate_kind INTEGER NOT NULL,
  approval BLOB NOT NULL,
  signature BLOB NOT NULL,
  candidate_token_hash BLOB NOT NULL,
  expires_ms INTEGER NOT NULL,
  used_ms INTEGER NOT NULL DEFAULT 0,
  canceled_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX pairing_admissions_account ON pairing_admissions(account_id, expires_ms);
PRAGMA user_version = 4;
COMMIT;
```

This is only the intermediate layout 3→4 step. Complete the protocol 9 / layout 5 steps below before starting the current relay. Remove the migration backup after verification according to your own retention policy: restoring the entire old database would also restore old device permissions. No account root or private device key is needed for this SQL.


## Upgrading the device security domain (protocol 9)

This version requires relay layout 5 and daemon storage generation 2. Automatic installation and automatic data migration are disabled. Stop remote activity on every old daemon and client before upgrading all components. Replacing the relay alone is insufficient: a malicious relay can still communicate with old executors. Preserve account identities and history. Do not print account secrets, export private keys, or give local state to the relay.

1. Stop the relay and make a private backup. Check `PRAGMA user_version`. For layout 3, apply the 3→4 SQL above first without starting the old server. Only layout 4 may apply this SQL:

```sql
BEGIN IMMEDIATE;
ALTER TABLE accounts ADD COLUMN security_genesis BLOB;
UPDATE pairing_admissions SET canceled_ms = CAST(strftime('%s','now') AS INTEGER)*1000
 WHERE used_ms = 0 AND canceled_ms = 0;
PRAGMA user_version = 5;
COMMIT;
```

2. Stop each retained daemon and privately back up its local state. After verifying that its `state-generation` file is `1`, manually change it to `2`, preserving owner-only permissions. For older unmarked state, verify its format first; do not blindly add a marker. Keep `account.json`, sessions, queues, images and settings. Do not copy another machine's security database. The new security state is created separately; old acceptance logs are not converted, and old protocol commands are refused.
3. Install all new programs. Choose one trusted computer that normally stays online as the fixed authority. Run `remote-agent account security-init` with its existing account, keep its daemon stopped. Do not initialize separate authorities on the other computers. Run `remote-agent account security-genesis --output public-genesis.bin`. Transfer this **public signed state only** to the relay operator over a trusted channel. Before the relay listens, use the SQLite CLI to pin it to the exact public account ID (the `account-id` from `account status`):

```sql
.bail on
BEGIN IMMEDIATE;
CREATE TEMP TABLE pin_check(n INTEGER CHECK (n = 1));
UPDATE accounts SET security_genesis=readfile('public-genesis.bin')
 WHERE id='PUBLIC_ACCOUNT_ID' AND security_genesis IS NULL
 AND length(readfile('public-genesis.bin')) > 0;
INSERT INTO temp.pin_check VALUES (changes());
COMMIT;
```

Run it as a file, `sqlite3 remote-agent.db < pin.sql`: `.bail on` stops at the check and the open transaction is rolled back unless exactly one unpinned account matched a non-empty file. Never overwrite an existing pin; compare `shasum -a 256 public-genesis.bin` on both ends over the trusted channel, and verify the file came from the chosen trusted computer, not from a relay/device listing. Repeat for each existing account before reopening the relay. Only then start the relay and authority daemon. **Skipping offline pinning allows a still-registered legacy machine to claim the relay domain first and delete relay data using its own fake completions.** Network first-pin is only for a freshly created account with no other enrolled machines; it is not an existing-account migration procedure.
4. Run `remote-agent account pair` on the authority. Other retained computers use `remote-agent account security-enroll --server https://relay.example.com --code CODE`; existing browsers and apps choose the security binding setup. Compare all four symbols: confirm on the candidate first, then allow on the initiating device. This retains the existing Ed25519 identity and history while adding an independent exchange key and pinned domain. Only then start that computer's new daemon.
5. Check every retained identity, directory and history, then exercise a remote command and removal. Old relay registrations that never joined the new domain are not trusted members. They cannot obtain new domain authorization, but should be cleaned up offline as described below. Use normal client removal for domain members; deleting a server row is not cryptographic revocation.

**Manual cleanup of old registrations:** only remove exact public device IDs confirmed as unwanted and never enrolled in the new domain. With the relay stopped, use one transaction to insert their ID, account ID and current millisecond timestamp into `revoked_devices`; delete matching account/device rows from `tokens`, `push_subs`, `fcm_targets` and `devices`. For a machine, first delete its session `events`, then its `images`, `sessions` and `machines`; recompute the account's `event_bytes` from remaining event envelopes. Cancel all unused `pairing_admissions` for that account. Consult `internal/server/store.go` for the schema. Preserve other accounts, retained devices, tombstones and `security_genesis`. Restoring an old backup restores old permissions. This is honest-relay access cleanup, not a substitute for domain key rotation and executor acknowledgements.

The authority computer cannot be removed normally. Authority handoff and root-only recovery are not implemented. If its local security state is lost, stop; do not erase pins and reconstruct trust from the relay. Backups contain decryption material and authorization state, so restrict access and apply your retention policy after acceptance. New enrollment requires the authority online. An offline retained computer keeps removal pending until it installs the fence automatically; browsers and phones do not require individual user approval. Legacy history is displayed as unverified; current approvals use fresh executor snapshots.
