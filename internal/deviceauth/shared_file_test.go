package deviceauth

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// GetSharedFileChunk skips the accepted log as a read-only command, but
// still only runs for a Client sender: a Machine can neither seal one nor
// get a forged one accepted.
func TestSharedFileChunkOnlyFromClients(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, filepath.Join(t.TempDir(), "machine.db"))
	defer machine.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	body, err := proto.Marshal(&pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: "session", FileId: "0123456789abcdef0123456789abcdef"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := uint64(time.Now().UnixMilli())
	h := Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Session: "session", Request: "0123456789abcdef", Deadline: now + 60_000}
	calls := 0
	fn := func(Header, []byte) ([]byte, error) { calls++; return []byte("block"), nil }
	for i := 0; i < 2; i++ {
		w, err := client.Prepare(h, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := machine.Accept(w, nil, now, fn); err != nil || string(out) != "block" {
			t.Fatalf("client: %q %v", out, err)
		}
	}
	// Read-only: a repeat runs again instead of replaying a stored block.
	if calls != 2 || len(machine.data.Accepted) != 0 {
		t.Fatalf("calls=%d accepted=%d", calls, len(machine.data.Accepted))
	}
	if _, err := machine.Prepare(h, nil, body); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("machine sealed a command: %v", err)
	}
	head := chainHead(machine.data)
	sh, _ := head.Hash()
	forged := h
	forged.Account, forged.Domain, forged.Epoch, forged.State, forged.ContentEpoch, forged.Sender = head.Core.Account, head.Core.Domain, head.Core.Epoch, sh, head.Core.ContentEpoch, c.machine.ID
	w, err := Seal(machine.data.Secret, nil, forged, body, c.machineEd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Accept(w, nil, now, fn); err == nil || calls != 2 {
		t.Fatalf("machine-signed chunk request ran: %v calls=%d", err, calls)
	}
}

// A shared file's block is sealed as is: same-length blocks give
// same-length ciphertexts whatever their content.
func TestPrepareUncompressed(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, filepath.Join(t.TempDir(), "machine.db"))
	defer machine.Close()
	h := Header{Kind: Result.kind(), Purpose: Result, Machine: c.machine.ID, Session: "session", Request: "request-1234567890"}
	zeros := bytes.Repeat([]byte{0}, 4096)
	random := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123456789"), 114)[:4096]
	var sizes []int
	for _, body := range [][]byte{zeros, random} {
		w, err := machine.PrepareUncompressed(h, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		got, plain, err := openCipher(machine.data.Secret, nil, w)
		if err != nil || got.Compression != 0 || !bytes.Equal(plain, body) {
			t.Fatalf("compression=%d %v", got.Compression, err)
		}
		sizes = append(sizes, len(w.Cipher))
	}
	if sizes[0] != sizes[1] {
		t.Fatalf("ciphertext lengths differ: %v", sizes)
	}
	if w, _ := machine.Prepare(h, nil, zeros); len(w.Cipher) >= sizes[0] {
		t.Fatal("Prepare no longer compresses results")
	}
}
