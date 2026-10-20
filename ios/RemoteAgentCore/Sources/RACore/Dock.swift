// What waits for the user above the message box, from Android's ui/Dock.kt.
import Foundation
import RAProto

/// What the dock holds: the highest item of the priority list.
public enum DockItem: Equatable, Sendable, Identifiable {
    case approval(PBApprovalRequested)
    case question(PBQuestionAsked)
    case plan(text: String)
    case popup(title: String)

    public var id: String {
        switch self {
        case .approval(let a): return "a:" + a.approvalID
        case .question(let q): return "q:" + q.questionID
        case .plan: return "plan"
        case .popup(let title): return "popup:\(title)"
        }
    }
}

/// What the dock remembers an item by when it is folded. A session has one plan at a time, so a plan
/// is told from the one before it by its text: a new plan opens again.
public func dockFoldKey(session: String, _ item: DockItem) -> String {
    switch item {
    case .plan(let text): return session + "/plan:" + text
    default: return session + "/" + item.id
    }
}

/// The dock's one line: type, a summary, 处理 (client-v7).
public func dockSummary(_ item: DockItem, agent: String) -> (label: String, brief: String) {
    switch item {
    case .approval(let a):
        if isSkeleton(a) { return (skeletonMessage, "") }
        return (a.command.isEmpty ? "请求权限" : "请求执行", approvalDisplay(a.command.ifEmpty(a.toolName)).jvmLines()[0])
    case .question(let q):
        if isSkeleton(q) { return (skeletonMessage, "") }
        return ("\(agent) 在问你", q.questions.first?.text.jvmLines()[0] ?? "")
    case .plan(let text): return (isSkeleton(text) ? skeletonMessage : "计划待批准", "")
    case .popup(let title): return ("终端里有弹窗", title)
    }
}

/// A question is long when the dock would cut it: then it has a page of its own.
public func longQuestion(_ q: PBQuestion) -> Bool {
    q.text.jvmLength > 120 || q.options.count > 4 || q.options.reduce(0) { $0 + $1.label.jvmLength + $1.description_p.jvmLength } > 400
}
