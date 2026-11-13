package relay

import (
	"context"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"os"
	"path/filepath"
	"testing"
)

func TestV3TrustAnchorCannotBeBypassed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REMOTE_AGENT_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "security-v3.db"), []byte("old-store"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSecurityStorage(); err != deviceauth.ErrStorageVersion {
		t.Fatal(err)
	}
	if err := ResumeRegistration(context.Background()); err != deviceauth.ErrStorageVersion {
		t.Fatal(err)
	}
	if err := bootstrapAdmission(pairAdmission{}); err != deviceauth.ErrStorageVersion {
		t.Fatal(err)
	}
	if _, err := JoinByPairing(context.Background(), PairLink{}, nil); err != deviceauth.ErrStorageVersion {
		t.Fatal(err)
	}
	if _, err := os.Stat(securityPath()); !os.IsNotExist(err) {
		t.Fatal("bootstrap replaced trust anchor")
	}
	// A successfully published v4 takes precedence while the source is retained
	// for offline recovery. No old endpoint is ever opened by production code.
	if err := os.WriteFile(securityPath(), []byte("new-store"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSecurityStorage(); err != nil {
		t.Fatal(err)
	}
}
