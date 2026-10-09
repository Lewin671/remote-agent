import Foundation
import Testing

@testable import RAMarkdown

/// The repository root, found from this file: the cases are shared with the web client and Android.
private let repoRoot: URL = {
    var u = URL(fileURLWithPath: #filePath)
    // .../ios/RemoteAgentCore/Tests/RAMarkdownTests/CasesTests.swift
    for _ in 0..<5 { u.deleteLastPathComponent() }
    return u
}()

/// The cases every client reads Markdown by. The web client
/// (markdown.cases.test.ts) and Android (MarkdownCasesTest.kt) run the same
/// file, so a change there changes all three or fails one. The trees are
/// compared in the shape the file has them.
@Suite struct CasesTests {
    let cases: [String: Any]

    init() throws {
        let data = try Data(contentsOf: repoRoot.appendingPathComponent("testdata/markdown/cases.json"))
        cases = try JSONSerialization.jsonObject(with: data) as! [String: Any]
    }

    private func spans(_ v: [MarkdownSpan]) -> [NSDictionary] {
        v.map { s -> [String: Any] in
            switch s {
            case .text(let v): ["t": "text", "v": v]
            case .code(let v): ["t": "code", "v": v]
            case .bold(let c): ["t": "b", "c": spans(c)]
            case .italic(let c): ["t": "i", "c": spans(c)]
            case .strike(let c): ["t": "s", "c": spans(c)]
            case .link(let v, let href): ["t": "link", "v": v, "href": href]
            case .ref(let v, let target): ["t": "ref", "v": v, "target": target]
            }
        }.map { $0 as NSDictionary }
    }

    private func blocks(_ v: [MarkdownBlock]) -> [NSDictionary] {
        v.map { b -> [String: Any] in
            switch b {
            case .paragraph(let text): return ["kind": "text", "text": text]
            case .code(let lang, let text): return ["kind": "code", "lang": lang, "text": text]
            case .heading(let level, let text): return ["kind": "heading", "level": level, "text": text]
            case .rule: return ["kind": "rule"]
            case .quote(let inner): return ["kind": "quote", "blocks": blocks(inner)]
            case .list(let ordered, let start, let items):
                return [
                    "kind": "list", "ordered": ordered, "start": start,
                    "items": items.map { item -> NSDictionary in
                        var d: [String: Any] = ["text": item.text, "children": blocks(item.children)]
                        // The file has no key at all for an item that is not a task.
                        if let task = item.task { d["task"] = task == .done ? "done" : "todo" }
                        return d as NSDictionary
                    },
                ]
            case .table(let head, let align, let rows):
                return [
                    "kind": "table", "head": head,
                    "align": align.map { a -> Any in
                        switch a {
                        case .left: "left"
                        case .center: "center"
                        case .right: "right"
                        case nil: NSNull()
                        }
                    },
                    "rows": rows,
                ]
            }
        }.map { $0 as NSDictionary }
    }

    /// Runs every case of a group and names all that differ, not only the first.
    private func check(_ name: String, _ run: ([String: Any]) -> String?) {
        let group = cases[name] as? [[String: Any]] ?? []
        #expect(!group.isEmpty, "no \(name) cases")
        let failed = group.compactMap(run)
        #expect(failed.isEmpty, "\(name) cases that read differently:\n\(failed.joined(separator: "\n"))")
    }

    @Test func blocks() {
        check("blocks") { c in
            let got = blocks(Markdown.parse(c["md"] as! String)) as NSArray
            let want = c["want"] as! NSArray
            return got == want ? nil : "\(c["name"]!)\n  want \(want)\n  got  \(got)"
        }
    }

    @Test func inline() {
        check("inline") { c in
            let got = spans(Markdown.inlineSpans(c["md"] as! String)) as NSArray
            let want = c["want"] as! NSArray
            return got == want ? nil : "\(c["name"]!)\n  want \(want)\n  got  \(got)"
        }
    }

    @Test func numericColumns() {
        check("numericColumn") { c in
            let cells = c["cells"] as! [String]
            let want = c["want"] as! Bool
            return Markdown.numericColumn(cells) == want ? nil : "\(cells) should be \(want)"
        }
    }

    @Test func looseLists() {
        check("looseList") { c in
            let items = c["items"] as! [String]
            let want = c["want"] as! Bool
            return Markdown.looseList(items) == want ? nil : "\(items) should be \(want)"
        }
    }

    @Test func boldHeadings() {
        check("boldHeading") { c in
            let want = c["want"] as! Bool
            return Markdown.boldHeading(c["md"] as! String) == want ? nil : "\(c["md"]!) should be \(want)"
        }
    }
}
