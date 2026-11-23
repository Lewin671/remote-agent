// Project and computer display names retained for shell fallbacks.
import Foundation
import RAProto

/// A project's name: the last folder of its path.
public func projectName(_ key: String) -> String {
    key.split(ascii: UInt8(ascii: "/")).last { !$0.isEmpty } ?? L10n.projectsUnknown()
}

/// A computer's name: its host name without .local, else the start of its ID.
public func machineName(_ m: MachineView?, id: String = "") -> String {
    if let host = m?.meta?.hostname.removingSuffix(".local"), !host.isEmpty { return host }
    if let m { return m.id.jvmTake(8) }
    return id.jvmTake(8).ifEmpty(L10n.commonUnknownComputer())
}
