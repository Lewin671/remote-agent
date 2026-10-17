// From Android's ui/ApprovalDisplay.kt.
import RAProto

/// Show approval display controls as escapes; never rewrite the actual
/// payload. A command that hides what it does behind direction overrides or
/// terminal escapes is shown with them spelled out.
public func approvalDisplay(_ text: String) -> String {
    var out = String.UnicodeScalarView()
    for u in text.unicodeScalars {
        let code = u.value
        if (code < 32 && u != "\n" && u != "\t") || (127...159).contains(code) || code == 0x061c || (0x200e...0x200f).contains(code)
            || (0x202a...0x202e).contains(code) || (0x2066...0x2069).contains(code)
        {
            let hex = String(code, radix: 16)
            out.append(contentsOf: "\\u".unicodeScalars)
            out.append(contentsOf: (String(repeating: "0", count: max(0, 4 - hex.count)) + hex).unicodeScalars)
        } else {
            out.append(u)
        }
    }
    return String(out)
}

/// What an approval asks for, as its card, its dock and its page head it.
public func approvalHead(_ a: PBApprovalRequested) -> String {
    a.command.isEmpty ? "请求权限" + (a.toolName.isEmpty ? "" : " · \(approvalDisplay(a.toolName))") : "请求执行"
}

/// What an approval would run or pass, shown with whatever it hides spelled
/// out (`approvalDisplay`). Every place that offers 允许 shows it: a tool
/// that is not a shell command has no command, only its input.
public func approvalBody(_ a: PBApprovalRequested) -> String {
    approvalDisplay(a.command.isEmpty ? a.toolInputJson : a.command)
}

// The relay's existing suffix marks every field shortened for upload.
private let truncatedNote = "\n…（remote-agent：内容过长，已截断）"
public let approvalTruncatedMessage = "内容被截断，请在终端查看"
public func approvalTruncated(_ a: PBApprovalRequested) -> Bool {
    [a.description_p, a.command, a.toolInputJson].contains { $0.hasSuffix(truncatedNote) }
}
