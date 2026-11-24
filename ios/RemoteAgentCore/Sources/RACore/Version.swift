/// Protocol version of the envelope format, Header.version.
public let envelopeVersion: UInt32 = 1

/// The relay protocol version checked by remote-agent-server at its entry points
/// (RegisterDevice, Login, PeekPairing, JoinPairing). Keep it in step with
/// Version in Go's internal/relayproto, clientcore, web/src and Android.
public let relayProtocolVersion: UInt32 = 17

/// The peer protocol version this app speaks with daemons: Command.protocol_version,
/// MachineMeta.protocol_version and peerwire Context.protocol. Peers of different
/// versions do not work together; keep it in step with PeerVersion in Go's
/// internal/protocol, clientcore, web/src and Android. It is not envelopeVersion,
/// which stored data keeps.
public let peerProtocolVersion: UInt32 = 17
