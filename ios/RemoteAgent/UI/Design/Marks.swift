// The drawn marks of design-system §2 and §6: the status graphic at the
// head of a row, which always has its status word beside it, and a
// project's avatar. They are drawn, not icons, and grow with the text.
import RACore
import SwiftUI

/// The status graphic (design-system §2 「状态图形」).
/// How a session stands, by colour family: running, waiting for you, idle, off.
enum StatusTone: Sendable, CaseIterable { case run, wait, idle, off }

enum StatusKind: Sendable, Equatable {
    /// 运行中: an ink ring, half open, turning; still when the system asks for less motion.
    case running
    /// 空闲: an empty grey ring.
    case idle
    /// 等你: an orange dot.
    case waiting
    /// 未连接, 电脑离线: a faint dot.
    case off
    /// 未读结果: a blue dot.
    case unread
    /// 在线: a green dot.
    case online

    /// A session's status as its row draws it; an ended one has no graphic.
    init?(_ status: Status) {
        switch status.tone {
        case .run: self = .running
        case .idle: self = .idle
        case .wait: self = .waiting
        case .off:
            if status == .exited { return nil }
            self = .off
        }
    }
}

struct StatusMark: View {
    private let kind: StatusKind
    @ScaledMetric(relativeTo: .body) private var side: CGFloat = 10
    @Environment(\.accessibilityReduceMotion) private var still

    init(_ kind: StatusKind) {
        self.kind = kind
    }

    var body: some View {
        Group {
            switch kind {
            case .running:
                Spinner(color: DS.ink).frame(width: side + 2, height: side + 2)
            case .idle:
                Circle().strokeBorder(DS.ink3, lineWidth: 1.6).frame(width: side, height: side)
            case .waiting: dot(DS.orange)
            case .off: dot(DS.faint)
            case .unread: dot(DS.blue)
            case .online: dot(DS.green)
            }
        }
        .accessibilityHidden(true)
    }

    private func dot(_ color: Color) -> some View {
        Circle().fill(color).frame(width: side * 0.8, height: side * 0.8)
    }
}

/// The running ring: a little over half a circle in ink, a turn every 0.9 s.
/// The turning ring of something in progress, half open; still when the system asks for less motion.
struct Spinner: View {
    var color: Color = DS.ink
    @State private var turning = false
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        Circle()
            .trim(from: 0, to: 0.7)
            .stroke(color, style: StrokeStyle(lineWidth: 1.8, lineCap: .round))
            .rotationEffect(.degrees(turning ? 360 : 0))
            .animation(still ? nil : .linear(duration: 0.9).repeatForever(autoreverses: false), value: turning)
            .onAppear { turning = !still }
            .onChange(of: still) { _, still in turning = !still }
    }
}

/// A project's avatar: its name's first letter in ink on a `fill2` circle,
/// or 全 for every project. Never coloured by project: colour says state
/// (design-system §2). It grows with the text beside it.
struct Avatar: View {
    private let letter: String
    @ScaledMetric private var side: CGFloat

    /// `key` is the project's path.
    init(_ key: String, size: CGFloat = 28) {
        letter = projectName(key).prefix(1).lowercased()
        _side = ScaledMetric(wrappedValue: size, relativeTo: .body)
    }

    /// 全, for 全部项目.
    static func all(size: CGFloat = 28) -> Avatar { Avatar(letter: L10n.commonAllProjectsAvatar(), size: size) }

    private init(letter: String, size: CGFloat) {
        self.letter = letter
        _side = ScaledMetric(wrappedValue: size, relativeTo: .body)
    }

    var body: some View {
        Text(letter)
            .font(.system(size: side * 0.5, weight: .bold))
            .foregroundStyle(DS.ink)
            .frame(width: side, height: side)
            .background(DS.fill2, in: Circle())
            .accessibilityHidden(true)
    }
}
