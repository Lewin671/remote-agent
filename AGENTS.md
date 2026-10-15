# remote-agent

在任何设备上远程操作跑在自己电脑 tmux 里的 AI 编程 CLI（Codex 和 Claude Code）。组成部分：Go 写的本地 daemon、Go 写的中继 server，以及 PWA 客户端、Android 原生客户端和开发中的 iOS 原生客户端。项目范围见 [docs/scope.md](docs/scope.md)，技术决策见 [docs/architecture.md](docs/architecture.md)，当前进度和下一步见 [docs/plan.md](docs/plan.md)。

有两件事是这个项目存在的理由，改动时请保住它们：

- **隐私**：server 只转发和存储密文，内容和密钥不应该出现在 server 上，也不要写进日志
- **原生 CLI**：agent 以原生交互形态跑在 tmux 里，电脑和手机操作的是同一个会话，而不是通过 SDK 驱动

其余的按常识和 Go 的惯例来就好，遇到拿不准的取舍，提出来讨论。

## 不写兼容逻辑

项目还在早期。代码只支持当前协议版本：

- 两端能不能互通只看协议版本，不看产品版本。只改某一端自己的逻辑（界面、交互、本地状态）、不改变两端之间约定的改动，不提升协议版本，没升级的旧客户端继续可用
- 不为旧版本的 daemon、server 或客户端写降级、推断分支，不保留废弃字段。字段或行为变了就直接改，旧的一并删掉
- 改动会被旧版本的另一端误解时，提升协议版本号：Go 的 `internal/protocol`、网页的 `web/src/protocol.ts`、Android 的 `core/Version.kt`、iOS 的 `RACore/Version.swift` 四处同值（iOS 的测试核对这四处一致）。版本不一致的两端互相拒绝并提示更新，机制见 [docs/architecture.md](docs/architecture.md) 第 4 节的「版本」
- 不写数据迁移代码。存储格式要变时，一次性手动迁移或清掉重建，并在发布说明里写明升级要求
- 这些不算兼容逻辑，照常保留：适配外部工具的不同版本（Codex、Claude Code、浏览器、Android 和 iOS 系统），以及当前版本自己也会出现的缺失值
- 有其他人开始使用或到 1.0 时，重新讨论这条规则。v0.32.0（2026-10-10）起产物公开发布，别人可以自己部署，这个条件已经出现；用户还没有重新决定，在那之前规则照旧，但任何要求一起升级或重建数据的改动，都必须在发布说明里写清楚

改动或新建 `docs/` 下的文档前，先读 [docs/README.md](docs/README.md)，里面规定了每类内容写在哪。

## 开发流程

所有改动，不论大小，默认直接进 `main`，不提 PR，也不走长期分支；任务完成后按下文自动发布：

- 每次提交只做一件事，出问题时用 `git revert` 撤回
- 推送前 `go test ./...` 必须通过；先 `git fetch`，远端有新提交就 rebase 到最新的 `main` 上，禁止 force push
- 里程碑（如 P0、P1 完成）可以继续打里程碑 tag；产品发布用 `vMAJOR.MINOR.PATCH`

### 本地端到端测试

- 端到端测试默认从当前 checkout 构建并启动本地 server 和 daemon，使用临时账户、独立数据库、状态目录和 tmux socket；只有用户要求验证线上环境时才连接线上，不使用日常账户或现有会话做测试
- 现有入口是 [ios/scripts/dev-env.sh](ios/scripts/dev-env.sh)：`start --daemon` 启动隔离环境并输出本地地址；`launch [udid]` 让已安装的 iOS Debug App 加入；`cli <命令>` 操作测试 daemon；`status` 查看状态；`stop` 停止并清理。模拟器可访问它的 `127.0.0.1` 地址
- 每个测试任务设置独立的 `RA_DEV_DIR`，后续命令沿用同一目录，避免和其他任务共用默认环境；端口使用脚本自动分配的值，不写死
- 验收要确认客户端发出的操作经过 server、daemon 和真实 CLI，再回到客户端；示例数据和单元测试不能代替这条链路。结果注明测试环境、直接界面操作与调试触发的区别，以及未覆盖的流程
- 测试结束清理临时环境；用户需要继续查看时保留，并说明地址和停止方法

### 自动版本与发布

用户已授权 agent 在合适的开发节点自动更新版本并发布，不需要逐次确认，不使用定时任务。

- 一个用户任务的完整功能或一批相关修复完成、本地验收通过后，自动发布一次；不要给任务中间的小提交逐一发版。只有文档、界面稿或测试变化时不发产品版本（`web/src` 下的测试除外：网页构建会对它们做类型检查，写错一样会让发布失败）；只改发布和把关工具（`release.py`、`public_scan.py`、`publish-public.sh`、`publish_public.py`、流水线文件）或公开仓库页面时也不发，它们不进产物。产物由什么组成以 `scripts/release.py` 的 `PRODUCT_PATHS` 和 `PRODUCT_FILES` 为准，新增会被打进包里或决定构建方式的文件时把它加进去
- 新功能升 minor，修复升 patch；1.0 以前的不兼容变化升 minor，1.0 起不兼容变化升 major。只有需要实际发布时才改 `version.properties`；各端共用产品版本，协议版本独立
- 在独立 worktree 中提交任务代码并完成验收后，自动执行 `scripts/release-task.sh patch|minor|major "变化与升级要求"`。它会 fetch/rebase、检查是否有产品变化、递增共享版本与 Android code、提交版本文件、跑检查、创建 tag 并原子推送。没有产品变化会跳过；需要单步处理时用 `python3 scripts/release.py prepare patch|minor|major`
- 版本提交 rebase 到最新 main 后重新跑 `go test ./...` 和改动涉及的检查。若别人已发布使版本不再适用，重新计算版本；冲突仍按下文规则停下报告
- 验证通过后，在版本提交上创建 annotated tag，并以 `git push --atomic origin HEAD:main refs/tags/vX.Y.Z` 一次推送提交和 tag，禁止 force push、移动或复用已推送的版本 tag
- GitHub 的 Release 工作流在版本提交推到 `main`（`version.properties` 变化）时触发，从 tag 所在的提交构建所有端，Go/网页/Android 检查全部通过才发布：正式签名 APK、各平台的 daemon 和 server、网页包，以及 `SHA256SUMS`。最新一版随后发布成公开仓库 `Lewin671/remote-agent-releases` 的 Release，App 从那里更新，别人从那里部署自己的一套（[docs/architecture.md](docs/architecture.md) 的「产品版本与发布」）；源码不公开。失败不算发布完成：查看 Actions 错误并修复；代码修复发下一版，同一 tag 的暂时失败可手动运行工作流并填入该 tag 重跑；私有 Release 已公开、只重试公开发布时选 `publish_only=true`，直接复用已发布附件，不重建。不填 tag 手动运行是演练：跑全部检查和打包，但不发布
- **公开的东西不带我们自己的部署**：公开仓库的页面在 `deploy/releases-repo/`，产物由 `scripts/build-dist.sh` 构建；域名、主机名、邮箱、设备号、密钥和本机路径都不能出现在里面。`scripts/public_scan.py` 在打包、公开上传和同步页面时各查一遍，查到就不发布：按名单查内容，按文件名拒绝部署的配置和状态（`.env`、数据库、密钥等），打包时还要求一版的文件和每个压缩包的成员正好是预期的那些，所以往包里加文件要同时改它的 `release()`；新增一种不能公开的标识时把它加进这个脚本的名单。公开上传用 Secret `RELEASES_TOKEN`，先草稿、校验全部附件后才公开；已公开的 Release 不覆盖，重试只能核对一致的文件。公开的示例一律用 `example.com`
- **改公开仓库的页面**：只改 `deploy/releases-repo/` 里的源文件，随任务提交到这里。推到 `main` 后 `Public pages` 工作流自动把这个目录同步成公开仓库的 `main`（`scripts/sync-public-pages.sh`：先扫描，用 GitHub 的 noreply 地址提交，普通推送），不用发版，也不要手动推；不在 GitHub 网页上直接改，那样绕过扫描，下次同步还会被覆盖。推送后确认这个工作流成功。改了那边的安装脚本，先在这里的演练里跑通（Release 工作流不填 tag 手动运行）
- **在公开仓库说话也是公开的**：回 issue、写评论或 Release 说明时，不贴 daemon 或 server 的日志原文、截图、域名、机器 ID、账户密钥、setup token 和配对链接；需要举例就用 `example.com` 和编造的 ID
- **发布说明是公开的**：`release-task.sh` 的第二个参数成为 tag 的说明，原样进公开的清单和公开仓库的 Release，显示在 App 的更新弹层里。只写用户看得懂的变化和升级要求，不写服务器地址、部署细节、密钥或内部讨论
- 发布产物不代表部署线上服务，不自动升级本机 daemon，也不自动安装 APK。已有部署授权按原任务执行
- 手动 APK 工作流只生成独立 `.dev` 应用，推送关闭，短期保留，不更新正式版本、不创建 Release

### 多 agent 并行

可能有多个 agent 同时开发，每个 agent 在自己的 `git worktree` 里干活，不在主工作区里改代码。这样暂存区、`go test` 和 rebase 都互不干扰。

```sh
# 开工：从最新的 main 建 worktree，放在所在工作区的 worktrees/remote-agent/ 下（主工作区在 projects/remote-agent/）
git fetch
git worktree add -b wt/<任务名> ../../worktrees/remote-agent/<任务名> origin/main

# 做完：在 worktree 里提交后，rebase 到最新的 main，测试通过再推
git fetch && git rebase origin/main
go test ./...
git push origin HEAD:main

# 收尾：确认已推送后清理，并把主工作区快进到最新
git worktree remove ../../worktrees/remote-agent/<任务名>
git branch -D wt/<任务名>
git -C <主工作区> merge --ff-only origin/main
```

- 本地验收和测试都通过，就可以直接提交并推送，不用等用户确认
- `wt/<任务名>` 只是临时分支，不推到远端，推送目标始终是 `main`
- rebase 出现冲突时先停下来告诉用户，不要自行合并；rebase 后测试不过（别人的改动和你的不兼容）也一样
- 主工作区留给用户，agent 不在里面改动或提交；唯一例外是收尾时的快进，它只会前移到远端已有的提交，不动未提交的改动。快进失败就跳过，告诉用户原因
- 新 agent 通常从主工作区启动、读那里的 AGENTS.md，所以每次推送后都要快进，免得后来的 agent 读到旧规则
