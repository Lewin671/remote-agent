// Driving the terminal from a script, and showing it without a server
// (debug builds; the pattern of `App/Launch.swift`). None of it is in a
// release build.
//
// Signals, sent with `xcrun simctl spawn <udid> notifyutil -p
// io.github.lewin671.remoteagent.debug.<name>` while a terminal is open:
//
//   term.type        types the text of the file `term-input` in the app's
//                    tmp directory, as the keyboard would (`insertText`)
//   term.ime         the same file's first line as an input method's
//                    marked text, and its second as the text then chosen
//   term.paste       pastes the file's text as the menu's 粘贴 does, without the clipboard
//   term.enter, term.esc, term.tab, term.ctrlc,
//   term.left, term.up, term.down, term.right     the key bar's keys
//   term.keyboard    the key bar's 键盘
//   term.fit         the toolbar's 适配 / 还原
//   term.fontUp, term.fontDown                    字号
//   term.reconnect   重新连接
//   term.details     查看详情
//   term.dump        writes what the pane shows (state, sizes, the screen's
//                    text) to `term-dump` in the tmp directory
//
// Launch arguments, with -demo -demoRoute terminal/<session>:
//
//   -termState <live|connecting|closed|offline|ended>   the pane in that state
//   -termSize <cols>x<rows>     the sample screen as a window of that size, not fitted
import RACore
import SwiftUI

#if DEBUG
@MainActor
enum TerminalDebug {
    private static let args = ProcessInfo.processInfo.arguments

    private static func value(_ name: String) -> String? {
        guard let i = args.firstIndex(of: name), i + 1 < args.count else { return nil }
        return args[i + 1]
    }

    static var input: String {
        let url = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("term-input")
        return (try? String(contentsOf: url, encoding: .utf8)) ?? ""
    }

    /// Shows sample data instead of connecting; false when the terminal is a real one.
    static func sample(_ terminal: RemoteTerminal, demo: Bool) -> Bool {
        guard demo else { return false }
        switch value("-termState") ?? "live" {
        case "connecting": terminal.show(.connecting)
        case "closed", "offline", "ended":
            screen(terminal)
            terminal.show(.closed("终端连接断开：NSURLErrorDomain -1005"))
        default: screen(terminal)
        }
        return true
    }

    private static func screen(_ terminal: RemoteTerminal) {
        let room = terminal.host.capacity
        if room.cols == 0 {
            // Not laid out yet: the sample is as large as the area.
            Task {
                try? await Task.sleep(for: .milliseconds(50))
                screen(terminal)
            }
            return
        }
        var size = TerminalSize(max(room.cols, 20), max(room.rows, 10))
        var fitted = true
        if let asked = value("-termSize")?.split(separator: "x").compactMap({ Int($0) }), asked.count == 2 {
            size = TerminalSize(asked[0], asked[1])
            fitted = false
        }
        terminal.show(cols: size.cols, rows: size.rows, data: Data(text(size).utf8), fitted: fitted)
    }

    /// A screen as Codex draws one: colours, a box, Chinese, wide and combined characters.
    private static func text(_ size: TerminalSize) -> String {
        let e = "\u{1b}["
        let rule = String(repeating: "─", count: max(size.cols - 2, 1))
        var lines = [
            "\(e)1m>_ OpenAI Codex\(e)0m \(e)2m(v0.142.0)\(e)0m",
            "",
            "\(e)2m╭\(rule)╮\(e)0m",
            "\(e)2m│\(e)0m model: \(e)36mgpt-6-luna\(e)0m  ~/Code/billing",
            "\(e)2m╰\(rule)╯\(e)0m",
            "",
            "\(e)33m  Folder access\(e)0m",
            "  信任这个目录吗？中文和 ASCII 对齐",
            "  |12345678|  宽字符 ｆｕｌｌ 😀 ✻ ⏵ ⎿ ⠋",
            "  |中文中文|  \(e)32m✓ passed\(e)0m \(e)31m✕ failed\(e)0m",
            "",
            "\(e)94m› 1. Trust and continue\(e)0m",
            "  2. Quit",
            "",
            "\(e)2m  enter continue · esc quit\(e)0m",
        ]
        lines.append("\(e)7m \(e)0m 第 \(size.cols)×\(size.rows) 屏")
        return lines.prefix(size.rows).joined(separator: "\r\n")
    }

    static func dump(_ terminal: RemoteTerminal, font: Int) {
        let host = terminal.host
        let emulator = host.terminal.getTerminal()
        let text = """
            state: \(terminal.state)
            remote: \(terminal.size.cols)x\(terminal.size.rows) fitted=\(terminal.fitted) resized=\(terminal.resizedElsewhere)
            emulator: \(emulator.cols)x\(emulator.rows)
            room: \(host.capacity.cols)x\(host.capacity.rows) font=\(font) drawn=\(host.terminal.font.pointSize)
            view: \(Int(host.bounds.width))x\(Int(host.bounds.height)) grid=\(host.terminal.frame.integral.debugDescription)
            keyboard: \(terminal.keyboard) responder=\(host.keyboardShown)
            ---
            \(host.screenText)

            """
        try? text.write(to: URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("term-dump"), atomically: true, encoding: .utf8)
    }
}

private struct TerminalDebugSignals: ViewModifier {
    let terminal: RemoteTerminal
    @Binding var font: Int
    @Binding var details: Bool
    let reconnect: () -> Void

    private func key(_ label: String) {
        if let key = terminalKeyRows.joined().first(where: { $0.label == label }) { terminal.send(key) }
    }

    private func setFont(_ points: Int) {
        font = min(max(points, TerminalFont.sizes.lowerBound), TerminalFont.sizes.upperBound)
        TerminalFont.preferred = font
        terminal.setFont(font)
    }

    func body(content: Content) -> some View {
        let view = terminal.host.terminal
        content
            .debugSignal("term.type") { view.insertText(TerminalDebug.input) }
            .debugSignal("term.ime") {
                let lines = TerminalDebug.input.split(separator: "\n").map(String.init)
                guard lines.count >= 2 else { return }
                view.setMarkedText(lines[0], selectedRange: NSRange(location: lines[0].utf16.count, length: 0))
                view.insertText(lines[1])
            }
            .debugSignal("term.paste") { view.send(txt: TerminalDebug.input) }
            .debugSignal("term.enter") { key("回车") }
            .debugSignal("term.esc") { key("Esc") }
            .debugSignal("term.tab") { key("Tab") }
            .debugSignal("term.ctrlc") { key("Ctrl+C") }
            .debugSignal("term.left") { key("左") }
            .debugSignal("term.up") { key("上") }
            .debugSignal("term.down") { key("下") }
            .debugSignal("term.right") { key("右") }
            .debugSignal("term.keyboard") { terminal.host.toggleKeyboard() }
            .debugSignal("term.fit") { terminal.toggleFit() }
            .debugSignal("term.fontUp") { setFont(font + 1) }
            .debugSignal("term.fontDown") { setFont(font - 1) }
            .debugSignal("term.reconnect") { reconnect() }
            .debugSignal("term.details") { details.toggle() }
            .debugSignal("term.dump") { TerminalDebug.dump(terminal, font: font) }
    }
}

extension View {
    func terminalDebug(_ terminal: RemoteTerminal, font: Binding<Int>, details: Binding<Bool>, reconnect: @escaping () -> Void) -> some View {
        modifier(TerminalDebugSignals(terminal: terminal, font: font, details: details, reconnect: reconnect))
    }
}
#else
extension View {
    func terminalDebug(_ terminal: RemoteTerminal, font: Binding<Int>, details: Binding<Bool>, reconnect: @escaping () -> Void) -> some View { self }
}
#endif
