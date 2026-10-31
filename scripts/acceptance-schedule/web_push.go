package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"google.golang.org/protobuf/proto"
)

// The local test transport removes Web Push's outer encryption. The captured
// JSON must still carry a signed, end-to-end encrypted NotifyContent envelope.
func verifyWebPush(ctx context.Context, client *remoteclient.Client, phone *deviceauth.Endpoint, path string) {
	var raw []byte
	wait(ctx, "local Web Push transport captures scheduled ciphertext", func() bool {
		var err error
		raw, err = os.ReadFile(path)
		return err == nil && len(raw) > 0
	})
	var data map[string]string
	must(json.Unmarshal(raw, &data))
	if len(raw) > 3993 || data["title"] != "remote-agent" || data["body"] != "定时任务有新结果" {
		panic("invalid generic Web Push payload")
	}
	b, err := base64.StdEncoding.DecodeString(data["content"])
	must(err)
	var env pb.Envelope
	must(proto.Unmarshal(b, &env))
	must(client.Sync(ctx))
	w, err := deviceauth.FromEnvelope(&env)
	must(err)
	h, p, err := phone.Open(w, nil, true, uint64(time.Now().UnixMilli()))
	must(err)
	p, err = deviceauth.Unpack(h, p)
	must(err)
	var c pb.NotifyContent
	must(proto.Unmarshal(p, &c))
	if h.Purpose != deviceauth.Notify || c.Kind != pb.NotifyKind_NOTIFY_KIND_SCHEDULED ||
		c.Title != "Completion notification acceptance" || c.Text != "Completion fallback verified." ||
		c.SessionId != data["session"] || c.SourceEventSeq == 0 {
		panic("Web Push ciphertext lost authenticated task title or summary")
	}
	fmt.Println("PASS real daemon -> local relay -> Web Push transport -> verified NotifyContent task title and summary")
}
