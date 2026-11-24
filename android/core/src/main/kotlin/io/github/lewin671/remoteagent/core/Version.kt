package io.github.lewin671.remoteagent.core

/** Protocol version of the envelope format, Header.version. */
const val ENVELOPE_VERSION = 1

/**
 * The relay protocol version checked by remote-agent-server at its entry points
 * (RegisterDevice, Login, PeekPairing, JoinPairing). Keep it in step with
 * Version in Go's internal/relayproto, clientcore, web/src and iOS.
 */
const val RELAY_PROTOCOL_VERSION = 17

/**
 * The peer protocol version this app speaks with daemons: Command.protocol_version,
 * MachineMeta.protocol_version and peerwire Context.protocol. Peers of different
 * versions do not work together; keep it in step with PeerVersion in Go's
 * internal/protocol, clientcore, web/src and iOS. It is not ENVELOPE_VERSION,
 * which stored data keeps.
 */
const val PEER_PROTOCOL_VERSION = 18
