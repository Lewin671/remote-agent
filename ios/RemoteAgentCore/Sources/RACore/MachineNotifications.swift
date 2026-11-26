import Foundation
import RAProto

public func machineNotificationKinds(_ kinds: [PBNotifyKind], advertised: Bool) -> [PBNotifyKind] {
    kinds.filter { $0 != .machineDisk } + (advertised ? [.machineDisk] : [])
}
public struct MachineDiskNotice: Equatable, Sendable {
    public let machineID: String
    public let tag: String
    public let title: String
    public let body: String
}
/// iOS currently has no OS push delivery adapter. This RACore receiver is the
/// verified envelope -> Core-validated `SecurityNotifyDisplay` -> localized notification + machine destination boundary.
public func receiveMachineDiskNotification(
    envelope: Data,
    notifyRecord: Data,
    machineID: String,
    tag: String,
    showContent: Bool,
    locked: Bool,
    nowMs: Int64 = Int64(Date().timeIntervalSince1970 * 1000),
    locale: String = MessageLocale.current
) throws -> MachineDiskNotice? {
    guard let display = try openNotifyDisplayViaCore(
        notifyBlob: notifyRecord,
        rawEnvelope: envelope,
        expectedKind: .machineDisk,
        nowMs: nowMs,
        machineId: machineID,
        tag: tag
    ), display.valid else {
        return nil
    }
    let detailed = !locked && (showContent || display.showDetailedByDefault)
    let title = detailed ? display.title.rendered(locale: locale) : ""
    let body = detailed ? display.body.rendered(locale: locale) : display.fullGenericBody.rendered(locale: locale)
    return MachineDiskNotice(machineID: display.machineID, tag: display.tag, title: title, body: body)
}
