# 开发与本地使用

从源码构建、在本机运行和调试 remote-agent 的说明，面向开发者和编程 agent。项目介绍、安装和自部署见根目录的 [README](../README.zh-CN.md) 和[自部署指南](self-hosting.zh-CN.md)。

## 组成

| 组件 | 语言 | 说明 |
|---|---|---|
| `remote-agent` | Go | 每台电脑一个，daemon 和本地命令在同一个二进制里：tmux 托管会话、读取 transcript、hook 审批、输入注入 |
| `remote-agent-server` | Go | 部署在 VPS：鉴权、密文转发与存储、推送 |
| `web` | TypeScript（React） | PWA 客户端：聊天视图、终端视图、权限审批 |
| `android` | Kotlin（Jetpack Compose） | Android 原生客户端：原生终端，在通知里审批 |
| `ios` | Swift（SwiftUI） | iOS 原生客户端（开发中）：原生终端 |

> 当前进度和下一步见 [plan.md](plan.md)，项目边界见 [scope.md](scope.md)，全部文档见 [docs/README.md](README.md)，开发规范见 [AGENTS.md](../AGENTS.md)。

## 本地使用

需要 Go 1.26.11+、tmux 3.2+，以及已登录的 codex、claude 或 agy。

```sh
scripts/install.sh             # 编译到 ~/bin/remote-agent；装过后台服务的话顺便重启 daemon
scripts/daemon.sh install      # 把 daemon 装成后台服务（macOS 用 launchd，Linux 用 systemd --user），开机自启
remote-agent daemon            # 或者：在前台运行 daemon
remote-agent codex             # 另开一个终端：在当前目录启动 Codex 并 attach
remote-agent claude            # 同上，启动 Claude Code
remote-agent agy               # 同上，启动 Antigravity
remote-agent ls                # 列出会话
remote-agent send <id> 你好     # 从别处发消息，id 可以只写前几位
remote-agent events <id> -f    # 持续查看事件
```

- 从手机或网页新开的会话由你自己的登录 shell 启动（交互式登录，读 `.zshrc`、`.profile` 等），环境每次现算：升级 nvm、装新命令、CLI 换了位置都会在下一个会话生效，不用重新 `install` 或重启 daemon。从终端用 `remote-agent codex` 开的会话仍用那个终端的环境，里面临时 `export` 的变量有效，启动的也是在那个终端里敲 `codex` 会找到的那个（PATH 里靠前的包装脚本同样生效）；找不到就报错，不会改用 daemon 自己找到的那个
- CLI 需要的代理等环境变量写进 `~/.remote-agent/agent.env`，一行一个 `KEY=VALUE`，daemon 启动每个会话时都会带上；这里设置的值会覆盖 shell 配置算出来的结果
- 后台服务的日常操作：`scripts/daemon.sh restart|stop|start|status|log`，日志在 `~/.remote-agent/daemon.log`。重启 daemon 不影响正在 tmux 里运行的会话。服务用的是安装时 shell 的 `PATH`：它决定 daemon 自己找到哪个 codex/claude/agy（`doctor` 用它；版本检查在有会话启动后跟着会话实际跑的那个），tmux 或 CLI 换了位置就重新 `install` 一次
- 第一次在某个目录启动时，Codex 会问是否信任这个目录、是否信任 hook，需要在终端里回答
- 正式 Release 安装支持 `remote-agent update check`、`remote-agent update` 和 `remote-agent update status`。自动更新默认关闭；`remote-agent update auto enable --time 04:00` 开启本机每日检查，`remote-agent update auto disable` 关闭。需要先用 Release 安装器升级一次以获得这些命令；源码构建不自更新。限制和恢复方式见 [daemon 更新](architecture.md#daemon-更新)。
- **升级 Codex 后运行 `remote-agent doctor`**：它用一个临时会话（GPT-6-Luna，低档位）检查 remote-agent 依赖的各项行为，并记下这个版本已验证

## 连接 server 和网页

```sh
deploy/deploy.sh root@your-vps           # 部署 server，第一次会生成 setup token
scripts/ship.sh --web-only [vX.Y.Z]     # 仅更新 Cloudflare Pages；协议变化时拒绝，按 API 校验生产提交
scripts/ship.sh [vX.Y.Z]                # 我们自己的部署：按顺序上线 server、网页和本机 daemon
remote-agent account join --server https://ra.example.com --code 12345678   # 在新电脑上加入（管理端显示配对码）
cd web && pnpm install && pnpm run deploy                   # 部署网页到 Cloudflare Pages（先 npx wrangler login）
```

- 账户和配对可在网页、Android 或 iOS 客户端里操作：用户先建账户成为唯一管理端，电脑只执行 `account join`。本地端到端产品验收从 `ios/scripts/dev-env.sh start --no-account` 开始，用真实客户端建账户和批准配对；测试夹具或 `internal/remoteclient` 只用于自动回归，不能代替产品链路验收；`account init`、`account pair` 不再是用户入口
- server 前面需要有负责 HTTPS 的反向代理，部署方式见 [docs/architecture.md](architecture.md) 第 1 节
- 本地调试网页用 `cd web && pnpm dev --host 127.0.0.1 --port 0`，打开输出的本机地址；CSS 修改会热更新。加 `?demo` 可用示例数据检查界面。动画检查时让标签页保持前台，确认页面可见后再比较连续帧；后台标签页的静止截图不能证明动画失效。构建产物、CSP 和 Service Worker 则用 `pnpm build && pnpm preview` 验证（默认 http://localhost:4173）。示例界面不替代真实 CLI 的端到端验收。
- 会话内容由内容密钥解开；账户根密钥用于账户签名，不再用于解密旧历史，同样只留在自己的设备上
- daemon 发现 `~/.remote-agent/account.json` 后会自动连上 server，不用重启
- 网页能登录但一直「未连接」时，多半是浏览器的拦截扩展挡住了 WebSocket，见 [docs/browser-notes.md](browser-notes.md)

## 开发

- `go test ./...`：单元测试，不依赖真实的 Codex
- `scripts/acceptance.sh`：用真实的 Codex 跑 P0 验收
- `cd web && pnpm test`：网页的单元测试，包括和 Go 共用的加密测试向量；迭代时可先用 `pnpm test src/views/flows.test.tsx` 定位交互回归，局部运行不替代网页全套、AGENTS.md 要求的 Go 测试和发布把关。
- 改了 `proto/` 之后运行 `buf generate`（需要 `buf`、`protoc-gen-go`、`protoc-gen-connect-go`），再在 `web/` 里运行 `pnpm gen`；生成的代码在 `gen/` 和 `web/src/gen/`，一并提交
- Android 构建需要 JDK 17 和 Android SDK；Gradle 会按项目的 daemon JVM criteria 选择 JDK 17。找不到时先安装 JDK 17 再构建。
- Android 要复用编译缓存但完整重跑 JVM 测试，用 `cd android && ./gradlew --build-cache :core:test --rerun :terminal:testReleaseUnitTest --rerun :app:testDebugUnitTest --rerun :app:lintVitalRelease`。每个测试任务后的 `--rerun` 强制它执行，禁止复用该任务的增量结果或缓存；编译和 lint 仍可复用未变输入的结果，Go 集成夹具仍从当前 checkout 构建。Release 工作流也用这些任务选项，签名构建与公开扫描照常执行。

### 迭代与交付检查

四端统一入口是 `python3 scripts/dev/check.py <go|web|android|ios> <quick|full>`。每次创建 `/tmp` 下独立的 remote-agent 状态、tmux socket 名、Codex 和 Claude 配置目录；不继承个人会话和安装服务的 opt-in 测试开关。构建缓存保留，Go 的结果缓存和 Gradle 的测试结果缓存不用于全量验收。

| 端 | quick（迭代） | full（交付） |
|---|---|---|
| Go | CLI 解析、调度、协议、加密向量等纯逻辑包；不跑 daemon、server、relay、PTY 和更新器集成测试 | `go test -count=1 -parallel=8 -timeout=5m ./...` |
| 网页 | 类型检查和全部 Vitest projects | 同 quick，完整运行全部网页用例 |
| Android | core 的 Attempt、Prefs、EventProjection、Schedules 和共享向量测试；不跑 App UI | core、terminal、app 的全部 JVM 测试，逐任务 `--rerun`，加 Release lint |
| iOS | SwiftPM 的 Markdown、Attempt、PagedList、Prefs、Timeline、Project、Schedules 与向量测试；不启动模拟器 | `ios/scripts/check.sh`：文案生成、String Catalog 编译、全部 SwiftPM 测试、buf 核对、App 构建与全部单元测试；不含界面自动化 |

quick 的整个命令预算为 60 秒，full 为 30 分钟；预算耗尽返回 124，不能记为通过。60 秒目标以依赖和编译缓存已预热为前提，冷编译或共享机器繁忙时可能超时。quick 的通过不能代替 full。Release 工作流仍运行原有全套检查，不因迭代档缩减覆盖；真实 CLI 的隔离端到端验收另按 AGENTS.md 执行。

`ios/scripts/check.sh` 还可以带一个参数：`core` 只跑 SwiftPM 测试，`app` 只构建 App 并跑单元测试，`ui` 只跑界面自动化，`full` 是全部步骤加界面自动化。不带参数是交付检查：除界面自动化以外的全部步骤。界面自动化在模拟器里真实触摸，要几分钟，机器忙时会慢到超时，所以只在改了 `ios/RemoteAgent/UI` 下的视图或 `ios/RemoteAgentUITests` 时跑，以及提交 App Store 之前跑一次，并且同一台机器同一时间只让一个任务跑。每个用例有 180 秒的执行上限，超时算失败而不是一直等。没有按 Git 改动路径自动跳过步骤，避免漏掉共享资源、构建配置和目标之间的间接依赖。模拟器可通过 `IOS_DESTINATION` 指定，构建并行度默认 4。`IOS_SWIFT_BUILD_PATH` 和 `IOS_DERIVED_DATA_PATH` 可把 SwiftPM / Xcode 中间产物放到任务自己的临时目录。

## 任务开发工具

位于 `scripts/dev/`，供日常开发与 peer 任务快速拉起环境、排查故障及生成测试素材：

- `scripts/dev/preflight.sh`：开工前检查常见环境坑：`RA_DEV_DIR` 路径长度（规避 macOS AF_UNIX 104 字符超长）、残留会话变量（提示需 unset 的列表）、磁盘可用空间与中继 2 GiB 低水位、端口可用性与核心工具（tmux/go/pnpm/adb/xcrun）、Android 设备在线与屏幕唤醒状态（`--wake` 唤醒）、iOS 模拟器可用性。
- `scripts/dev/e2e-up.sh` / `scripts/dev/e2e-down.sh`：基于 `dev-env.sh` 一键启动与停止隔离的本地端到端环境。自动分配短路径与端口，拉起隔离 relay、初始化测试账户与 daemon、启动 Vite 网页代理服务，并打印网页地址与 CLI 示例；`e2e-down.sh` 一键停止服务、清理 tmux socket 与临时目录。
- `scripts/dev/fixtures.sh [目录]`（或 `scripts/dev/fixtures/generate.py`）：生成全套编号测试素材至指定目录（普通 PNG、长图、64MP 超大像素、>20MB 大图、GIF、WebP、损坏文件、HEIC、超长单行命令、多行 heredoc 文本），不提交二进制进仓库。
- `scripts/dev/frames.sh <视频> [输出目录] [时间点...]`：使用 ffmpeg 提取全片约 2fps 的缩略联系表（`contact/sheet.jpg` 及帧序列）和指定时间点前后的逐帧片段，输出固定目录结构与元数据。
- `scripts/dev/worktree.sh <任务名>`：按 AGENTS.md 约定创建新分支与 worktree，使用 pnpm 离线 store 快速连接 Web 依赖，并打印 Android/iOS 缓存复用规则与各端单测运行命令。

## iOS 卡顿诊断

实现和隐私边界见 [iOS 客户端设计](ios-client.md#本机卡顿诊断)。前台主线程超过两秒没有回复时才生成报告，正常运行时目录可能不存在。开发签名的 Release 构建与 Debug 使用同一个容器内相对路径；无需启动或重装 App 即可取回已有文件：

```sh
xcrun devicectl device copy from --device "$RA_TEST_DEVICE" \
  --domain-type appDataContainer \
  --domain-identifier io.github.lewin671.remoteagent \
  --source 'Library/Application Support/Diagnostics' \
  --destination ./chat-diagnostics
```

`RA_TEST_DEVICE` 填目标设备的标识；独立 Debug App 的 bundle identifier 改为 `io.github.lewin671.remoteagent.dev`。此命令需要可访问 App 数据容器的开发签名及配对环境，不承诺 App Store 安装包可这样读取。模拟器可用 `xcrun simctl get_app_container <simulator> <bundle-identifier> data` 找到数据容器后读取相同相对目录。

报告 `format = 1`。`path` 对应代码中的 `ChatDiagnosticPath.rawValue`；`a…d` 是对应 `record` 调用的数值，`nonFinite` 的四个位标记原输入非有限值。`entity` 是进程内小整数，`sequence` 是诊断序号，均不是服务器事件或会话标识。`layoutBegin.a` 指明测量种类，`layout` 配对 begin/end；`mainThread` 区分主线程与后台测量，`mainLayout` 是快照时尚未结束的主线程 scope。时间单位是纳秒，相对于该进程内缓冲的创建；`createdAtUnixMilliseconds` 仅用于文件时间。`startedAt` 是未回复 ping 的投递时间，`recoveredAt` 和 `duration` 在恢复后补入，不代表已经定位卡顿起点或根因。

聊天 collection 的 `.sight` 是合并后的 UIKit viewport 样本（offset、contentSize.height、bounds.height）；`.content` 是内容高度和 `height - offset`，`.floor` 是 viewport 的底边高度，两者都用 collection 内坐标，不是窗口坐标。`.row` 是可见 cell 相对 viewport 的 y、高、宽和列表实体，离开可见区记 `.rowHidden`。`.listLayout` scope 每次 `UICollectionView.layoutSubviews` 都记录，因此即使观测合并，也能从逐路径计数看见布局风暴。`.keep` / `.keepTarget` / `.positionY` / `.kept` 表示 snapshot 前插或 cell 重排时的单次 offset 修正，`.positionID` 表示 UIKit 按行定位；`proxy…` 这些已有路径名保留其操作目的，定位已由 UIKit 执行。主线程 watchdog 和报告格式不变。

模拟器聊天回归：`ChatScrollStressTests.tallRowsAtViewportBoundary` 用 609 pt viewport 和长短高度相差超过 1,000 pt 的正文／代码／表格混排行，测试附件保存数值环缓冲与开始／结束计数。`ChatCollectionTests` 断言跟随、前插、fresh、cell 自适应和宽屏布局；`RemoteAgentUITests/ChatGestureTests` 用 `-demo -demoRoute session/sample-boundary` 的合成数据执行原生拖动／快速滑动、回到底部和输入区收键盘。它们不连接中继或真实 CLI，不替代端到端验收。


## iOS App Store 自动发布

`iOS App Store` 工作流在 `Release` 成功后运行，从已公开的最新正式 tag 构建 iOS，使用 `version.properties` 的产品版本与 `androidVersionCode` 作为构建号。先串行运行 Swift package 测试，再归档签名的 Release 并上传、等待 Apple 处理、提交审核，审核通过后自动上架。Apple 审核不由 CI 控制；前一个版本还在 Apple 手里（等待审核、审核中、等待上架）时，这一版不提交，工作流正常结束并写明原因，不算失败：不撤回审核，等它结束后手动重跑，或由下一次发布带上这些变化。前一个版本待手动发布或状态异常时仍明确失败；不覆盖较新版本。解决 Apple 侧问题后，在 Actions 手动运行，填写最新正式 tag 并选中 `publish`；同一版本已上传的构建会复用，已自动送审的版本直接跳过。并发发布串行执行，GitHub 只保留一个等待运行的任务；被新版本取代的旧版本跳过。

初次配置：

- 创建专用于发布的 App Store Connect 团队 API Key，角色为 App Manager。将标准 fastlane JSON 保存为仓库 Secret `ASC_API_KEY`：`key_id`、`issuer_id`、`key`（完整 p8 内容）。不保存 Apple 登录密码或验证码。
- 创建专用 Apple Distribution 证书及对应 App Store 分发描述文件。Secret `IOS_SIGNING` 是 JSON：`certificate`（p12 的 Base64）、`password`（p12 密码）、`profile`（mobileprovision 的 Base64）、`team_id`。构建时写进临时 Keychain，结束时清理。证书或描述文件到期前更新此 Secret。用 OpenSSL 导出 p12 时，使用 `-keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-3DES -macalg sha1`，并先在临时 macOS 钥匙串验证可导入且存在 Apple Distribution 身份；默认导出格式可能报 MAC verification failed。
- `RA_PUBLIC_DENY` 沿用 GitHub Release 的公开内容检查。流水线只上传 tag 的更新说明，保留 App Store 现有截图、商店资料、审核联系方式和分发地区。加密声明沿用已经审核的实现：仅使用 Apple 系统 CryptoKit，不含非豁免加密；若实现变化，先核对声明再发布。
- 手动运行工作流且不勾选 `publish`，完成测试及无签名 Release 归档演练；实际发布验证成功后，将仓库变量 `IOS_APP_STORE_ENABLED` 设为 `true`，启用后续自动发布。

本地演练需要 Ruby 3.3、Bundler 4 和 Xcode 27。在 `ios/` 执行 `bundle install`，再执行 `IOS_OUTPUT_DIR=/tmp/ios-release-check bundle exec fastlane ios verify`。演练额外执行无签名归档；正式发布只执行后续的分发签名归档。需单独验证签名时，传入 `IOS_SOURCE_DIR`（tag 的干净 checkout）、`IOS_OUTPUT_DIR` 和 `IOS_SIGNING` 后运行 `bundle exec fastlane ios package`，只生成 IPA。签名配置仅临时用于 App 的 Release 目标，构建结束后原样恢复工程，并再次核对 checkout 干净。自动发布工具的 Ruby 依赖由 `ios/Gemfile.lock` 固定；工具从 main 读取，产品源码来自不可移动的正式 tag，旧 tag 无需包含这些工具。发布工具本身的修改不升产品版本。

## 文案生成与检查

```sh
python3 scripts/i18n.py generate
python3 scripts/i18n.py generate --check
python3 scripts/i18n.py lint
python3 -m unittest discover -s scripts -p 'i18n_test.py'
```

新增 key 时，在对应 domain JSON 加中文原句、英文初稿、译者说明、参数类型/例值、适用端和逐语言审校状态，遵循 `i18n/messages.schema.json`；运行 generate 后一起提交源和全部产物。网页调用 `t("domain.key", { name: value })`，无参数时省略第二参；参与语言切换的独立 React 树使用 `useLocale()` 订阅。Android core 用生成的 `UiMessages.domainKey(...)` 表达消息，Compose 显示边界用 `uiText(...)`，后台显示用 `resolveUiMessage(context, message, locale)`。iOS 用 `L10n.domainKey(name: value)` 显式得到 String，再交给自定义组件；测试可传 locale。Go 用生成的 `i18n.DomainKey(locale, ...)`。不要手改 generated 文件。

新增 key 必须实际使用，lint 拒绝孤儿/未定义/不适用端调用。新增写死中英文命中时优先抽取；非产品内容在 `i18n/lint-allowlist.json` 精确写 path、literal 和 reason。基线是阶段 0 抽取前 HEAD 的历史债务，以路径、字串摘要和出现次数计数，移动行不新增债务、复制旧字串会失败；`lint --update-baseline` 只用于明确批准的债务重建，日常新增 key 不运行它。扫描是保守词法检查，覆盖 CJK 字串、JSX/HTML 文本以及已知原生 UI/错误/命令行输出入口的英文；不是完整语法解析器，动态拼接、间接英文变量和新 UI API 仍需代码审查并扩展扫描规则。

语言入口仍关闭，不能用系统英文作为全端英文完成的验收。三端试点可显式 locale 做单测快照；Android 设置 `RA_I18N_SCREENSHOT_DIR` 后运行 `:app:testDebugUnitTest --tests '*MessageLocaleTest'`，iOS `MessageLocaleSnapshotTests` 在测试宿主 Documents 写入 i18n-<locale>.png。截图不代表配对、通知或真实 CLI 端到端验收。


## 安全状态 v3 到 v4 的一次性手动升级

这是传输 0a 第 4–5 步的手动工具，不编入产品二进制，也不会由 daemon 调用。第 6 步的 wire 升级另行发布。本步骤保留配对，不要求删除账户或会话。

1. 停止该状态目录的 daemon，核对在途操作。旧接受记录的截止时间必须全部已过（最长为请求准入后 10 分钟）；工具发现未到期记录会直接退出，不自行等待。停止期间不要运行配对、账户变更或其他写该目录的命令。
2. 在本版源码根目录运行 `CGO_ENABLED=0 go run ./scripts/migrate-security-v4 --home /path/to/state --dry-run --locale zh-CN`。路径必须明确指定；工具取得同一个 daemon 排他锁，验证 SQLite 完整性和身份安全链，演练不发布备份或新库。
3. 同一命令去掉 `--dry-run`。工具先制作一致的 SQLite 备份（含已提交 WAL），在私有副本内升级，逐表核对身份、成员链、内容密钥和租约，再验证 v4 endpoint。成功发布 `security-v3.db.backup` 与 `security-v4.db`；已有目标或备份时拒绝覆盖。原 `security-v3.db`、账户文件和会话目录不修改。
4. 启动新版 daemon，用原客户端验证连接和原会话操作。v3 接受记录没有原载荷 hash，全部丢弃；迁移后的旧 ID 查询返回未知，不恢复旧正文，也不重新执行旧授权。请结合会话内容核对旧操作，不能自动换 ID 重做。

工具失败不会替换原库。若已发布备份而新库发布失败，先保留或移走该备份，再修正原因重试。需要回退且**尚未用 v4 接受新操作**时，保持 daemon 停止，移走 `security-v4.db` 及其 `-wal`、`-shm` 文件，再用旧二进制和原 v3 库启动。若 v4 已接受操作，旧 v3 库不包含它们的防重复记录；必须先核对这些操作，并让全部旧授权期限结束，不能直接降级继续重试。备份可以在停机状态下恢复 v3，但同样不能恢复 v4 执行期间新增的结果。

工具不输出账户、密钥、会话内容或 SQL 错误原文。`--locale en` 提供英文操作提示；`--dry-run` 也会明确提示旧请求结果不可恢复。
