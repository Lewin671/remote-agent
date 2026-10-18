# Reference

English · [简体中文](reference.zh-CN.md)

The details behind the [README](../README.md): the components, everyday commands, the security model, downloads and verification, and updating. To set up your own relay, see the [self-hosting guide](self-hosting.md).

## Components

| Component | Runs on | Role |
|---|---|---|
| **Daemon** (`remote-agent`) | Each computer with your code — macOS or Linux | Hosts agent sessions in tmux, reads their transcripts, relays approvals through CLI hooks, injects input. The same binary is the command-line interface. |
| **Relay** (`remote-agent-server`) | A Linux server you control | Authenticates devices, stores and forwards ciphertext, sends push notifications. Cannot read content. |
| **Web client** | Any static HTTPS host | Installable PWA: conversation view, live terminal, approvals, Web Push. |
| **Android app** | Android 8.0 or later | Native client with a native terminal and approvals from the notification. |
| **iOS app** (preview) | iPhone and iPad, iOS 17 or later | Native SwiftUI client with a native terminal, approvals and images. Not distributed yet — see [Status and limitations](../README.md#status-and-limitations). |

Every device holds the account key; the relay holds none. Devices join an account by scanning a QR code or entering a pairing code, and both sides confirm a short emoji sequence before keys are exchanged.

## Commands

Sessions started from a client and sessions started in a terminal are the same kind of session. From the command line:

| Command | Purpose |
|---|---|
| `remote-agent codex` / `remote-agent claude` | Start a session in the current directory and attach to it |
| `remote-agent ls` | List sessions; IDs may be shortened to any unique prefix |
| `remote-agent attach <id>` | Attach to a running session |
| `remote-agent send <id> <text>` | Send a message without attaching |
| `remote-agent events <id> -f` | Follow a session's events |
| `remote-agent doctor` | Check that the installed CLIs behave as remote-agent expects; run it after upgrading Codex or Claude Code |
| `remote-agent account pair` | Add another device to the account |
| `remote-agent account security-init` | Establish the fixed authority on the chosen existing computer |
| `remote-agent account security-enroll --server URL --code CODE` | Bind a retained computer without replacing its identity |

`remote-agent --help` lists every command.

## Security model

The relay is untrusted. Devices pin a fixed authority daemon after comparing pairing symbols locally on both devices. Removal rotates content keys and waits for every retained executor to install its fence automatically; offline computers keep it pending. After completion, old root/session keys and a removed device's private keys cannot authorize new control or decrypt new content.

Retained endpoints, their OS/client code, randomness and local security state must remain trusted and must not be rolled back. Already downloaded content cannot be erased. The authority computer cannot be removed normally; handoff and root-only recovery are not implemented. When it is offline, new remote authorization pauses while the local CLI continues. Legacy history and images are marked unverified and read-only. See the [full security boundary](scope.md#设备加入与移除) and [protocol 9 upgrade steps](self-hosting.md#upgrading-the-device-security-domain-protocol-9).

| Data | Visible to the relay |
|---|---|
| Messages, tool calls, terminal output, approval requests, images | No |
| Project names, paths, git remotes, session titles, machine names | No |
| Account and session keys | No |
| Device, machine and session IDs, timestamps, ciphertext sizes | Yes — needed for routing |
| Whether and when a session ended | Yes |
| Your devices' IP addresses | Yes — as to any server |

The web client is deliberately **not** served by the relay: a compromised relay could otherwise replace the page's JavaScript and defeat the encryption. Host it elsewhere, such as Cloudflare Pages. Report vulnerabilities privately through [GitHub security advisories](https://github.com/Lewin671/remote-agent/security/advisories/new), not in public issues.

## Downloads and verification

Each [release](https://github.com/Lewin671/remote-agent/releases/latest) ships every component at the same version:

| File | Contents | Platforms |
|---|---|---|
| `remote-agent-vX.Y.Z-<os>-<arch>.tar.gz` | Daemon and command line, plus `remote-agent-service` for running it in the background | macOS and Linux, arm64 and amd64 |
| `remote-agent-server-vX.Y.Z-linux-<arch>.tar.gz` | Relay, with a Docker Compose setup that includes HTTPS | Linux, arm64 and amd64 |
| `remote-agent-web-vX.Y.Z.tar.gz` | Web client as static files | Any HTTPS static host |
| `remote-agent-vX.Y.Z.apk` | Android app | Android 8.0 and later |
| `SHA256SUMS` | Checksums of all of the above | |

`install.sh` selects the right file, verifies it against `SHA256SUMS`, and installs without root. To inspect it first, download it, read it, and run it with `sh install.sh`. To verify a download by hand:

```sh
sha256sum --check --ignore-missing SHA256SUMS   # macOS: shasum -a 256 --check --ignore-missing SHA256SUMS
```

The Android app is always signed with the same key. Its certificate's SHA-256 fingerprint:

```
8f:ea:c3:68:de:87:29:d5:25:ca:65:f7:40:36:83:27:17:76:68:af:e3:de:06:2a:df:09:3e:df:89:3f:20:cc
```

## Updating

- **Android app:** updates itself from this repository's releases — **Settings › Version**.
- **Daemon:** `remote-agent update check`, `remote-agent update`, and `remote-agent update status`. Automatic updates are off by default; enable them per computer with `remote-agent update auto enable --time 04:00` (local time) and disable them with `remote-agent update auto disable`. The updater verifies size and SHA-256 over HTTPS, swaps the binary atomically, keeps tmux sessions running, and rolls back if the new daemon fails to start. Upgrades that change the protocol or stored data are not applied automatically; follow the release notes instead.
- **Relay and web client:** rerun the installer for the new version.

## Source tree

| Path | Contents |
|---|---|
| `cmd/remote-agent`, `cmd/remote-agent-server` | Entry points of the daemon/CLI and the relay (Go) |
| `internal/` | Go packages: daemon, relay server, encryption, Codex and Claude Code integration, updater |
| `proto/`, `gen/` | Protocol definitions (Protocol Buffers, Connect) and generated code |
| `web/` | Web client (TypeScript, React, Vite) |
| `android/`, `ios/` | Native clients (Kotlin with Jetpack Compose; Swift with SwiftUI) |
| `deploy/` | Container image and the self-hosting templates shipped with the relay |
| `scripts/` | Build, test, release and deployment tooling |
| `docs/` | Scope, architecture, measurements of external tools, plan and acceptance records |

To build and run from source, see the [development guide](development.md) (in Chinese).
