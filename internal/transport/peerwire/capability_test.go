package peerwire

import (
	"encoding/hex"
	"encoding/json"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
	"os"
	"testing"
)

type snapshotVector struct {
	Name   string `json:"name"`
	Packet string `json:"packet_hex"`
	Accept bool   `json:"accept"`
}

func TestCapabilitySnapshotVectors(t *testing.T) {
	const now = uint64(1700000000000)
	a, b := fixture("machine", 2), fixture("client", 1)
	codec := fixtureCodec()
	file := "../../../testdata/transport/wire/capability-snapshot.json"
	if os.Getenv("RA_WRITE_SNAPSHOT_VECTORS") == "1" {
		caps := &pb.MachineCapabilities{ImageUpload: true, MaxImagesPerMessage: 8, MaxUploadBytes: 32 << 20, MaxChunkBytes: 768 << 10, UploadFormats: []pb.ImageFormat{1, 2, 3, 4}, ApprovalAlways: true, ScheduledTasks: true, FileUpload: true, MaxFileUploadBytes: 64 << 20, MaxFilesPerMessage: 8, SharedFiles: true, MaxSharedFileBytes: 512 << 20, SharedFileChunkBytes: 768 << 10, MachineStatus: true}
		var rows []snapshotVector
		for _, name := range []string{"full", "expired", "over_60_seconds", "wrong_head", "wrong_account", "old_instance", "missing_capabilities"} {
			c := NewContext(a, b.Public(), Hash([]byte("snapshot-connection"))[:16], 0, 1, 14, now, 2, false)
			message := &pb.CapabilitySnapshot{CapabilityId: Hash([]byte("snapshot"))[:16], Capabilities: caps, ExpiresAtMs: int64(now + 60000)}
			sender, scope := a, *codec
			switch name {
			case "expired":
				message.ExpiresAtMs = int64(now)
			case "over_60_seconds":
				message.ExpiresAtMs = int64(now + 60001)
			case "wrong_head":
				scope.head = Hash([]byte("wrong"))
			case "wrong_account":
				scope.account = []byte("1123456789abcdef0123456789abcdef")
			case "old_instance":
				sender.Instance = Hash([]byte("old-instance"))[:16]
				c.SenderInstance = sender.Instance
			case "missing_capabilities":
				message.Capabilities = nil
			}
			body, err := proto.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := scope.Seal(sender, b.Public(), c, 14, body, Hash([]byte("frame"))[:16], Hash([]byte("context nonce"))[:12], Hash([]byte("body nonce"))[:12])
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, snapshotVector{name, hex.EncodeToString(raw), name == "full"})
		}
		raw, _ := json.MarshalIndent(rows, "", "  ")
		if err := os.WriteFile(file, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var rows []snapshotVector
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			raw, err := hex.DecodeString(row.Packet)
			if err != nil {
				t.Fatal(err)
			}
			o, err := codec.Open(raw, a.Public(), b, now)
			if err == nil {
				_, err = CapabilityExpiry(o, now)
			}
			if (err == nil) != row.Accept {
				t.Fatalf("accepted=%v, want %v", err == nil, row.Accept)
			}
			if row.Accept {
				var snapshot pb.CapabilitySnapshot
				if err = proto.Unmarshal(o.Payload, &snapshot); err != nil || !snapshot.GetCapabilities().GetFileUpload() || snapshot.GetCapabilities().GetUploadFormats()[3] != pb.ImageFormat_IMAGE_FORMAT_GIF {
					t.Fatal("full capabilities were not preserved")
				}
			}
		})
	}
}
