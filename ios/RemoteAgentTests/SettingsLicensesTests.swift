import Foundation
import Testing

@testable import RemoteAgent

/// The licences the app shows against what it is built with: a package
/// pinned in either Package.resolved without a licence text fails here, so
/// a new dependency cannot ship without one.
@Suite struct SettingsLicensesTests {
    /// The ios/ directory of the checkout the tests were built from; the simulator reads the Mac's files.
    private static let ios = URL(filePath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    private static let resolved = [
        "RemoteAgent.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved",
        "RemoteAgentCore/Package.resolved",
    ]

    private struct Resolved: Decodable {
        struct Pin: Decodable { let identity: String }
        let pins: [Pin]
    }

    private static func pins(_ path: String) throws -> [String] {
        let data = try Data(contentsOf: ios.appending(path: path))
        return try JSONDecoder().decode(Resolved.self, from: data).pins.map(\.identity)
    }

    @Test(arguments: resolved)
    func everyPinnedPackageHasALicence(path: String) throws {
        let pinned = try Self.pins(path)
        #expect(!pinned.isEmpty)
        let listed = Set(Licenses.all.compactMap(\.package))
        for identity in pinned {
            #expect(listed.contains(identity), "\(identity) is pinned in \(path) and has no entry in Licenses.all")
        }
    }

    @Test func noLicenceIsListedForAPackageThatIsGone() throws {
        let pinned = Set(try Self.resolved.flatMap(Self.pins))
        for license in Licenses.all {
            if let package = license.package { #expect(pinned.contains(package), "\(package) is listed and no longer pinned") }
        }
    }

    @Test func everyListedLicenceHasItsTextInTheSourcesAndInTheApp() throws {
        let directory = Self.ios.appending(path: "RemoteAgent/Resources/Licenses")
        var files = Set<String>()
        for license in Licenses.all {
            for file in license.files {
                files.insert(file + ".txt")
                let text = try String(contentsOf: directory.appending(path: file + ".txt"), encoding: .utf8)
                #expect(text.count > 500, "\(file) is not a licence text")
            }
            // What the page shows comes from the bundle.
            let shown = try #require(Licenses.text(license), "\(license.name) is not in the app bundle")
            #expect(shown.count > 500)
        }
        // And nothing lies there that the page does not show.
        let present = Set(try FileManager.default.contentsOfDirectory(atPath: directory.path).filter { $0.hasSuffix(".txt") })
        #expect(present == files)
        #expect(Set(Licenses.all.map(\.id)).count == Licenses.all.count)
    }

    @Test func theShownTextKeepsEveryWordAndOnlyUndoesHandWrapping() throws {
        let wrapped = """
                                 Apache License
                           Version 2.0, January 2004

           1. Definitions.

              "License" shall mean the terms and conditions for use, reproduction,
              and distribution as defined by Sections 1 through 9 of this document.

        Copyright (c) 2019-2022 Miguel de Icaza (https://github.com/migueldeicaza)
        Copyright (c) 2017-2019, The xterm.js authors (https://github.com/xtermjs/xterm.js)

          * HOMEPAGE: a line that is long enough to have been wrapped by hand, surely
            * https://example.com/a
        ANY KIND,
        EXPRESS OR IMPLIED
        """
        let shown = Licenses.display(wrapped)
        #expect(shown.contains("Apache License\nVersion 2.0, January 2004\n\n1. Definitions.\n\n"))
        #expect(shown.contains("for use, reproduction, and distribution as defined"))
        // Each holder keeps a line, list items stay items, and short lines are left as written.
        #expect(shown.contains("migueldeicaza)\nCopyright (c) 2017-2019"))
        #expect(shown.contains("surely\n* https://example.com/a"))
        #expect(shown.contains("ANY KIND,\nEXPRESS OR IMPLIED"))
        // Only white space differs, in every bundled text.
        func words(_ s: String) -> [Substring] { s.split(whereSeparator: \.isWhitespace) }
        #expect(words(shown) == words(wrapped))
        for license in Licenses.all {
            let text = try #require(Licenses.text(license))
            #expect(words(Licenses.display(text)) == words(text), "\(license.name)")
        }
    }

    @Test func theTextsAreTheLicencesTheyAreListedAs() throws {
        for license in Licenses.all {
            let text = try #require(Licenses.text(license))
            switch license.kind {
            case "MIT": #expect(text.contains("Permission is hereby granted, free of charge"))
            case "Apache-2.0": #expect(text.contains("Apache License") && text.contains("Version 2.0"))
            case "ISC": #expect(text.contains("ISC License"))
            default: Issue.record("\(license.name) has a licence the test does not know: \(license.kind)")
            }
        }
        // Apple's NIO projects ask that their NOTICE travels with them.
        let nio = try #require(Licenses.all.first { $0.package == "swift-nio" })
        #expect(nio.files.count == 2)
        #expect(try #require(Licenses.text(nio)).contains("SwiftNIO"))
    }
}
