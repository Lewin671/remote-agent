import Connect
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
  /// The live pairing must consume its approval and replace the registration
  /// even when a re-pairing retains an identity that can already log in.
  public func register(
    http: HTTP, persist: @Sendable (Account, DeviceSecurity.Record) async throws -> Void
  ) async throws -> Account {
    try await complete(http: http, persist: persist, loginFirst: false)
  }
  /// Only restart recovery may treat a successful login as completed registration.
  public func resume(
    http: HTTP, persist: @Sendable (Account, DeviceSecurity.Record) async throws -> Void
  ) async throws -> Account {
    try await complete(http: http, persist: persist, loginFirst: true)
  }
  private func complete(
    http: HTTP, persist: @Sendable (Account, DeviceSecurity.Record) async throws -> Void, loginFirst: Bool
  ) async throws -> Account {
    let account = Account(
      server: server, root: root, deviceSeed: seed, deviceId: deviceID, accountId: accountID)
    let record = try DeviceSecurity.Record.parse(security)
    try DeviceSecurity.check(
      record.memberID == deviceID && record.pin.account == accountID
        && deviceId(publicKey: SigningKey(seed: seed).publicKey) == deviceID, .storage)
    _ = try publicProfile
    let api = try Api(server: server, http: http)
    // A lost registration/receipt response must not replay an expired admission
    // or replace a newer security snapshot after a subsequent handoff.
    var registered = false
    if loginFirst {
      do {
        _ = try await login(api: api, account: account)
        registered = true
      } catch let error as ConnectError where [.permissionDenied, .notFound, .unauthenticated].contains(error.code) {
        // Only a definite authentication refusal permits the original registration.
      }
    }
    // Registration may already exist during a re-pairing whose fresh X was
    // not installed before interruption. The vault merges this bootstrap
    // idempotently and preserves any newer verified chain and time floor.
    try await persist(account, record)
    if registered { return account }
    let res = try await api.auth.registerDevice(
      request: PBRegisterDeviceRequest(serializedBytes: request)
    ).value()
    try DeviceSecurity.check(res.deviceID == deviceID, .untrusted)
    return account
  }
}
