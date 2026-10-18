// The version of the protocol this page, the daemon and the other clients
// speak to one another. Peers of different versions do not work together:
// each refuses the other and says to update, instead of falling back
// feature by feature. Keep it equal to Version in Go's internal/protocol
// and to Android's PROTOCOL_VERSION. It is not the envelope version
// (VERSION in crypto.ts), which stored data keeps.
export const PROTOCOL_VERSION = 8;
