package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"google.golang.org/protobuf/encoding/protojson"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The opt-in Web helper holds only this controller's disposable identity.
// It invokes the production Remote forward/signature/RPC path in a JS runtime.
func saveWebBootstrap(root string, a *remoteclient.Authority, machine string) {
	states := make([][]byte, len(a.States))
	proofs := make([][]byte, len(a.States))
	for i, s := range a.States {
		states[i], _ = s.MarshalBinary()
		if a.Proofs[i] != nil {
			proofs[i], _ = a.Proofs[i].MarshalBinary()
		}
	}
	lease, _ := a.Lease.MarshalBinary()
	record := map[string]any{"version": 3, "account": a.Pin.Account, "domain": a.Pin.Domain[:], "genesis": a.Pin.Genesis[:], "member": a.Member.ID, "x": a.X, "states": states, "proofs": proofs, "lease": lease}
	b, _ := json.Marshal(map[string]any{"record": record, "server": a.Server, "machine": machine, "seed": a.Ed.Seed(), "token": a.Token})
	write(filepath.Join(root, "web-bootstrap.json"), b)
}
func (r *run) webForward(ctx context.Context, id, sid string, deadline time.Time, command *pb.Command) (*pb.CommandResult, error) {
	url := string(mustRead(filepath.Join(r.root, "web-client-url")))
	b, _ := protojson.Marshal(command)
	body, _ := json.Marshal(map[string]any{"id": id, "session": sid, "machine": r.machine, "deadline": deadline.UnixMilli(), "command": json.RawMessage(b)})
	req, err := http.NewRequestWithContext(ctx, "POST", url+"/forward", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Transport-Test", string(mustRead(filepath.Join(r.root, "control-token"))))
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 150 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, &remoteclient.CommandError{Outcome: outcome.Unknown, Code: "unknown_outcome", Message: "web transport interrupted"}
	}
	defer res.Body.Close()
	var reply struct {
		Result                               json.RawMessage `json:"result"`
		Code, Message, Outcome, ConflictKind string
		FromDaemon, ResultAvailable          bool
	}
	if err = json.NewDecoder(res.Body).Decode(&reply); err != nil {
		return nil, err
	}
	var result *pb.CommandResult
	if len(reply.Result) > 0 {
		result = &pb.CommandResult{}
		if err = protojson.Unmarshal(reply.Result, result); err != nil {
			return nil, err
		}
	}
	if reply.Outcome != "" {
		return result, &remoteclient.CommandError{Code: reply.Code, Message: reply.Message, FromDaemon: reply.FromDaemon, Outcome: outcome.Category(reply.Outcome), ConflictKind: reply.ConflictKind, ResultAvailable: reply.ResultAvailable}
	}
	if result == nil {
		return nil, errors.New("web driver returned no result")
	}
	return result, nil
}
func (r *run) hasWebClient() bool {
	_, err := os.Stat(filepath.Join(r.root, "web-client-url"))
	return err == nil
}
