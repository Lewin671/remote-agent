// The account's project preferences (architecture §8, 项目列表偏好):
// object ID and decrypted snapshot retained for transitional `RemoteState`.
import Foundation
import RAProto

public let projectPrefsID = "project-prefs"

/// The preferences as the account has them, decrypted, at a revision.
public struct PrefsObject: Equatable, Sendable {
    public var revision: UInt64
    public var prefs: PBProjectPrefs

    public init(revision: UInt64, prefs: PBProjectPrefs) {
        self.revision = revision
        self.prefs = prefs
    }

    public static let empty: PrefsObject = {
        var p = PBProjectPrefs()
        p.schema = 1
        return PrefsObject(revision: 0, prefs: p)
    }()
}
