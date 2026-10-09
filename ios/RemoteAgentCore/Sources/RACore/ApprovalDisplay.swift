// From Android's ui/ApprovalDisplay.kt.

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
