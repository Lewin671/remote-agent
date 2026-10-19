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

then `docker compose up -d` (with the `compose.yaml` of v0.41.0 or later, which passes these settings to the relay). With open signup, creating an account in the Android app needs no setup token: the phone spends a second or so on a proof of work, and the relay creates only a few accounts an hour for each address. Such accounts are on a trial plan, with room for 8 devices, 2 computers, 200 sessions and 256 MB of events; accounts made with the setup token have room for 32 devices and 16 computers. The relay checks its free disk space every minute; while it is below 2 GB, no account can add devices, sessions, events, images or settings until there is room again.

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

Connect the computer to your relay after the phone has created the account. The phone shows a pairing code:

```sh
remote-agent account join --server https://ra.example.com --code "1234 5678"
```

- Sessions you start from your phone or the web run through your login shell (loading `.zshrc`, `.profile` and so on), and their environment is computed fresh each time: an nvm upgrade, a newly installed command or a moved `codex`/`claude` applies to the next session without reinstalling the service.
- Environment variables your CLIs need, such as proxies, go in `~/.remote-agent/agent.env`, one `KEY=VALUE` per line; what you set there overrides what the shell configuration produces.
- After upgrading Codex or Claude Code, run `remote-agent doctor` to check that it still works with remote-agent. It starts a short real session, so it uses a little of your model quota.
- The service remembers the `PATH` of the shell that installed it; that decides which `codex`/`claude` the daemon itself finds (`doctor` uses it, and version checks follow what sessions run). A session started from a terminal runs the CLI its terminal's `PATH` finds, and does not start if there is none. If `tmux` moves, or you want the daemon to find a different `codex`/`claude`, run `remote-agent-service install` again.
- On Linux, a user service stops when you log out unless lingering is on: `loginctl enable-linger "$USER"`.
- `~/.remote-agent/account.json` holds this computer's keys. Keep it private; losing every device and the recovery code means the relay's data cannot be decrypted.
- The first time Codex starts in a directory it asks whether to trust it; answer in the terminal.

## 4. Your phone and other devices

Install the Android APK from the [latest release](https://github.com/Lewin671/remote-agent/releases/latest) first. In the app, create the account on this relay (it asks for the setup token) and save the recovery code it shows once (the whole text, including its genesis-hash prefix). The phone is the account's manager: only it can add or remove devices, renew the lease and recover the account.

- **Computer:** run `remote-agent account join --server https://ra.example.com --code <code from the phone>`; the running daemon connects within a few seconds. Compare the four symbols on both screens: confirm on the computer first, then allow on the phone.
- **Web:** open `https://app.example.com`, choose the join flow, and enter the code or scan the phone's QR code. Confirm the same four symbols in the browser, then allow on the phone. In the browser's menu, install the page as an app to get a home-screen icon and Web Push notifications.
- **iOS:** join the same way; it is a normal client and cannot manage the account.
- **Another device later:** open the phone's device page and choose add device. The QR code and code come from the phone, not from a computer.
- **Removal:** only from the phone's device page. Browsers and computers show a prompt to use the phone. A removed computer that joins again gets a fresh device identity; its old key stays revoked.
- **Lease:** open the app at least once every 72 hours. After the lease expires, remote control pauses; chat, history and the terminal view keep working until the phone renews it.

Now start a session and it appears on every device:

```sh
cd ~/code/my-project
remote-agent codex      # or: remote-agent claude
```

> [!CAUTION]
> `remote-agent account secret` prints root material for legacy history; it does not include the device key or the recovery code. Keep it offline; it cannot authorize a new device or recover management. Enroll new devices by pairing in the app, never paste it into an issue or a chat.

## Upgrading

Before restarting a layout-3 relay, apply the [protocol-8 device enrollment database upgrade](#upgrading-device-enrollment-protocol-8) and then the [phone authority upgrade](#upgrading-to-the-phone-authority-protocol-10). Older single-account databases first need the [v0.41.0 layout conversion](#upgrading-to-v0410), followed by the layout-3 upgrade.

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
| Relay startup reports layout 3 / layout 4 / layout 5 | Run the [manual device enrollment upgrade](#upgrading-device-enrollment-protocol-8) and then the [phone authority upgrade](#upgrading-to-the-phone-authority-protocol-10) before restarting. |
| The web client signs in but stays "not connected" | A content blocker is stopping the WebSocket. Host the web client and relay under the same registrable domain, or allow your relay's domain in the blocker. |
| The web client cannot reach the relay at all | Its address is missing from `ALLOWED_ORIGINS`, or the relay was not restarted after editing `.env`. |
| The Android app says the setup token is required | The token does not match `RA_SETUP_TOKEN` in the relay's `.env`. |
| The relay exits saying the database has the layout of a relay with a single account | Move the database as described in [Upgrading to v0.41.0](#upgrading-to-v0410). |
| The Android app says the relay has no room for another account | This relay already has its account. Join it with a code from the phone, or let the relay hold more accounts (see [Opening the relay to others](#opening-the-relay-to-others)). |
| A client asks you to update | Its protocol version differs from the daemon's. Upgrade every part to the same release. |
| No notifications in the Android app | Expected on your own relay; see the limits in the [README](../README.md#status-and-limitations). |



## Upgrading device enrollment (protocol 8)

Protocol 8 requires database layout 4. Upgrade the relay, daemons, web client and apps together. Already enrolled devices and conversation history remain valid; unfinished pairings must be started again. An account secret can no longer enroll a device, including when every device is lost. Keep at least one enrolled device with its local state.

Protocol 10 additionally requires the [phone authority upgrade](#upgrading-to-the-phone-authority-protocol-10). For an existing layout-3 relay, stop the relay and make a private backup of `data` first. On the relay host, run the following once against `data/remote-agent.db` with SQLite. Check `PRAGMA user_version` is **3** before running it; do not apply this SQL to any other layout.

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

This is only the intermediate layout 3→4 step. Continue with the 4→5 and 5→6 steps below before starting the current relay. Remove the migration backup after verification according to your own retention policy: restoring the entire old database would also restore old device permissions. No account root or private device key is needed for this SQL.


## Upgrading to the phone authority (protocol 10)

This version requires relay layout 6 and daemon storage generation 3. Stop every old daemon and client before upgrading: components of different protocol versions refuse each other, and a malicious relay could otherwise keep talking to old executors. The Android phone that will manage the account must already be a member before the upgrade; pair it with the current protocol 9 release first. Protocol 10 cannot enroll a phone that was never a member, so an account without such a phone has to be rebuilt and its old data discarded. Do not print account secrets, export private keys, or give local state to the relay.

1. Stop the relay and make a private backup. Check `PRAGMA user_version`. Work through the steps in order until the layout is 6: layout 3 applies the 3→4 SQL above, layout 4 the 4→5 SQL below, and layout 5 the 5→6 SQL. Never apply a step twice.

```sql
BEGIN IMMEDIATE;
ALTER TABLE accounts ADD COLUMN security_genesis BLOB;
UPDATE pairing_admissions SET canceled_ms = CAST(strftime('%s','now') AS INTEGER)*1000
 WHERE used_ms = 0 AND canceled_ms = 0;
PRAGMA user_version = 5;
COMMIT;
```

```sql
BEGIN IMMEDIATE;
CREATE TABLE security_states (
  account_id TEXT NOT NULL,
  epoch INTEGER NOT NULL,
  state BLOB NOT NULL,
  hash BLOB NOT NULL,
  recovery BLOB,
  PRIMARY KEY (account_id, epoch)
);
CREATE TABLE security_recovery (
  account_id TEXT PRIMARY KEY,
  recovery_id TEXT NOT NULL UNIQUE,
  statement BLOB NOT NULL,
  state BLOB NOT NULL,
  new_recovery_ed BLOB NOT NULL,
  submitted_ms INTEGER NOT NULL,
  ready_ms INTEGER NOT NULL,
  completed_ms INTEGER NOT NULL DEFAULT 0,
  rejected_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX security_recovery_new_ed ON security_recovery(new_recovery_ed);
ALTER TABLE sessions ADD COLUMN key_content_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN recovery_ed BLOB;
CREATE INDEX accounts_recovery ON accounts(recovery_ed);
-- Mark every account that predates the phone authority. They must pin their
-- genesis offline (step 5) instead of over the network.
ALTER TABLE accounts ADD COLUMN offline_pin INTEGER NOT NULL DEFAULT 0;
UPDATE accounts SET offline_pin = 1;
UPDATE accounts SET security_genesis = NULL;
UPDATE pairing_admissions SET canceled_ms = CAST(strftime('%s','now') AS INTEGER)*1000
 WHERE used_ms = 0 AND canceled_ms = 0;
PRAGMA user_version = 6;
COMMIT;
```

Clearing `security_genesis` discards the protocol 9 pin. Every account on the relay has to pin the protocol 10 genesis before it can use remote control.

2. Stop each retained daemon and make a private backup of its local state. After the backup, delete the protocol 9 security files (`security-v2.db` and, on the old authority computer, `authority-v2.db`); keep `account.json`, sessions, queues, images and settings. Write `3` to the `state-generation` file, preserving owner-only permissions. The new daemon refuses generation 2 and never converts the old security state. Protocol 9 content keys are not imported: if you need protocol 9 history, read it with the old version or keep the old state backup, because the protocol 10 daemon cannot open it. Protocol 8 and earlier history still needs the account root.

3. Install all new programs. For an existing account, keep the relay stopped and pin offline in step 5. For a fresh relay with no account, start it now; the phone creates the account and pins over the network. It does not accept protocol 9 clients.

4. On the Android phone, update the app. It sees the existing account without a protocol 10 security state and offers **rebuild the security domain and become the authority**. Keep the account root and the phone's Ed25519 device identity; discard the protocol 9 security state; generate a new recovery code and the genesis state (the phone is the only member). There is no separate authority key: the phone member signs states with its existing device key. The app shows the new recovery code once and does not keep it — save it offline before continuing. If the account was upgraded from layout 5, the relay refuses the network pin and the app tells you to pin offline in step 5; the genesis is still built locally and can be exported. Protocol 9 accounts written by older app versions lack the account public key in their local account file; the app derives it from the retained root once and rewrites the file. The recovery code only signs a recovery statement: a new phone will not decrypt content from before the recovery. Retained computers re-publish their current session metadata, session keys and current-state snapshots under the new content key, so live sessions reappear; older history stays on devices that already had it.

5. Pin the genesis. An existing account must be pinned offline before the relay starts: old device tokens are still in the database and could otherwise race the phone to the first pin. A newly created account has no earlier tokens, so the phone may pin over the network. On a relay upgraded from layout 5, every pre-existing account is marked `offline_pin = 1` by the migration and the relay refuses `PinSecurityDomain` over the network for it, so the offline pin below must be done before the relay starts. There is no switch: a fresh account (created after the upgrade) is not marked and may pin over the network. On the Android phone, open **设备** (Devices) and tap **导出创世状态（中继离线固定用）** (Export genesis for the relay's offline pin), then share the resulting `public-genesis.bin` to the computer that holds the relay. It is the public genesis state and contains no keys. Transfer it over a trusted channel, and run this with the relay stopped:

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

Run it as a file, `sqlite3 remote-agent.db < pin.sql`: `.bail on` stops at the check and the open transaction is rolled back unless exactly one unpinned account matched a non-empty file. Compare `shasum -a 256 public-genesis.bin` on both ends over the trusted channel and verify the file came from the phone, not from a relay or device listing. On startup the server seeds epoch 1 into `security_states` from the pinned bytes and runs the full genesis validation (state version, one Client member, `AuthorityID` and member Ed key match, non-zero `RecoveryEd`); if it fails, that account refuses all RPCs until the pin is fixed. There is no separate admin step. Repeat for each existing account, then start the relay if it is still stopped; with network pinning it is already running. Skipping offline pinning makes the first pin depend on the relay; the phone still holds the only signing key, so a malicious relay can refuse or misdirect setup but cannot forge a genesis.

6. On each retained computer, run `remote-agent account join --server https://relay.example.com --code CODE` with the code shown by the phone. This overwrites the old `security` block in `account.json` and ignores any old authority fields. Existing browsers and apps choose to pair again. Compare all four symbols: confirm on the candidate first, then allow on the phone. Re-pairing keeps the Ed25519 identity and replaces the exchange key; start that computer's new daemon only after joining. Remove the migration backup after verification: restoring it restores old permissions.

Old protocol 9 registrations that are not paired again are not members. After the genesis exists, the relay checks every logged-in device against the signed member list and refuses anything else, so a leftover row or bearer token cannot read the account or upload state. Delete those rows offline if you want the device list clean; re-pairing replaces a retained device's row. The protocol 10 authority phone cannot be removed normally: it has no second authority and no immediate handoff. Every account on a relay upgraded from layout 5 must be pinned before the relay listens; an account without a protocol 9 phone member must be rebuilt and its data discarded. Put that in the release notes. A new phone takes over through the recovery code after 48 hours, during which the old phone can veto; the recovery state removes the old phone from membership, and the new phone holds a new code with which it can recover again later. The code carries the first 10 bytes of the pinned genesis hash, so a relay holding the code still cannot substitute a forged chain. It cannot decrypt content from before the recovery; retained computers keep the history they already hold and re-publish current-state session snapshots under the new key. Open the app at least once every 72 hours to keep remote control; after that, viewing continues but control pauses until the phone renews the lease. Backups contain decryption material and authorization state, so restrict access and apply your retention policy after acceptance. Legacy history is displayed as unverified; current approvals use fresh executor snapshots.
