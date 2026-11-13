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
public func machineDiskNotice(_ c: PBNotifyContent, locale: String = MessageLocale.current) -> MachineDiskNotice? {
    guard c.kind == .machineDisk, c.sessionID.isEmpty, !c.machineID.isEmpty, !c.machineName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
          c.title.isEmpty, c.text.isEmpty, !c.hasApproval, c.hasDiskStatus else { return nil }
    let disk=c.diskStatus, critical=diskPressureCritical(c.diskStatus)
    guard (critical || disk.pressure == .warning), disk.availableBytes < (critical ? 2_000_000_000 : 10_000_000_000) else { return nil }
    let formatter=NumberFormatter();formatter.locale=Locale(identifier:locale);formatter.minimumFractionDigits=1;formatter.maximumFractionDigits=1
    let number=formatter.string(from:NSNumber(value:Double(disk.availableBytes)/1e9))!
    let size=L10n.machineStatusSizeGb(size:number,locale:locale)
    return MachineDiskNotice(machineID:c.machineID,tag:c.machineID+"/NOTIFY_KIND_MACHINE_DISK",
        title:critical ? L10n.machineStatusNotifyCritical(computer:c.machineName,size:size,locale:locale) : L10n.machineStatusNotifyWarning(computer:c.machineName,size:size,locale:locale),
        body:critical ? L10n.machineStatusNotifyCriticalBody(locale:locale) : L10n.machineStatusNotifyWarningBody(locale:locale))
}
private func diskPressureCritical(_ disk: PBDiskStatus) -> Bool { disk.pressure == .critical }
/// iOS currently has no OS push delivery adapter. This RACore receiver is the
/// verified envelope -> localized notification + machine destination boundary.
public func receiveMachineDiskNotification(envelope: Data, notifyRecord: Data, locale: String = MessageLocale.current) throws -> MachineDiskNotice? {
    let c=try DeviceSecurity.NotifyRecord.parse(notifyRecord).open(PBEnvelope(serializedBytes:envelope))
    return machineDiskNotice(c,locale:locale)
}
extension Remote {
    public func setMachinePushTarget(type: PBPushType, target: String, kinds: [PBNotifyKind]) async throws -> PBSetPushTargetResponse {
        let headers=Api.headers(token:try await currentToken())
        let advertised = target.isEmpty ? false : try await api.client.getPushKey(request:PBGetPushKeyRequest(),headers:headers).value().machineDiskNotifications
        var request=PBSetPushTargetRequest();request.type=type;request.target=target;request.kinds=target.isEmpty ? [] : machineNotificationKinds(kinds,advertised:advertised)
        return try await api.client.setPushTarget(request:request,headers:headers).value()
    }
}
