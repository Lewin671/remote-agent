// The relay protocol version is checked by remote-agent-server at its entry
// points (RegisterDevice, Login, PeekPairing, JoinPairing). Keep it equal to
// Version in Go's internal/relayproto, clientcore, Android and iOS.
export const RELAY_PROTOCOL_VERSION = 17;

// The peer protocol version this page, the daemon and the other clients speak
// to one another (MachineMeta, Command, peerwire Context). Peers of different
// versions do not work together: each refuses the other and says to update,
// instead of falling back feature by feature. Keep it equal to PeerVersion in
// Go's internal/protocol, clientcore, Android and iOS. It is not the envelope
// version (VERSION in crypto.ts), which stored data keeps.
export const PEER_PROTOCOL_VERSION = 17;
