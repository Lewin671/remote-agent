// Sample data for looking at screens without a server (`-demo`), debug
// builds only. Three sets, because a screen that looks right with the
// mockup's tidy examples has only been checked against one of the cases it
// will meet:
//
//   everyday  the mockup's own examples, the data of Android's V7Gallery
//             and V8Gallery (androidTest): three computers, a handful of
//             sessions in every state, one of each thing that waits;
//   crowded   long titles and names, many projects, sessions, computers
//             and clients, states that overlap, a computer offline with
//             sessions on it, quotas nearly used up;
//   empty     an account that has just been made: this client and nothing else.
//
// The Remote that shows them is never started (`Remote.showForTest`):
// nothing is fetched, and commands fail as they would offline.
#if DEBUG
import Foundation
import RACore
import RAProto
import SwiftProtobuf

enum DemoSet: String, CaseIterable, Sendable {
    case everyday, crowded, empty
}

enum Demo {
    static let me = "2222222222222222"
    /// An account that opens nothing: the keys are zeros and the server is a closed port on this device.
    static let account = Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: me)

    static let home = "/Users/me"
    static let foo = home + "/Code/foo"

    @MainActor static func remote() -> Remote {
        // The zero keys are valid keys; nothing else can fail here.
        try! Remote(account: account, http: AppModel.http)
    }

    static func state(_ set: DemoSet, now: Int64 = nowMs()) -> RemoteState {
        switch set {
        case .everyday: everyday(now)
        case .crowded: crowded(now)
        case .empty: empty(now)
        }
    }

    static func events(_ set: DemoSet, now: Int64 = nowMs()) -> [String: [PBEvent]] {
        switch set {
        case .everyday: everydayEvents(now)
        case .crowded: crowdedEvents(now)
        case .empty: [:]
        }
    }

    /// What each computer lists for each CLI: Codex's models and Claude
    /// Code's, with efforts and context windows, one of them with a long
    /// slug and no display name. For every computer of every set.
    static let models: [(machineId: String, agent: PBAgent, models: PBListModelsResponse)] = {
        func model(_ slug: String, _ name: String, efforts: [String] = [], window: Int64 = 0) -> PBModelInfo {
            .with {
                $0.slug = slug
                $0.displayName = name
                $0.efforts = efforts
                $0.defaultEffort = efforts.isEmpty ? "" : "medium"
                $0.contextWindow = window
            }
        }
        let efforts = ["low", "medium", "high", "xhigh", "max", "ultra"]
        let codex = PBListModelsResponse.with {
            $0.defaultModel = "gpt-6.1-sol"
            $0.models = [
                model("gpt-6.1-sol", "GPT-6.1-Sol", efforts: efforts), model("gpt-6-astra", "GPT-6-Astra", efforts: efforts),
                model("gpt-6-sol", "GPT-6-Sol", efforts: efforts), model("gpt-6-luna", "GPT-6-Luna", efforts: efforts),
                model("gpt-5.6-sol", "GPT-5.6-Sol", efforts: efforts), model("gpt-5.6-terra", "GPT-5.6-Terra", efforts: efforts),
                model("gpt-5.6-luna", "GPT-5.6-Luna", efforts: efforts), model("gpt-5.5", "GPT-5.5", efforts: efforts),
                model("a-model-with-a-very-long-slug-and-no-display-name-2026-10-09-preview", ""),
            ]
        }
        let claude = PBListModelsResponse.with {
            $0.defaultModel = "sonnet"
            $0.models = [
                model("opus", "Opus", efforts: ["low", "medium", "high"], window: 1_000_000),
                model("sonnet", "Sonnet", efforts: ["low", "medium", "high"], window: 1_000_000),
                model("haiku", "Haiku", window: 200_000),
            ]
        }
        let machines = ["mac", "vps", "imac", "studio", "ci", "nas"]
        return machines.map { (machineId: $0, agent: PBAgent.codex, models: codex) }
            + machines.map { (machineId: $0, agent: PBAgent.claude, models: claude) }
    }()

    /// A made-up home folder for 新项目 · 选目录: what each folder holds, by path.
    static let folders: [String: [String]] = [
        home: ["Code", "Documents", "Downloads", "work"],
        home + "/Code": [
            "ai-bot", "a-very-long-project-name-that-keeps-going-and-going", "bar", "billing", "blog", "demo", "design-system", "foo",
            "infra", "mobile", "remote-agent", "shop", "数据平台",
        ],
        home + "/Code/ai-bot": ["remote-agent", "happy", "notes-sync"],
        home + "/work": ["blog"],
    ]

    /// A folder of the made-up home as a computer would list it; an empty path is the home folder.
    static func directory(_ path: String) -> PBListDirectoriesResponse {
        let at = path.isEmpty ? home : path
        let above = at.split(separator: "/").dropLast().map { "/" + $0 }.joined()
        return .with {
            $0.path = at
            $0.home = home
            $0.parent = at == "/" ? "" : above.isEmpty ? "/" : above
            $0.dirs = folders[at] ?? []
        }
    }

    /// The made-up home, listed after a moment as a computer would; `-dirError` makes the listing fail.
    @MainActor static let directories = DirectorySource { _, path in
        try await Task.sleep(for: .milliseconds(120))
        if HomeLaunch.flag("-dirError") { throw CommandError("not_found", "目录不存在") }
        return directory(path)
    }

    // MARK: Builders

    private static func ts(_ ms: Int64) -> Google_Protobuf_Timestamp { .with { $0.seconds = ms / 1000 } }

    /// Today at a wall-clock time.
    private static func at(_ h: Int, _ m: Int, now: Int64) -> Int64 {
        let day = Calendar.current.startOfDay(for: Date(timeIntervalSince1970: Double(now) / 1000))
        return Int64(day.timeIntervalSince1970 * 1000) + Int64(h * 3600 + m * 60) * 1000
    }

    private static func machine(
        _ id: String, _ name: String, now: Int64, online: Bool = true, claude: Bool = false, quotas: [PBAgentQuota] = []
    ) -> MachineView {
        MachineView(
            id: id, online: online, lastSeenMs: online ? now : now - 2 * 3_600_000,
            meta: .with { meta in
                meta.hostname = name
                meta.home = home
                meta.os = "darwin"
                meta.protocolVersion = protocolVersion
                meta.capabilities = .with { $0.approvalAlways = true }
                meta.agents = (claude ? [PBAgent.codex, .claude] : [.codex]).map { agent in
                    .with {
                        $0.agent = agent
                        $0.defaultMode = .ask
                        $0.defaultModeSource = .cliConfig
                    }
                }
                meta.quotas = quotas
            },
            metaRev: 1)
    }

    private static func session(
        _ id: String, _ machine: String, _ cwd: String, _ title: String, now: Int64, status: PBSessionStatus = .idle,
        agent: PBAgent = .codex, approvals: Int32 = 0, questions: Int32 = 0, plan: Bool = false, preview: String = "",
        updated: Int64? = nil, created: Int64? = nil, mode: PBApprovalMode = .ask, detail: String = "", context: Int64 = 0,
        queued: Int32 = 0
    ) -> SessionView {
        let updated = updated ?? now
        let info = PBSession.with {
            $0.id = id
            $0.cwd = cwd
            $0.title = title
            $0.status = status
            $0.agent = agent
            $0.pendingApprovals = approvals
            $0.pendingQuestions = questions
            $0.planOpen = plan
            $0.preview = preview
            $0.createdAt = ts(created ?? now - 3_600_000)
            $0.mode = mode
            $0.statusDetail = detail
            $0.model = agent == .claude ? "sonnet" : "gpt-6-astra"
            $0.effort = agent == .claude ? "high" : "medium"
            $0.contextTokens = context
            $0.contextWindow = context > 0 ? 760_000 : 0
            $0.queuedInputs = queued
            if approvals > 0 || questions > 0 || plan || status == .blocked { $0.attentionSince = ts(updated) }
            if status == .exited { $0.endedAt = ts(updated) }
        }
        return SessionView(
            id: id, machineId: machine, info: info, metaRev: 1, updatedMs: updated,
            listedEndedMs: status == .exited ? updated : 0)
    }

    private static func prefs(pinned: [String], hidden: [String] = []) -> PrefsObject {
        PrefsObject(revision: 1, prefs: .with { p in
            p.schema = 1
            p.pinned = pinned.map { path in .with { $0.path = path } }
            p.hidden = hidden.map { path in .with { $0.path = path } }
        })
    }

    private static func client(_ id: String, now: Int64, name: String = "", platform: String = "", description: String = "", ageMs: Int64, seenMs: Int64 = 0) -> DeviceView {
        DeviceView(
            id: id, kind: .client, createdMs: now - ageMs,
            meta: .with {
                $0.name = name
                $0.platform = platform
                $0.description_p = description
            },
            metaRev: 1, lastSeenMs: seenMs == 0 ? 0 : now - seenMs)
    }

    private static func keyed<V: Identifiable>(_ list: [V]) -> [String: V] where V.ID == String {
        Dictionary(list.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
    }

    // Events, numbered in the order given, a second apart, ending now.
    private static func events(_ now: Int64, _ kinds: [PBEvent.OneOf_Kind]) -> [PBEvent] {
        kinds.enumerated().map { i, kind in
            .with {
                $0.seq = UInt64(i + 1)
                $0.time = ts(now - Int64(kinds.count - i) * 1000)
                $0.kind = kind
            }
        }
    }

    private static let turn = PBEvent.OneOf_Kind.turnStarted(PBTurnStarted())
    private static func user(_ text: String) -> PBEvent.OneOf_Kind { .userMessage(.with { $0.text = text }) }
    private static func agent(_ text: String) -> PBEvent.OneOf_Kind { .agentMessage(.with { $0.text = text }) }
    private static func thought(_ text: String) -> PBEvent.OneOf_Kind { .reasoning(.with { $0.text = text }) }
    private static func turnEnd(_ ms: Int64) -> PBEvent.OneOf_Kind { .turnEnded(.with { $0.durationMs = ms }) }

    private static func call(
        _ title: String, kind: PBToolKind = .command, output: String = "", exit: Int32? = nil, item: String = "Bash"
    ) -> PBEvent.OneOf_Kind {
        .toolCall(.with {
            $0.title = title
            $0.callID = title
            $0.kind = kind
            $0.status = "completed"
            $0.itemType = item
            $0.output = output
            if let exit { $0.exitCode = exit }
        })
    }

    private static func started(_ title: String) -> PBEvent.OneOf_Kind {
        .toolStarted(.with {
            $0.title = title
            $0.callID = title
            $0.kind = .command
            $0.itemType = "Bash"
            $0.input = title
        })
    }

    private static func approval(_ id: String, _ command: String, _ description: String, now: Int64) -> PBEvent.OneOf_Kind {
        .approvalRequested(.with {
            $0.approvalID = id
            $0.command = command
            $0.description_p = description
            $0.toolName = "Bash"
            $0.deadline = ts(now + 8 * 60_000 + 30_000)
        })
    }

    private static func resolved(_ id: String) -> PBEvent.OneOf_Kind {
        .approvalResolved(.with {
            $0.approvalID = id
            $0.decision = .allow
        })
    }

    private static func question(_ id: String, _ questions: [PBQuestion]) -> PBEvent.OneOf_Kind {
        .questionAsked(.with {
            $0.questionID = id
            $0.questions = questions
        })
    }

    private static func ask(_ text: String, multi: Bool = false, _ options: [(String, String)]) -> PBQuestion {
        .with { q in
            q.text = text
            q.multiSelect = multi
            q.options = options.map { label, description in
                .with {
                    $0.label = label
                    $0.description_p = description
                }
            }
        }
    }

    // MARK: Everyday

    private static func quotaMac(_ now: Int64) -> MachineView {
        machine("mac", "MacBook", now: now, claude: true, quotas: [
            .with {
                $0.agent = .codex
                $0.state = .ok
                $0.plan = "Pro Lite"
                $0.observedMs = now - 60_000
                $0.windows = [
                    .with {
                        $0.name = "primary"
                        $0.windowMinutes = 300
                        $0.usedPercent = 62
                        $0.resetsAtMs = now + 3 * 3_600_000
                    },
                    .with {
                        $0.name = "secondary"
                        $0.windowMinutes = 10080
                        $0.usedPercent = 34
                        $0.resetsAtMs = now + 3 * 86_400_000
                    },
                ]
            },
            .with {
                $0.agent = .claude
                $0.state = .ok
                $0.observedMs = now - 120_000
                $0.windows = [
                    .with {
                        $0.name = "five_hour"
                        $0.usedPercent = 92
                        $0.resetsAtMs = now + 2 * 3_600_000
                    },
                    .with { $0.name = "seven_day" },
                ]
            },
        ])
    }

    private static func everyday(_ now: Int64) -> RemoteState {
        var s = RemoteState()
        s.machines = keyed([quotaMac(now), machine("vps", "VPS-dev", now: now), machine("imac", "iMac", now: now, online: false)])
        s.sessions = keyed([
            session("run", "mac", foo, "修复登录超时", now: now, status: .running, preview: "正在执行 go test ./auth/...", updated: now - 20_000, created: now - 60_000, context: 87_000),
            session("doc", "mac", foo, "补充接口文档", now: now, agent: .claude, preview: "已补充 12 个接口的说明，要生成 OpenAPI 吗？", updated: at(8, 12, now: now), created: at(8, 12, now: now)),
            session("bill", "imac", home + "/Code/billing", "", now: now, updated: at(11, 25, now: now), created: at(11, 25, now: now)),
            session("blog1", "mac", home + "/Code/blog", "发布秋季文章", now: now, status: .exited, updated: now - 2 * 3_600_000),
            session("blog2", "vps", home + "/work/blog", "修复 RSS", now: now, status: .exited, updated: now - 26 * 3_600_000),
            session("ended", "mac", home + "/Code/remote-agent", "介绍下这个项目", now: now, status: .exited, preview: "没有按时间失效。模型列表缓存在前端页面的内存里，同一台机器每次页面加载只请求一次。", updated: now - 3 * 86_400_000),
            session("api", "vps", foo, "重构 API 层", now: now, approvals: 1, preview: "请求执行 go test ./... -count=1", updated: at(9, 31, now: now)),
            session("dep", "imac", home + "/Code/bar", "升级依赖", now: now, questions: 1, updated: at(9, 20, now: now)),
            session("plan", "mac", home + "/Code/shop", "拆分订单服务", now: now, plan: true, updated: at(9, 10, now: now), mode: .plan),
            session("trust", "vps", home + "/Code/billing", "对账脚本", now: now, status: .blocked, updated: at(9, 0, now: now), detail: "Trust this folder?"),
        ])
        s.devices = keyed([
            client(me, now: now, platform: "ios", description: "iOS 26.0 · iPhone", ageMs: 86_400_000),
            client("b71c00000000b71c", now: now, name: "我的手机", platform: "android", description: "Android 16 · Pixel 9", ageMs: 3_600_000, seenMs: 40 * 60_000),
            client("a8f200000000a8f2", now: now, platform: "web", description: "Chrome · macOS", ageMs: 9 * 86_400_000, seenMs: 2 * 86_400_000),
        ])
        s.prefs = prefs(pinned: [foo])
        s.conn = .online
        s.link = .online
        s.loaded = true
        s.synced = true
        return s
    }

    private static func everydayEvents(_ now: Int64) -> [String: [PBEvent]] {
        [
            "run": events(now, [
                turn, user("登录接口偶尔超时，帮我查一下原因并修掉"), thought("先看登录的连接处理。"),
                call("rg -n Login auth"), call("sed -n 40,80p auth/login.go"), call("git log -3 auth"),
                agent("问题在 `auth/login.go:58`：\n\n- 每次登录都新建 Redis 连接\n- 连接没设超时，池子耗尽后请求挂住\n\n我改成复用连接池并加上 `2s` 超时。"),
                call("auth/login.go", kind: .fileChange), started("go test ./auth/..."),
            ]),
            "api": events(now, [
                turn, user("把 handler 拆到单独的包里"), call("internal/handler", kind: .fileChange),
                approval("A0", "mkdir -p internal/handler", "新建目录", now: now), resolved("A0"), call("mkdir -p internal/handler"),
                agent("拆分完成，跑一下测试确认没有破坏现有行为。"),
                approval("A1", "go test ./... -count=1", "跑一遍全部测试，确认拆包没有破坏行为", now: now),
            ]),
            "dep": events(now, [
                turn, user("把依赖都升级到最新的版本"), call("go list -m -u all"),
                agent("有 12 个依赖可以升级，其中 `pgx` 是 v4 → v5 的大版本。"),
                question("Q1", [
                    ask("大版本也一起升吗？", [("只升小版本 (Recommended)", "改动小，go build 大概率直接通过"), ("包括 pgx v5", "要改连接池和事务相关的调用")]),
                    ask("要顺便更新 go.mod 的 Go 版本吗？", [("更新到 1.27", ""), ("保持不变", "")]),
                ]),
            ]),
            "plan": events(now, [
                turn, user("先出个方案，把订单服务里的库存逻辑拆出去"), call("读取 14 个文件", kind: .other, item: "Read"),
                agent("方案如下，批准后开始改。"),
                .planProposed(.with {
                    $0.text = "1. 新建 `inventory` 包，迁移库存扣减和回滚\n2. 订单服务改为调用接口，保留旧实现做开关\n3. 补充集成测试，覆盖并发扣减\n4. 灰度开关上线"
                }),
            ]),
            "ended": events(now, [
                turn, user("缓存是多久失效"),
                agent("没有按时间失效。模型列表缓存在前端页面的内存里，**同一台机器每次页面加载只请求一次**；刷新或重新打开页面会重新获取。"),
                turnEnd(12_000),
            ]),
            "bill": events(now, [
                turn, user("把 3 月的对账结果导出成 CSV"), call("python scripts/export.py --month 2026-03"),
                agent("已导出到 `out/2026-03.csv`，共 1,284 行。"),
            ]),
        ]
    }

    // MARK: Crowded

    private static let longTitle = "把结算服务里按商户维度聚合的对账任务从定时批处理迁到事件驱动，并补上幂等和重放的集成测试，顺便整理一下文档"

    private static func crowded(_ now: Int64) -> RemoteState {
        var s = RemoteState()
        let nearlyOut: [PBAgentQuota] = [
            .with {
                $0.agent = .codex
                $0.state = .ok
                $0.plan = "Enterprise · 团队共享额度（华东二区）"
                $0.observedMs = now - 5 * 60_000
                $0.windows = [
                    .with {
                        $0.name = "primary"
                        $0.windowMinutes = 300
                        $0.usedPercent = 99.6
                        $0.resetsAtMs = now + 12 * 60_000
                    },
                    .with {
                        $0.name = "secondary"
                        $0.windowMinutes = 10080
                        $0.usedPercent = 100
                        $0.resetsAtMs = now + 6 * 86_400_000
                    },
                ]
            },
            .with {
                $0.agent = .claude
                $0.state = .failed
                $0.detail = "claude: not logged in"
                $0.observedMs = now - 3 * 86_400_000
            },
        ]
        s.machines = keyed([
            machine("mac", "alex-macbook-pro-16-inch-2026.local", now: now, claude: true, quotas: nearlyOut),
            machine("vps", "ip-10-0-12-231.ap-southeast-1.compute.internal", now: now),
            machine("imac", "iMac", now: now, online: false, claude: true),
            machine("studio", "Mac-Studio-录音棚", now: now, claude: true),
            machine("ci", "ci-runner-07", now: now, online: false),
            machine("nas", "", now: now),
        ])
        let projects = [
            foo, home + "/Code/billing", home + "/Code/shop", home + "/Code/remote-agent", home + "/work/blog",
            home + "/Code/a-very-long-project-name-that-keeps-going-and-going/services/settlement-reconciliation-worker",
            home + "/Code/数据平台/实时数仓", "/srv/www/site", home + "/Code/infra", home + "/Code/mobile",
            home + "/Code/design-system", home + "/tmp/scratch",
        ]
        let hosts = ["mac", "vps", "imac", "studio", "ci", "nas"]
        var list: [SessionView] = [
            // Everything that can wait, at once, on one session.
            session("all", "mac", foo, longTitle, now: now, status: .running, agent: .claude, approvals: 3, questions: 2, plan: true, preview: "请求执行 kubectl --context prod-cn-east-2 -n settlement rollout restart deployment/reconciliation-worker", updated: now - 40 * 60_000, mode: .fullAccess, context: 752_000, queued: 4),
            session("ask2", "vps", projects[5], "Fix flaky test TestReconcile_ReplayAfterPartialFailure_WithOutOfOrderEventsAndClockSkew", now: now, approvals: 12, preview: "rm -rf ./build && make -j16 all test integration-test e2e-test", updated: now - 3 * 60_000),
            session("q2", "studio", projects[6], "口径核对", now: now, agent: .claude, questions: 1, updated: now - 9 * 60_000),
            session("p2", "nas", projects[2], "", now: now, plan: true, updated: now - 70 * 60_000, mode: .plan),
            session("popup", "vps", projects[7], "部署", now: now, status: .blocked, updated: now - 50_000, detail: "Do you trust the files in this folder? /srv/www/site/releases/2026-10-09T08-15-22Z/current"),
            // Waiting on a computer that is offline.
            session("offask", "imac", projects[1], "对账脚本（离线的电脑上还有审批）", now: now, approvals: 1, updated: now - 5 * 3_600_000),
            session("offrun", "ci", projects[8], "夜间构建", now: now, status: .running, preview: "terraform plan -out=tfplan", updated: now - 9 * 3_600_000),
            session("start", "studio", projects[9], "", now: now, status: .starting, updated: now - 4_000, created: now - 5_000),
        ]
        for i in 0..<18 {
            let agent: PBAgent = i % 3 == 0 ? .claude : .codex
            list.append(session(
                "live\(i)", hosts[i % hosts.count], projects[i % projects.count],
                i % 5 == 0 ? "" : "进行中的任务 \(i + 1)：" + String(longTitle.prefix(6 + i * 3)), now: now,
                status: i % 2 == 0 ? .running : .idle, agent: agent, preview: i % 4 == 0 ? "" : "正在处理第 \(i * 7 + 3) 个文件，共 \(i * 31 + 40) 个",
                updated: now - Int64(i) * 11 * 60_000, created: now - Int64(i + 1) * 3_600_000, mode: i % 6 == 0 ? .auto : .ask,
                context: i % 4 == 0 ? Int64(600_000 + i * 8_000) : 0, queued: i % 7 == 0 ? 2 : 0))
        }
        for i in 0..<60 {
            list.append(session(
                "old\(i)", hosts[i % hosts.count], projects[(i * 5) % projects.count],
                i % 9 == 0 ? "" : "已结束 \(i + 1) · " + String(longTitle.dropFirst(i % 20).prefix(4 + i % 30)), now: now,
                status: .exited, agent: i % 4 == 0 ? .claude : .codex, preview: "最后一条回复的开头，第 \(i) 条",
                updated: now - Int64(i) * 9 * 3_600_000 - 30 * 60_000))
        }
        s.sessions = keyed(list)
        var devices = [client(me, now: now, name: "一个名字特别长的客户端，用来看一行放不放得下", platform: "ios", description: "iOS 26.0 · iPhone", ageMs: 200 * 86_400_000)]
        for i in 0..<14 {
            let id = String(repeating: String(i, radix: 16), count: 16)
            devices.append(client(
                id, now: now, name: i % 3 == 0 ? "" : ["我的手机", "公司的 iPad Pro 13 英寸（带键盘）", "Chrome 浏览器 - 家里的台式机", "备用机"][i % 4],
                platform: ["android", "ios", "web", ""][i % 4], description: i % 5 == 0 ? "" : ["Android 16 · Pixel 9 Pro XL", "iOS 26.0 · iPad", "Chrome 141 · macOS 26"][i % 3],
                ageMs: Int64(i + 1) * 20 * 86_400_000, seenMs: i % 4 == 0 ? 0 : Int64(i) * 3 * 86_400_000 + 60_000))
        }
        s.devices = keyed(devices)
        s.prefs = prefs(pinned: Array(projects.prefix(5)), hidden: [projects[11], home + "/Code/demo"])
        s.conn = .online
        s.link = .online
        s.loaded = true
        s.synced = true
        // The server has more ended sessions than were fetched.
        s.sessionsNext = SessionPlace(endedMs: now - 60 * 9 * 3_600_000, sessionId: "old59")
        return s
    }

    private static func crowdedEvents(_ now: Int64) -> [String: [PBEvent]] {
        var long: [PBEvent.OneOf_Kind] = []
        for i in 0..<40 {
            long += [
                turn, user(i % 7 == 0 ? longTitle + "。" + longTitle : "第 \(i + 1) 轮：继续"),
                thought("看一下相关的调用。"), call("rg -n \"Reconcile\" services/ --glob '!**/testdata/**' | head -\(20 + i)"),
                call("go test ./services/settlement/... -run TestReconcile -count=1", output: String(repeating: "ok  \tservices/settlement\t0.42s\n", count: 3), exit: i % 5 == 0 ? 1 : 0),
                agent("第 \(i + 1) 轮的结果：\n\n| 模块 | 用例 | 结果 |\n|---|---|---|\n| settlement | \(12 + i) | 通过 |\n| replay | \(3 + i) | \(i % 5 == 0 ? "**失败**" : "通过") |\n\n```go\nfunc Reconcile(ctx context.Context, merchantID string) error {\n\treturn store.WithTx(ctx, func(tx *Tx) error { return tx.Apply(merchantID) })\n}\n```"),
                turnEnd(Int64(8_000 + i * 900)),
            ]
        }
        long += [
            turn, user("部署到生产前再确认一遍"),
            approval("A1", "kubectl --context prod-cn-east-2 -n settlement rollout restart deployment/reconciliation-worker", "重启生产环境的对账 worker，使新的幂等键生效", now: now),
            approval("A2", "psql \"$DATABASE_URL\" -c 'ALTER TABLE settlements ADD COLUMN idempotency_key text'", "给结算表加幂等键列", now: now),
            approval("A3", "git push origin HEAD:release/2026-10", "推送发布分支", now: now),
            question("Q1", [
                ask("重放窗口取多长？这个值同时决定了去重表保留多久、补偿任务最多回看多远，以及凌晨批处理和事件流重叠时以谁为准。", [
                    ("24 小时 (Recommended)", "覆盖一次完整的日切，去重表大约 40 GB"), ("7 天", "覆盖周末积压，去重表约 280 GB，需要先扩容"),
                    ("不限", "所有事件永久可重放；去重表无限增长，不建议"),
                ]),
                ask("哪些环境一起上？", multi: true, [("staging", ""), ("prod-cn-east-2", ""), ("prod-ap-southeast-1", ""), ("prod-eu-central-1", "")]),
            ]),
            .planProposed(.with { $0.text = (1...12).map { "\($0). 第 \($0) 步：" + String(longTitle.prefix(20 + $0)) }.joined(separator: "\n") }),
        ]
        return [
            "all": events(now, long),
            "ask2": events(now, [turn, user("修一下这个不稳定的测试"), approval("B1", "rm -rf ./build && make -j16 all test integration-test e2e-test", "", now: now)]),
            "old0": events(now, [turn, user("你好"), agent("你好。"), turnEnd(900)]),
        ]
    }

    // MARK: Empty

    private static func empty(_ now: Int64) -> RemoteState {
        var s = RemoteState()
        s.devices = keyed([client(me, now: now, platform: "ios", description: "iOS 26.0 · iPhone", ageMs: 60_000)])
        s.prefs = .empty
        s.conn = .online
        s.link = .online
        s.loaded = true
        s.synced = true
        return s
    }
}
#endif
