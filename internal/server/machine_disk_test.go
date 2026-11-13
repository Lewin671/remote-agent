package server

import (
	"encoding/json"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestMachineDiskPushKindAndCiphertext(t *testing.T) {
	if !validNotifyKinds([]pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK}) {
		t.Fatal("machine kind refused")
	}
	payload := webPushPayload("", pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, []byte("sealed-fixture"), "public-event")
	var m map[string]string
	if e := json.Unmarshal(payload, &m); e != nil {
		t.Fatal(e)
	}
	if m["content"] == "" || m["session"] != "" {
		t.Fatal("sessionless ciphertext missing")
	}
	if m["body"] == "sealed-fixture" {
		t.Fatal("plaintext leaked")
	}
}
