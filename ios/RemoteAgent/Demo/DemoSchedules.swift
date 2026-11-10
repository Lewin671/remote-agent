// Sample scheduled tasks for `-demo` (debug builds): one that stopped, two
// with new results, a quiet one, a paused one and one that ended, so the
// 定时 page shows each of its groups (design-system §6 「定时列表」).
#if DEBUG
    import Foundation
    import RACore

    extension Demo {
        /// The tasks of a sample set, and those whose latest result is still unread.
        static func schedules(_ demo: DemoSet, now: Int64) -> (tasks: [MachineSchedule], unread: [String]) {
            guard demo != .empty else { return ([], []) }
            let t = Date(timeIntervalSince1970: Double(now) / 1000)
            func iso(_ minutesAgo: Double) -> String { scheduleISO(t.addingTimeInterval(-minutesAgo * 60)) }
            func run(_ id: String, _ minutesAgo: Double, status: String = "completed", notified: Bool, text: String = "", summary: String) -> String {
                """
                {"id":"\(id)","started_at":"\(iso(minutesAgo + 3))","ended_at":"\(iso(minutesAgo))","status":"\(status)",\
                "notified":\(notified),"notification_source":"\(notified ? "agent" : "")","notification_text":\(json(text)),\
                "final_summary":\(json(summary)),"start_seq":0,"after_seq":0}
                """
            }
            func task(
                _ id: String, _ title: String, cwd: String, agent: String, trigger: String, status: String = "active", reason: String = "",
                runs: [String], created: Double = 60 * 24 * 9, message: String
            ) -> String {
                """
                {"id":"\(id)","revision":3,"status":"\(status)","reason":\(json(reason)),"source":"user",\
                "created_at":"\(iso(created))","updated_at":"\(iso(30))",\
                "spec":{"title":\(json(title)),"message":\(json(message)),"trigger":\(trigger),\
                "target":{"type":"new_session","cwd":"\(cwd)","agent":"\(agent)","mode":"ask"},"notify":"agent"},\
                "runs":[\(runs.joined(separator: ","))]}
                """
            }
            let daily = #"{"type":"calendar","time":"08:00","timezone":"Asia/Shanghai"}"#
            let weekly = #"{"type":"calendar","time":"09:30","weekdays":[1],"timezone":"Asia/Shanghai"}"#
            let hourly = #"{"type":"interval","interval_seconds":3600}"#
            let tasks = [
                task(
                    "deps", "检查依赖更新", cwd: foo, agent: "codex", trigger: weekly, status: "paused",
                    reason: "agent_schedule_permissions_changed",
                    runs: [run("deps-1", 60 * 26, status: "failed", notified: false, summary: "")],
                    message: "看一下 go.mod 里有没有安全更新，有就开一个分支升级并跑测试。"),
                task(
                    "ci", "每天早上看 CI", cwd: foo, agent: "codex", trigger: daily,
                    runs: [
                        run("ci-2", 95, notified: true, text: "main 上有 2 个失败的测试",
                            summary: "main 分支昨晚的构建有 2 个测试失败：TestRelayReconnect 偶发超时，TestQuotaReset 在跨天时算错了时区。已经在 fix/quota-tz 分支修了第二个，第一个需要你决定要不要加长超时。"),
                        run("ci-1", 60 * 24 + 95, notified: true, text: "CI 全绿", summary: "CI 全绿，没有新的失败。"),
                    ],
                    message: "每天 8 点看一下 main 的 CI，有失败就找原因。"),
                task(
                    "report", "周报草稿", cwd: billing, agent: "claude", trigger: weekly,
                    runs: [run("report-1", 60 * 30, notified: true, text: "周报草稿写好了", summary: "本周合并了 14 个 PR，主要是账单导出和对账脚本；草稿在 docs/weekly/2026-45.md。")],
                    message: "根据这周合并的 PR 写一份周报草稿。"),
                task(
                    "health", "接口健康检查", cwd: foo, agent: "codex", trigger: hourly,
                    runs: [run("health-1", 40, notified: false, summary: "所有接口正常，p95 延迟 180ms。")],
                    message: "每小时访问一次健康检查接口，异常时告诉我。"),
                task(
                    "blog", "整理博客草稿", cwd: home + "/Code/blog", agent: "claude", trigger: daily, status: "paused", reason: "user_paused",
                    runs: [run("blog-1", 60 * 72, notified: true, text: "", summary: "整理了 3 篇草稿的目录。")],
                    message: "把 drafts 里的文章按主题整理一下。"),
                task(
                    "release", "发布前检查", cwd: foo, agent: "codex", trigger: #"{"type":"at","at":"\#(iso(60 * 50))"}"#,
                    status: "completed", runs: [run("release-1", 60 * 50, notified: false, summary: "发布前检查都通过了。")],
                    message: "发布前把 changelog、版本号和迁移脚本检查一遍。"),
            ]
            let decoded = tasks.compactMap { try? JSONDecoder().decode(ScheduleTask.self, from: Data($0.utf8)) }
            return (decoded.map { MachineSchedule(machineID: "mac", task: $0) }, ["ci", "report"])
        }

        private static func json(_ s: String) -> String {
            String(data: (try? JSONEncoder().encode(s)) ?? Data("\"\"".utf8), encoding: .utf8) ?? "\"\""
        }
    }
#endif
