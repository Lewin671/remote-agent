// Sample sessions for looking at the session screens in the states the
// app's own sample data (Demo/DemoData.swift) does not have, debug builds
// only: a queue with paused and held messages, every kind of row, messages
// that did not go, a session that is starting, modes outside the four, a
// computer on another protocol version, and a history long enough to
// measure. They are shown through the same never-started `Remote` as the
// rest of the sample data, so the screens take them the way they take a
// real session.
//
//   -demo -demoRoute session/sample-rows      (and sample-queue, sample-send, sample-start,
//                                              sample-claude, sample-old, sample-long,
//                                              sample-wait, sample-many, sample-ask, sample-ended)
//   -sessionPerf          print what each pass over the events cost, and the frames while session.stream plays
//   -sessionShow <what>   open a session with something showing: menu, mode, confirmFull,
//                         model, rename, project (项目设置), stop, dock (the docked item
//                         open), reason (the reason box of an approval)
//
// Signals (see App/Launch.swift): session.stream plays a turn into the
// session on screen, a reply and tool calls arriving as they would;
// session.resolve answers its first approval as another client would;
// session.back scrolls away from the end as a finger would, session.end
// goes back to it; session.focus and session.blur bring the keyboard up
// and down.
//
// For driving a real session without a finger, each of these does what the
// control it names does, through the same code: session.draft puts the text
// of `defaults write <bundle> debug.sessionText …` in the message box,
// session.send, session.queue and session.interrupt press the box's button;
// session.allow and session.deny answer the docked approval (deny with that
// text as its reason); session.cancel and session.resume act on the last
// waiting message; session.menu, session.mode, session.model, session.rename
// and session.project open what the ⋯ menu opens, session.mode.pick chooses the
// mode named by the text (ask, auto, plan, full) and session.rename.save
// saves the text as the title.
import Foundation
import RACore

/// What `-sessionShow` asks to have showing. Nothing in a release build.
enum SessionDebug {
    #if DEBUG
    static let shown: String? = {
        let args = ProcessInfo.processInfo.arguments
        guard let i = args.firstIndex(of: "-sessionShow"), i + 1 < args.count else { return nil }
        return args[i + 1]
    }()
    #else
    static let shown: String? = nil
    #endif

    static func shows(_ what: String) -> Bool { shown == what }

    /// `-sessionPerf`: print what each pass over the events cost, and the longest frames while a turn is played.
    #if DEBUG
    static let perf = ProcessInfo.processInfo.arguments.contains("-sessionPerf")
    #else
    static let perf = false
    #endif

    /// Text for the signals that type: `defaults write <bundle> debug.sessionText …`, then the signal.
    static var text: String { UserDefaults.standard.string(forKey: "debug.sessionText") ?? "" }
}

#if DEBUG
import RAProto
import SwiftProtobuf
import SwiftUI
import UIKit

@MainActor
enum SessionSamples {
    static let machine = "sample-mac"
    static let oldMachine = "sample-old"
    static let other = "c0ffee00c0ffee00"
    static let cwd = "/Users/me/Code/foo"
    private static var installed = false

    /// Adds the sample sessions to what the sample `Remote` shows, once.
    static func install(_ remote: Remote, store: SessionStore) async {
        guard !installed else { return }
        installed = true
        // After the app's own sample data: this adds to it.
        _ = await remote.state.first { $0.loaded }
        let now = nowMs()
        var state = remote.current
        for m in machines(now) { state.machines[m.id] = m }
        for s in sessions(now) { state.sessions[s.id] = s }
        state.devices[other] = DeviceView(
            id: other, kind: .client, createdMs: now - 86_400_000,
            meta: .with {
                $0.name = "我的 iPad"
                $0.platform = "ios"
            }, metaRev: 1, lastSeenMs: now - 60_000)
        await remote.showForTest(state: state, events: events(now), modelLists: modelLists, live: true)
        local(store)
    }

    // MARK: Builders

    nonisolated private static func ts(_ ms: Int64) -> Google_Protobuf_Timestamp { .with { $0.seconds = ms / 1000 } }

    private static func machines(_ now: Int64) -> [MachineView] {
        func make(_ id: String, _ name: String, version: UInt32) -> MachineView {
            MachineView(
                id: id, online: true, lastSeenMs: now,
                meta: .with {
                    $0.hostname = name
                    $0.home = "/Users/me"
                    $0.os = "darwin"
                    $0.protocolVersion = version
                    $0.capabilities = .with { $0.imageUpload = true }
                    $0.agents = [PBAgent.codex, .claude].map { agent in .with { $0.agent = agent } }
                }, metaRev: 1)
        }
        return [make(machine, "MacBook", version: protocolVersion), make(oldMachine, "旧 Mac mini", version: protocolVersion - 1)]
    }

    private static func session(
        _ id: String, _ title: String, now: Int64, machine: String = machine, status: PBSessionStatus = .idle,
        agent: PBAgent = .codex, mode: PBApprovalMode = .ask, approvals: Int32 = 0, context: Int64 = 0,
        edit: (inout PBSession) -> Void = { _ in }
    ) -> SessionView {
        var info = PBSession.with {
            $0.id = id
            $0.cwd = cwd
            $0.title = title
            $0.status = status
            $0.agent = agent
            $0.mode = mode
            $0.pendingApprovals = approvals
            $0.createdAt = ts(now - 3_600_000)
            $0.model = agent == .claude ? "claude-sonnet-5-5" : "gpt-6-astra"
            $0.effort = agent == .claude ? "high" : "medium"
            $0.contextTokens = context
            $0.contextWindow = context > 0 ? 400_000 : 0
        }
        edit(&info)
        return SessionView(id: id, machineId: machine, info: info, metaRev: 1, updatedMs: now)
    }

    private static func sessions(_ now: Int64) -> [SessionView] {
        [
            session("sample-rows", "每一种行", now: now, context: 348_000),
            session("sample-queue", "排队和暂停的消息", now: now, status: .running, context: 120_000),
            session("sample-send", "没发出去的消息", now: now),
            session("sample-start", "", now: now, status: .starting) { $0.createdAt = ts(now - 4_000) },
            session("sample-claude", "终端里设的模式", now: now, agent: .claude) {
                $0.mode = .unspecified
                $0.permissionMode = "acceptEdits"
                $0.model = "claude-haiku-4-5-20251001"
                $0.fullAccessUnavailable = true
            },
            session("sample-old", "另一个协议版本的电脑", now: now, machine: oldMachine),
            session("sample-long", "一段很长的历史", now: now, status: .running, context: 380_000),
            session("sample-wait", "别的客户端也在处理", now: now, status: .running, approvals: 1),
            session("sample-many", "一轮很忙的活", now: now, status: .running, agent: .claude, mode: .fullAccess),
            session("sample-ask", "有问题要回答", now: now, status: .running, agent: .claude) { $0.pendingQuestions = 1 },
            session("sample-ended", "手动结束的会话", now: now, status: .exited) {
                $0.statusDetail = "stopped"
                $0.endedAt = ts(now - 120_000)
            },
        ]
    }

    private static let modelLists: [(machineId: String, agent: PBAgent, models: PBListModelsResponse)] = {
        func model(_ slug: String, _ name: String, _ efforts: [String], autoUnavailable: Bool = false) -> PBModelInfo {
            .with {
                $0.slug = slug
                $0.displayName = name
                $0.efforts = efforts
                $0.defaultEffort = efforts.contains("medium") ? "medium" : ""
                $0.autoUnavailable = autoUnavailable
            }
        }
        let codex = PBListModelsResponse.with {
            $0.defaultModel = "gpt-6-astra"
            $0.models = [
                model("gpt-6-astra", "GPT-6-Astra", ["minimal", "low", "medium", "high", "xhigh"]),
                model("gpt-6-astra-mini", "GPT-6-Astra mini", ["low", "medium", "high"]),
                model("gpt-6-codex-max-preview-2026-10", "GPT-6 Codex Max（预览，2026-10）", ["low", "medium", "high", "max", "ultra"]),
                model("gpt-5.5", "GPT-5.5", ["low", "medium", "high"]),
                model("gpt-5.5-fast", "GPT-5.5 Fast", []),
                model("o9", "o9", ["medium", "high"]),
                model("o9-mini", "o9 mini", ["medium"]),
            ]
        }
        let claude = PBListModelsResponse.with {
            $0.defaultModel = "claude-opus-5-5"
            $0.models = [
                model("claude-opus-5-5", "Opus 5.5", ["low", "medium", "high", "xhigh", "max"]), model("claude-sonnet-5-5", "Sonnet 5.5", ["low", "medium", "high", "xhigh", "max"]),
                model("claude-haiku-4-5-20251001", "Haiku 4.5", [], autoUnavailable: true),
            ]
        }
        return [machine, oldMachine].flatMap {
            [(machineId: $0, agent: PBAgent.codex, models: codex), (machineId: $0, agent: PBAgent.claude, models: claude)]
        }
    }()

    /// Events numbered in the order given, a second apart, ending now. An event's origin is who caused it.
    private static func events(_ now: Int64, _ kinds: [(PBEvent.OneOf_Kind, PBOrigin?)], step: Int64 = 1000) -> [PBEvent] {
        kinds.enumerated().map { i, kind in
            .with {
                $0.seq = UInt64(i + 1)
                $0.time = ts(now - Int64(kinds.count - i) * step)
                $0.kind = kind.0
                if let origin = kind.1 { $0.origin = origin }
            }
        }
    }

    private static let mine = PBOrigin.with {
        $0.kind = .client
        $0.deviceID = Demo.me
    }
    private static let theirs = PBOrigin.with {
        $0.kind = .client
        $0.deviceID = other
    }
    private static let typed = PBOrigin.with { $0.kind = .local }

    private typealias Kind = (PBEvent.OneOf_Kind, PBOrigin?)

    private static let turn: Kind = (.turnStarted(PBTurnStarted()), nil)
    private static func user(_ text: String, by: PBOrigin? = mine, images: [PBImageRef] = []) -> Kind {
        (.userMessage(.with {
            $0.text = text
            $0.images = images
        }), by)
    }
    private static func agent(_ text: String) -> Kind { (.agentMessage(.with { $0.text = text }), nil) }
    private static func thought(_ text: String) -> Kind { (.reasoning(.with { $0.text = text }), nil) }
    private static func turnEnd(_ ms: Int64, aborted: Bool = false, by: PBOrigin? = nil) -> Kind {
        (.turnEnded(.with {
            $0.durationMs = ms
            if aborted { $0.outcome = .aborted }
        }), by)
    }

    private static func call(
        _ title: String, kind: PBToolKind = .command, exit: Int32? = nil, id: String? = nil, ms: Int64 = 0, images: [PBImageRef] = [],
        background: PBToolBackground = .unspecified, digest: String = "", item: String = "Bash"
    ) -> Kind {
        (.toolCall(.with {
            $0.title = title
            $0.callID = id ?? title
            $0.kind = kind
            $0.status = exit.map { $0 == 0 ? "completed" : "failed" } ?? "completed"
            $0.itemType = item
            $0.durationMs = ms
            $0.images = images
            $0.background = background
            $0.inputDigest = digest
            if let exit { $0.exitCode = exit }
        }), nil)
    }

    private static func started(_ title: String, id: String? = nil, digest: String = "") -> Kind {
        (.toolStarted(.with {
            $0.title = title
            $0.callID = id ?? title
            $0.kind = .command
            $0.itemType = "Bash"
            $0.input = title
            $0.inputDigest = digest
        }), nil)
    }

    private static func approval(_ id: String, _ command: String, _ description: String, now: Int64, digest: String = "", left: Int64 = 510_000) -> Kind {
        (.approvalRequested(.with {
            $0.approvalID = id
            $0.command = command
            $0.description_p = description
            $0.toolName = "Bash"
            $0.inputDigest = digest
            $0.deadline = ts(now + left)
        }), nil)
    }

    private static func resolved(_ id: String, _ decision: PBApprovalDecision, message: String = "", by: PBOrigin? = mine) -> Kind {
        (.approvalResolved(.with {
            $0.approvalID = id
            $0.decision = decision
            $0.message = message
        }), by)
    }

    private static func input(
        _ id: String, _ status: PBInputStatus, text: String = "", mode: PBSendMode = .steer, paused: Bool = false, reason: String = "",
        by: PBOrigin? = mine
    ) -> Kind {
        (.inputState(.with {
            $0.requestID = id
            $0.status = status
            $0.text = text
            $0.mode = mode
            $0.paused = paused
            $0.reason = reason
        }), by)
    }

    private static func usage(total: Int64, last: Int64, window: Int64 = 400_000) -> Kind {
        (.tokenUsage(.with {
            $0.totalTokens = total
            $0.lastTotalTokens = last
            $0.contextWindow = window
        }), nil)
    }

    /// A small picture, drawn here: the preview an event would carry.
    private static func picture(_ hue: CGFloat, w: Int, h: Int, index: Int32 = 0, sync: PBImageSync = .synced, path: String = "") -> PBImageRef {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        let size = CGSize(width: w, height: h)
        let image = UIGraphicsImageRenderer(size: size, format: format).image { ctx in
            let colors = [UIColor(hue: hue, saturation: 0.5, brightness: 0.95, alpha: 1), UIColor(hue: hue + 0.08, saturation: 0.7, brightness: 0.7, alpha: 1)]
            let gradient = CGGradient(colorsSpace: nil, colors: colors.map(\.cgColor) as CFArray, locations: nil)!
            ctx.cgContext.drawLinearGradient(gradient, start: .zero, end: CGPoint(x: size.width, y: size.height), options: [])
        }
        return .with {
            $0.imageID = "img-\(Int(hue * 100))-\(w)-\(index)"
            $0.sync = sync
            $0.cliIndex = index
            $0.path = path
            $0.width = Int32(w * 8)
            $0.height = Int32(h * 8)
            $0.previewWidth = Int32(w)
            $0.previewHeight = Int32(h)
            if sync != .nameOnly { $0.preview = image.pngData() ?? Data() }
        }
    }

    // MARK: The histories

    private static func events(_ now: Int64) -> [String: [PBEvent]] {
        [
            "sample-rows": events(now, rows(now)),
            "sample-queue": events(now, [
                turn, user("把导出改成流式的，别一次读进内存"), thought("先看现在怎么读的。"), call("rg -n ReadAll internal/export"),
                agent("现在是 `io.ReadAll` 之后再写，我改成边读边写。"), started("go test ./internal/export/... -run TestStream -count=1"),
                input("q1", .heldByCli, text: "顺便把缓冲区大小做成可配置的"),
                input("q2", .queued, text: "改完以后跑一下基准测试，对比一下内存占用，结果贴给我。另外看看有没有别的地方也是一次读进内存的，有的话列个清单，先不要改。", mode: .queue),
                input("q3", .queued, text: "然后提交", mode: .queue, by: theirs),
                input("q4", .queued, text: "这条是中断前排着的，方向变了以后要不要发由你决定", mode: .queue, paused: true),
                input("q5", .queued, text: "最后更新一下 CHANGELOG", mode: .queue),
            ]),
            "sample-send": events(now, [
                turn, user("看一下 CI 为什么红了"), call("gh run view --log-failed", ms: 4_200),
                agent("是 `lint` 这一步：`internal/export/stream.go` 有一个没用到的变量。"), turnEnd(21_000),
                input("f1", .queued, text: "修掉它"), input("f1", .failed, reason: "input_changed"),
                input("f2", .queued, text: "然后重新跑 CI"), input("f2", .needsTerminal, reason: "write_failed"),
                input("f3", .queued, text: "跑完告诉我"), input("f3", .unconfirmed),
            ]),
            "sample-start": events(now, [input("p1", .queued, text: "给这个仓库加一个 Makefile，包含 build、test 和 lint 三个目标")]),
            "sample-claude": events(now, [
                turn, user("把 README 里的安装步骤更新一下"), call("README.md", kind: .fileChange, item: "Edit"),
                agent("已更新安装步骤。"), turnEnd(9_000),
            ]),
            "sample-old": events(now, [turn, user("你好"), agent("你好。"), turnEnd(900)]),
            "sample-long": events(now, long(), step: 400),
            "sample-wait": events(now, [
                turn, user("把依赖装上"),
                approval("W1", "curl -fsSL https://example.com/install.sh | sh", "下载并运行安装脚本", now: now, left: 50_000),
                started("curl -fsSL https://example.com/install.sh | sh"),
            ]),
            "sample-many": events(now, busy()),
            "sample-ask": events(now, [
                turn, user("把缓存层换掉"), call("rg -n cache internal", item: "Grep"),
                (.questionAsked(.with {
                    $0.questionID = "SQ"
                    $0.questions = [
                        .with { q in
                            q.text = "换成哪一种？"
                            q.options = [("Redis (Recommended)", "已经在用，运维熟"), ("内存 LRU", "最简单，重启会丢"), ("Memcached", "")].map { o in
                                .with {
                                    $0.label = o.0
                                    $0.description_p = o.1
                                }
                            }
                        },
                        .with { q in
                            q.text = "哪些服务一起换？"
                            q.multiSelect = true
                            q.options = ["billing", "settlement", "gateway"].map { name in .with { $0.label = name } }
                        },
                    ]
                }), nil),
            ]),
            "sample-ended": events(now, [
                turn, user("把日志级别调成 debug"), call("config/log.yaml", kind: .fileChange), agent("已调成 `debug`。"), turnEnd(6_000),
                input("u1", .queued, text: "再把采样率调到 100%", mode: .queue), input("u1", .canceled, reason: "exited"),
                (.sessionState(.with { $0.status = .exited }), nil),
            ]),
        ]
    }

    /// One of every kind of row the chat draws.
    private static func rows(_ now: Int64) -> [Kind] {
        let shot = picture(0.58, w: 160, h: 100, index: 1, path: "/tmp/screenshot-2026-10-09 at 09.41.12.png")
        return [
            (.sessionForked(.with {
                $0.sourceSessionID = "run"
                $0.sourceTitle = "修复登录超时"
            }), nil),
            turn,
            user("[Image #1] [Image #2] 照这两张图把导出页面改一下", images: [
                picture(0.08, w: 120, h: 88, index: 1), picture(0.33, w: 60, h: 110, index: 2, sync: .previewOnly),
            ]),
            thought("先确认现在的布局是怎么写的，再对着图改。\n导出页在 web/src/Export.tsx。"),
            call("rg -n Export web/src", ms: 800), call("sed -n 1,80p web/src/Export.tsx"), call("git log -3 --oneline web/src/Export.tsx"),
            agent("## 改动\n\n导出页现在分成两栏，和图上一致：\n\n1. 左边是筛选条件\n2. 右边是预览表格，超过 200 行时**只渲染可见的行**\n\n```diff\n- <Table rows={rows} />\n+ <VirtualTable rows={rows} rowHeight={32} />\n```\n\n| 行数 | 之前 | 之后 |\n|---:|---:|---:|\n| 200 | 41 ms | 12 ms |\n| 20000 | 3.8 s | 14 ms |\n\n详见 [React 的文档](https://react.dev/reference/react/memo)。"),
            call("web/src/Export.tsx", kind: .fileChange, item: "Edit"), call("web/src/VirtualTable.tsx", kind: .fileChange, item: "Edit"),
            call("npm test -- --run Export", exit: 1, ms: 12_000),
            approval("R1", "npm install @tanstack/react-virtual", "安装虚拟列表的依赖", now: now), resolved("R1", .allow, by: theirs),
            call("npm install @tanstack/react-virtual", ms: 9_000),
            approval("R2", "rm -rf node_modules/.cache", "清掉缓存", now: now), resolved("R2", .deny, message: "先别清缓存"),
            call("截一张图看看效果", kind: .other, images: [shot], item: "Screenshot"),
            (.imageGenerated(.with {
                $0.revisedPrompt = "A clean two-column export page, filters on the left, a preview table on the right, light theme."
                $0.images = [picture(0.75, w: 200, h: 120)]
            }), nil),
            call("npm run dev", id: "bg1", background: .requested),
            usage(total: 300_000, last: 348_000),
            (.settingsChanged(.with { $0.compacted = true }), mine), usage(total: 320_000, last: 96_000),
            (.settingsChanged(.with {
                $0.model = "gpt-6-astra-mini"
                $0.effort = "high"
            }), theirs),
            (.settingsChanged(.with { $0.mode = .auto }), mine),
            (.planProposed(.with {
                $0.text = "1. 把筛选条件抽成 `useExportFilters`\n2. 预览表格换成虚拟列表\n3. 导出按钮在生成期间显示进度\n4. 补上 20000 行的性能测试\n5. 更新截图\n6. 更新文档\n7. 发版说明里写明行为变化"
            }), nil),
            // Few lines, each long: cut where they wrap, not by their count.
            (.planProposed(.with {
                $0.text = "先把筛选条件抽成 `useExportFilters`，让导出页和预览表格共用同一份状态，再把预览表格换成虚拟列表，超过 200 行时只渲染可见的行；导出按钮在生成期间显示进度，失败时保留已经填好的筛选条件；最后补上 20000 行的性能测试，更新截图和文档，并在发版说明里写明行为变化，提醒升级的人先清一次本地缓存。"
            }), nil),
            (.questionAnswered(.with { a in
                a.questionID = "Q0"
                a.answers = [.with { $0.answers = ["先做前两步"] }]
            }), theirs),
            usage(total: 352_000, last: 110_000),
            turnEnd(254_000, aborted: true, by: theirs),
            turn, user("页面右上角加一个刷新按钮", by: typed), agent("加好了，在标题右边。"), usage(total: 366_400, last: 118_000), turnEnd(18_000),
        ]
    }

    /// A long history: turns of a message, thinking, tools and a reply with a table and code.
    private static func long() -> [Kind] {
        var out: [Kind] = []
        for i in 0..<400 {
            out += [
                turn, user(i % 9 == 0 ? "第 \(i + 1) 轮：把结算服务里按商户聚合的对账任务再检查一遍，尤其是重放和幂等。" : "第 \(i + 1) 轮：继续"),
                thought("看一下相关的调用。"), call("rg -n \"Reconcile\" services/ | head -\(20 + i % 30)"),
                call("go test ./services/settlement/... -run TestReconcile -count=1", exit: i % 7 == 0 ? 1 : 0, ms: 2_000),
                call("git diff --stat"),
                agent("第 \(i + 1) 轮的结果：\n\n| 模块 | 用例 | 结果 |\n|---|---:|---|\n| settlement | \(12 + i) | 通过 |\n| replay | \(3 + i) | \(i % 7 == 0 ? "**失败**" : "通过") |\n\n- 幂等键已覆盖 `merchant_id` 和 `batch_id`\n- 重放窗口内的重复事件会被丢弃\n\n```go\nfunc Reconcile(ctx context.Context, merchantID string) error {\n\treturn store.WithTx(ctx, func(tx *Tx) error { return tx.Apply(merchantID) })\n}\n```"),
                usage(total: Int64(10_000 + i * 900), last: Int64(40_000 + i * 800)), turnEnd(Int64(8_000 + i * 90)),
            ]
        }
        out += [turn, user("最后再跑一遍全部测试"), started("go test ./... -count=1")]
        return out
    }

    /// A turn with many tools at once, some waiting, on a session in full access.
    private static func busy() -> [Kind] {
        var out: [Kind] = [turn, user("把所有服务的依赖都检查一遍")]
        for i in 0..<14 { out.append(call("go list -m -u all # services/svc-\(i)", ms: Int64(900 + i * 150))) }
        out += [
            agent("有 3 个服务的依赖落后一个大版本。我分别升级并跑测试。"),
            started("cd services/billing && go get -u ./... && go test ./... -count=1 -timeout 20m", id: "b1"),
            started("cd services/settlement && go get -u ./... && go test ./... -count=1 -timeout 20m", id: "b2"),
            started("cd services/a-very-long-service-name-that-keeps-going/reconciliation-worker && go get -u ./...", id: "b3"),
        ]
        return out
    }

    /// What this client itself holds for the samples: messages on their way, and a first message.
    private static func local(_ store: SessionStore) {
        func out(_ id: String, _ text: String, _ state: Outgoing.State, error: String = "") -> Outgoing {
            let o = Outgoing(requestId: id, text: text)
            o.state = state
            o.error = error
            return o
        }
        store.debugShow("sample-send", [
            out("f1", "修掉它", .sent), out("f2", "然后重新跑 CI", .sent), out("f3", "跑完告诉我", .sent),
            out("s1", "这条还在路上", .sending),
            out("s2", "这条没送到电脑", .failed, error: "电脑不在线，指令没有发出"),
            out("s3", "这条不确定送没送到", .unknown, error: "不确定电脑有没有执行：请求可能已经送到，之后网络断了"),
        ])
        store.debugShow("sample-ended", [out("u1", "再把采样率调到 100%", .sent)])
        store.rememberPrompt("sample-start", "给这个仓库加一个 Makefile，包含 build、test 和 lint 三个目标")
    }

    // MARK: Playing a turn

    private static var streams: [String: Task<Void, Never>] = [:]

    /// Counts the frames of the main thread, for `-sessionPerf`.
    private final class Frames: NSObject {
        private var link: CADisplayLink?
        private var last: CFTimeInterval = 0
        private(set) var count = 0
        private(set) var late = 0
        private(set) var worst: CFTimeInterval = 0

        func start() {
            link = CADisplayLink(target: self, selector: #selector(tick))
            link?.add(to: .main, forMode: .common)
        }

        @objc private func tick(_ link: CADisplayLink) {
            if last > 0 {
                let gap = link.timestamp - last
                count += 1
                // More than two frames of the display's own pace went by between two callbacks.
                if gap > link.duration * 2.5 { late += 1 }
                worst = max(worst, gap)
            }
            last = link.timestamp
        }

        func stop() -> String {
            link?.invalidate()
            return String(format: "PERF frames %d late %d worst %.1f ms", count, late, worst * 1000)
        }
    }

    /// Appends to a session's history as a daemon would: a message, tools starting and ending, a reply in several parts.
    static func stream(_ remote: Remote, _ sid: String) {
        streams[sid]?.cancel()
        streams[sid] = Task {
            let frames = Frames()
            if SessionDebug.perf { frames.start() }
            defer { if SessionDebug.perf { print(frames.stop()) } }
            var list = Array(remote.events(sid).value.events)
            func add(_ kind: Kind) async {
                list.append(.with {
                    $0.seq = UInt64(list.count + 1)
                    $0.time = ts(nowMs())
                    $0.kind = kind.0
                    if let origin = kind.1 { $0.origin = origin }
                })
                await remote.showForTest(state: remote.current, events: [sid: list])
            }
            await add(turn)
            await add(user("再检查一遍并发的情况"))
            for i in 0..<60 {
                if Task.isCancelled { return }
                try? await Task.sleep(nanoseconds: 120_000_000)
                switch i % 6 {
                case 0: await add(started("go test ./... -race -run TestConcurrent\(i) -count=1", id: "s\(i)"))
                case 2: await add(call("go test ./... -race -run TestConcurrent\(i - 2) -count=1", id: "s\(i - 2)", ms: 1_200))
                case 3: await add(thought("没有数据竞争，再看锁的顺序。"))
                default:
                    await add(agent("第 \(i + 1) 段：并发扣减走的是 `SELECT … FOR UPDATE`，同一商户的两个请求会排队。\n\n- 锁的顺序固定为 `merchant` → `batch`\n- 超时 2 秒后返回 `ErrBusy`，由调用方重试"))
                }
            }
            await add(turnEnd(9_000))
        }
    }

    /// Answers the session's first pending approval from another client.
    static func resolve(_ remote: Remote, _ sid: String) {
        Task {
            var list = Array(remote.events(sid).value.events)
            guard let id = derive(list).approvals.keys.first else { return }
            list.append(.with {
                $0.seq = UInt64(list.count + 1)
                $0.time = ts(nowMs())
                $0.kind = .approvalResolved(.with {
                    $0.approvalID = id
                    $0.decision = .allow
                })
                $0.origin = theirs
            })
            var state = remote.current
            if var s = state.sessions[sid], var info = s.info {
                info.pendingApprovals = 0
                s.info = info
                state.sessions[sid] = s
            }
            await remote.showForTest(state: state, events: [sid: list])
        }
    }
}

extension SessionStore {
    /// Puts messages in a session's outbox as they would stand after sending, for looking at them.
    func debugShow(_ sid: String, _ list: [Outgoing]) {
        for o in list where find(sid, requestId: o.requestId) == nil { debugAppend(sid, o) }
    }
}

private struct SessionSampleTask: ViewModifier {
    let sid: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context

    func body(content: Content) -> some View {
        content
            .task {
                if model.isDemo && sid.hasPrefix("sample-") { await SessionSamples.install(context.remote, store: context.sessionStore) }
            }
            .debugSignal("session.stream") { if model.isDemo { SessionSamples.stream(context.remote, sid) } }
            .debugSignal("session.resolve") { if model.isDemo { SessionSamples.resolve(context.remote, sid) } }
    }
}

extension View {
    /// Shows the sample sessions when one of them is asked for (`session/sample-…` with `-demo`).
    func sessionSamples(_ sid: String) -> some View { modifier(SessionSampleTask(sid: sid)) }
}
#else
import SwiftUI

extension View {
    func sessionSamples(_ sid: String) -> some View { self }
}
#endif
