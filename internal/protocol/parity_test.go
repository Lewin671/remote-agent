package protocol_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/relayproto"
)

func parseVersionConstant(t *testing.T, relPath string, pattern string) uint32 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", relPath))
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	re := regexp.MustCompile(pattern)
	m := re.FindSubmatch(raw)
	if len(m) != 2 {
		t.Fatalf("%s: pattern %q did not match", relPath, pattern)
	}
	n, err := strconv.ParseUint(string(m[1]), 10, 32)
	if err != nil {
		t.Fatalf("%s: parse %q: %v", relPath, m[1], err)
	}
	return uint32(n)
}

// TestProtocolVersionsAgreeAcrossAllCopies verifies that both the relay
// protocol version and the peer protocol version match across Go, clientcore,
// web, Android and iOS.
func TestProtocolVersionsAgreeAcrossAllCopies(t *testing.T) {
	peerChecks := []struct {
		name    string
		path    string
		pattern string
	}{
		{"go", "internal/protocol/protocol.go", `(?m)^const PeerVersion = (\d+)$`},
		{"clientcore", "clientcore/sync/types.go", `(?m)^\s*PeerVersion\s+uint32\s*=\s*(\d+)$`},
		{"web", "web/src/protocol.ts", `(?m)^export const PEER_PROTOCOL_VERSION = (\d+);$`},
		{"android", "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Version.kt", `(?m)^const val PEER_PROTOCOL_VERSION = (\d+)$`},
		{"ios", "ios/RemoteAgentCore/Sources/RACore/Version.swift", `(?m)^public let peerProtocolVersion: UInt32 = (\d+)$`},
	}
	for _, tc := range peerChecks {
		got := parseVersionConstant(t, tc.path, tc.pattern)
		if got != protocol.PeerVersion {
			t.Errorf("peer protocol version mismatch in %s (%s): got %d, want %d", tc.name, tc.path, got, protocol.PeerVersion)
		}
	}

	relayChecks := []struct {
		name    string
		path    string
		pattern string
	}{
		{"go", "internal/relayproto/relayproto.go", `(?m)^const Version = (\d+)$`},
		{"clientcore", "clientcore/sync/types.go", `(?m)^\s*RelayVersion\s+uint32\s*=\s*(\d+)$`},
		{"web", "web/src/protocol.ts", `(?m)^export const RELAY_PROTOCOL_VERSION = (\d+);$`},
		{"android", "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Version.kt", `(?m)^const val RELAY_PROTOCOL_VERSION = (\d+)$`},
		{"ios", "ios/RemoteAgentCore/Sources/RACore/Version.swift", `(?m)^public let relayProtocolVersion: UInt32 = (\d+)$`},
	}
	for _, tc := range relayChecks {
		got := parseVersionConstant(t, tc.path, tc.pattern)
		if got != relayproto.Version {
			t.Errorf("relay protocol version mismatch in %s (%s): got %d, want %d", tc.name, tc.path, got, relayproto.Version)
		}
	}
}
