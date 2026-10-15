package io.github.lewin671.remoteagent.core

/** Protocol version of the envelope format, Header.version. */
const val ENVELOPE_VERSION = 1

/**
 * The protocol version this app speaks with daemons: Command.protocol_version
 * and MachineMeta.protocol_version. Peers of different versions do not work
 * together; keep it in step with Version in the Go package internal/protocol
 * and PROTOCOL_VERSION in web/src. It is not ENVELOPE_VERSION, which stored
 * data keeps.
 */
const val PROTOCOL_VERSION = 6
