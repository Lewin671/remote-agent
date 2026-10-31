/// Protocol version of the envelope format, Header.version.
public let envelopeVersion: UInt32 = 1

/// The protocol version this app speaks with daemons: Command.protocol_version
/// and MachineMeta.protocol_version. Peers of different versions do not work
/// together; keep it in step with Version in the Go package internal/protocol,
/// PROTOCOL_VERSION in web/src and in Android's core/Version.kt. It is not
/// envelopeVersion, which stored data keeps.
public let protocolVersion: UInt32 = 12
