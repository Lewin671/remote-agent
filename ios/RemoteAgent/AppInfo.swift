// What the build says of itself. The version is the product's, shared with
// the other clients (version.properties at the repository root), and the
// commit is the one the build was made from; scripts/version.sh writes both
// into the Info.plist on every build.
import Foundation

enum AppInfo {
    /// The product version, such as 0.30.1.
    static var version: String { string("CFBundleShortVersionString") ?? "0.0.0" }

    /// The source commit, shortened, with `-dirty` when the tree had changes; "unknown" outside a checkout.
    static var commit: String { string("RASourceCommit") ?? "unknown" }

    /// A debug build: its own bundle ID (`.dev`) and name, installed next to the real app.
    static var isDev: Bool { Bundle.main.bundleIdentifier?.hasSuffix(".dev") ?? false }

    /// As a settings row shows it: `0.30.1 (dc305a9b7ab6)`, with `dev` in a debug build.
    static var versionLine: String { "\(version)\(isDev ? "-dev" : "") (\(commit))" }

    private static func string(_ key: String) -> String? {
        (Bundle.main.object(forInfoDictionaryKey: key) as? String).flatMap { $0.isEmpty ? nil : $0 }
    }
}
