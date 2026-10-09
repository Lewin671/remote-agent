// The third-party work the app is built with, each with its licence text
// under Resources/Licenses. The texts are copied from the package
// checkouts at the versions Package.resolved pins; SettingsLicensesTests
// fails when a package is pinned that has no entry here, so a new
// dependency cannot ship without its licence.
import Foundation

struct License: Identifiable, Sendable, Equatable {
    /// Where its texts are shown from: the licence first, then a NOTICE file when the project has one.
    let files: [String]
    let name: String
    /// What the app uses it for, as the row says.
    let use: String
    /// The licence's short name: MIT, Apache-2.0, ISC.
    let kind: String
    /// The SwiftPM identity in Package.resolved; nil for what is not a package (the icons).
    let package: String?
    /// Its code is in the app. The others are only resolved with a package that is: optional parts the app does not build.
    let linked: Bool

    var id: String { files[0] }
}

enum Licenses {
    static let all: [License] = [
        License(files: ["ISC-Lucide"], name: "Lucide", use: "界面中的图标", kind: "ISC", package: nil, linked: true),
        License(files: ["Apache-2.0-swift-protobuf"], name: "swift-protobuf", use: "协议报文的编码", kind: "Apache-2.0", package: "swift-protobuf", linked: true),
        License(files: ["Apache-2.0-connect-swift"], name: "connect-swift", use: "和服务器通信", kind: "Apache-2.0", package: "connect-swift", linked: true),
        License(files: ["MIT-GRDB"], name: "GRDB", use: "本机的密文缓存", kind: "MIT", package: "grdb.swift", linked: true),
        License(files: ["MIT-SwiftTerm"], name: "SwiftTerm", use: "终端", kind: "MIT", package: "swiftterm", linked: true),
        unlinked("swift-nio", notice: true),
        unlinked("swift-nio-http2", notice: true),
        unlinked("swift-nio-ssl", notice: true),
        unlinked("swift-atomics"),
        unlinked("swift-collections"),
        unlinked("swift-system"),
        unlinked("swift-argument-parser"),
    ]

    private static func unlinked(_ name: String, notice: Bool = false) -> License {
        License(
            files: ["Apache-2.0-\(name)"] + (notice ? ["NOTICE-\(name)"] : []), name: name, use: "", kind: "Apache-2.0",
            package: name, linked: false)
    }

    /// A licence's text as the page shows it. The files are wrapped by hand
    /// at 70 to 80 columns and some are indented to be centred, which on a
    /// phone leaves every other line a few words long. So the indentation
    /// goes, and a line that was plainly wrapped (a full one, followed by
    /// more of the same paragraph) runs on into the next. Nothing else
    /// changes: short lines, blank lines, lists and each copyright line stay
    /// as written, and the files in the bundle are the originals.
    static func display(_ text: String) -> String {
        let lines = text.components(separatedBy: "\n").map { $0.trimmingCharacters(in: .whitespaces) }
        var out = ""
        for (i, line) in lines.enumerated() {
            out += line
            guard i + 1 < lines.count else { break }
            let next = lines[i + 1]
            let wrapped = line.count >= 60 && !next.isEmpty && !startsItem(next)
            out += wrapped ? " " : "\n"
        }
        return out
    }

    /// The line begins something of its own: a list item, a numbered clause, a copyright line, a heading.
    private static func startsItem(_ line: String) -> Bool {
        if line.hasPrefix("Copyright") || line.hasPrefix("#") || line.hasPrefix("---") { return true }
        guard let first = line.unicodeScalars.first else { return false }
        if "*-•(".unicodeScalars.contains(first) { return true }
        // 1. Definitions.
        let digits = line.prefix { $0.isNumber }
        return !digits.isEmpty && line.dropFirst(digits.count).hasPrefix(".")
    }

    /// A licence's texts, one after the other, or nil when a file is not in the bundle.
    static func text(_ license: License, bundle: Bundle = .main) -> String? {
        var parts: [String] = []
        for file in license.files {
            guard let url = bundle.url(forResource: file, withExtension: "txt"), let text = try? String(contentsOf: url, encoding: .utf8)
            else { return nil }
            parts.append(text.trimmingCharacters(in: .newlines))
        }
        return parts.joined(separator: "\n\n\n")
    }
}
