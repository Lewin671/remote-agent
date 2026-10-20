import Foundation
import RAProto

/// Private, unlocked-device receipt. No new SAS or request is made during retry.
public struct RegistrationIntent: Codable, Sendable, CustomStringConvertible,
  CustomDebugStringConvertible, CustomReflectable
{
  let server: String
  let root: Data
  let seed: Data
  let deviceID: String
  let accountID: String
  let security: Data
  public let request: Data
  public var candidateStorageKey: String {
    get throws {
      let request = try PBRegisterDeviceRequest(serializedBytes: self.request)
      let approval = try PBPairApproval(serializedBytes: request.approval)
      return PairingIdentity.storageKey(server: server, pairingID: approval.pairingID)
    }
  }
  /// Derived from the existing exact request: no receipt-format change.
  public var publicProfile: AccountPublicProfile {
    get throws {
      let request = try PBRegisterDeviceRequest(serializedBytes: self.request)
      let registration = try PBDeviceRegistration(serializedBytes: request.registration)
      let record = try DeviceSecurity.Record.parse(security)
      try DeviceSecurity.check(record.pin.account == accountID, .untrusted)
      return try AccountPublicProfile(publicKey: registration.accountPublicKey, pin: record.pin)
    }
  }
  public var targetServer: String { server }
  public var description: String { "RegistrationIntent" }
  public var debugDescription: String { description }
  public var customMirror: Mirror { Mirror(self, children: [:]) }
  public init(account: Account, record: DeviceSecurity.Record, request: PBRegisterDeviceRequest)
    throws
  {
    server = account.server
    root = account.root
    seed = account.deviceSeed
    deviceID = account.deviceId
    accountID = account.accountId
    security = try record.bytes()
    self.request = try request.serializedData()
  }
  public func matchesBootstrap(account: Account, record: DeviceSecurity.Record) throws -> Bool {
    try server == account.server && root == account.root && seed == account.deviceSeed
      && deviceID == account.deviceId && accountID == account.accountId
      && security == record.bytes()
  }
  public func resume(
    http: HTTP, persist: @Sendable (Account, DeviceSecurity.Record) async throws -> Void
  ) async throws -> Account {
    let account = Account(
      server: server, root: root, deviceSeed: seed, deviceId: deviceID, accountId: accountID)
    let record = try DeviceSecurity.Record.parse(security)
    try DeviceSecurity.check(
      record.memberID == deviceID && record.pin.account == accountID
        && deviceId(publicKey: SigningKey(seed: seed).publicKey) == deviceID, .storage)
    _ = try publicProfile
    try await persist(account, record)
    let res = try await Api(server: server, http: http).auth.registerDevice(
      request: PBRegisterDeviceRequest(serializedBytes: request)
    ).value()
    try DeviceSecurity.check(res.deviceID == deviceID, .untrusted)
    return account
  }
}
