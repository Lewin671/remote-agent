import Foundation
import Observation
import RAProto

@MainActor @Observable
final class TerminalHistory {
    struct Page: Identifiable { let id: String; let text: String }
    private(set) var visible = false
    private(set) var pages: [Page] = []
    private(set) var cursor = ""
    private(set) var pending = ""
    private(set) var error = ""
    @ObservationIgnored private var timeout: Task<Void, Never>?

    func close() { timeout?.cancel(); visible = false; pending = ""; pages = []; cursor = ""; error = "" }
    func disconnected() {
        timeout?.cancel(); pending = ""
        if visible { error = "终端连接已断开，请返回终端并重新连接" }
    }
    func request(fresh: Bool = false, send: (String, String) -> Void) {
        if fresh { close(); visible = true; pages = []; cursor = ""; error = "" }
        guard visible, pending.isEmpty, fresh || pages.isEmpty || !cursor.isEmpty else { return }
        let id = UUID().uuidString
        pending = id; error = ""
        timeout = Task { [weak self] in
            guard (try? await Task.sleep(for: .seconds(10))) != nil, let self, self.pending == id else { return }
            self.pending = ""; self.error = "读取历史超时，请重试"
        }
        send(id, fresh ? "" : cursor)
    }
    func receive(_ page: PBTerminalHistoryPage) {
        guard visible, page.requestID == pending else { return }
        timeout?.cancel(); pending = ""
        if !page.error.isEmpty { error = "历史读取失败，请重试；若历史已过期，请返回终端后重新打开"; return }
        error = ""; cursor = page.nextCursor
        if !page.lines.isEmpty { pages.insert(Page(id: page.requestID, text: page.lines.joined(separator: "\n")), at: 0) }
    }
}
