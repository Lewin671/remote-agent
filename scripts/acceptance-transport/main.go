// Explicit opt-in real Claude Code/Codex regression controller. Its loopback
// control interface is a test driver, never a relay or a product API.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/updater"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func write(p string, b []byte) { must(os.WriteFile(p, b, 0600)) }
func local(p string) bool {
	resolved, e := filepath.EvalSymlinks(filepath.Clean(p))
	return e == nil && filepath.IsAbs(p) && (strings.HasPrefix(resolved, "/tmp/") || strings.HasPrefix(resolved, "/private/tmp/"))
}
func guarded(cmd *exec.Cmd, env map[string]string) {
	for _, k := range []string{"RA_DEV_DIR", "REMOTE_AGENT_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if !local(env[k]) {
			panic("non-isolated " + k)
		}
	}
	if env["REMOTE_AGENT_TMUX_SOCKET"] == "" {
		panic("missing task tmux socket")
	}
	fmt.Printf("ISOLATION verified REMOTE_AGENT_HOME=%s exec=%s\n", env["REMOTE_AGENT_HOME"], filepath.Base(cmd.Path))
	cmd.Env = nil
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
}
func start(cmd *exec.Cmd, env map[string]string, log string) func() {
	f, e := os.OpenFile(log, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	must(e)
	cmd.Stdout = f
	cmd.Stderr = f
	guarded(cmd, env)
	must(cmd.Start())
	return func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		f.Close()
	}
}
func poll(ctx context.Context, what string, fn func() bool) error {
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		if fn() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, ctx.Err())
		case <-timer.C:
		}
	}
}

type bridge struct {
	target  *url.URL
	mu      sync.Mutex
	streams map[*websocket.Conn]bool
	ready   atomic.Int64
	proxy   *httputil.ReverseProxy
}

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/stream" {
		b.proxy.ServeHTTP(w, r)
		return
	}
	front, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if e != nil {
		return
	}
	defer front.CloseNow()
	back, _, e := websocket.Dial(r.Context(), "ws://"+b.target.Host+"/v1/stream", nil)
	if e != nil {
		return
	}
	defer back.CloseNow()
	front.SetReadLimit(4 << 20)
	back.SetReadLimit(4 << 20)
	b.mu.Lock()
	b.streams[front] = true
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.streams, front); b.mu.Unlock() }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			t, p, e := front.Read(ctx)
			if e != nil {
				return
			}
			if back.Write(ctx, t, p) != nil {
				return
			}
		}
	}()
	for {
		t, p, e := back.Read(ctx)
		if e != nil {
			return
		}
		if front.Write(ctx, t, p) != nil {
			return
		}
		var f pb.ServerFrame
		if proto.Unmarshal(p, &f) == nil && f.GetReady() != nil {
			b.ready.Add(1)
		}
	}
}
func (b *bridge) drop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.streams {
		c.CloseNow()
	}
}

type run struct {
	root, machine, socket, project, binary string
	env                                    map[string]string
	client                                 *remoteclient.Client
	ctx                                    context.Context
	bridge                                 *bridge
	mu                                     sync.Mutex
	stopDaemon                             func()
	native                                 map[string]string
	results                                *os.File
}

func (r *run) restart() {
	if r.stopDaemon != nil {
		r.stopDaemon()
	}
	r.stopDaemon = start(exec.Command(r.binary, "daemon", "--claude", r.env["RA_CLAUDE_BIN"], "--codex", r.env["RA_CODEX_BIN"], "--agy", "/usr/bin/false", "--approval-timeout", "240s"), r.env, filepath.Join(r.root, "daemon.log"))
}
func (r *run) find(sid string) (remoteclient.Session, error) {
	rows, e := r.client.Sessions(r.ctx)
	if e != nil {
		return remoteclient.Session{}, e
	}
	for _, s := range rows {
		if s.ID == sid {
			return s, nil
		}
	}
	return remoteclient.Session{}, errors.New("test session missing")
}
func (r *run) adapter(sid string) agent.Adapter {
	if r.native[sid] == "claude" {
		return claude.New("", r.env["CLAUDE_CONFIG_DIR"])
	}
	return codex.New("", r.env["CODEX_HOME"], "")
}
func (r *run) screen(sid string) string {
	if len(sid) < 8 {
		return ""
	}
	c := exec.Command("tmux", "-L", r.socket, "capture-pane", "-p", "-e", "-t", "ra-"+sid[:8]+":")
	raw, _ := c.Output()
	return string(raw)
}
func (r *run) ready(ctx context.Context, sid string) error {
	a := r.adapter(sid)
	pending := []string{}
	return poll(ctx, "native composer", func() bool {
		raw := r.screen(sid)
		scr := a.Analyze(raw)
		plain := agent.StripANSI(raw)
		var key string
		if len(pending) > 0 {
			key = pending[0]
			pending = pending[1:]
		} else if strings.Contains(plain, "Make auto mode your default permission mode?") {
			key = "Down"
			if strings.Contains(plain, "❯ No, keep manual mode") {
				key = "Enter"
			}
		} else if check, ok := a.(agent.Checkable); ok {
			pending = check.StartupKeys(scr.Popup)
		}
		if key != "" {
			_ = exec.Command("tmux", "-L", r.socket, "send-keys", "-t", "ra-"+sid[:8]+":", key).Run()
			return false
		}
		return scr.ComposerReady && !scr.Busy && scr.Popup == ""
	})
}
func (r *run) observe(ctx context.Context, sid string, cmd *pb.Command) error {
	switch cmd.Kind.(type) {
	case *pb.Command_ResolveApproval, *pb.Command_AnswerQuestion, *pb.Command_AnswerPlan:
	default:
		return nil
	}
	view, e := r.find(sid)
	if e != nil {
		return e
	}
	result, e := r.client.Do(ctx, view, &pb.Command{Kind: &pb.Command_GetLiveSnapshot{GetLiveSnapshot: &pb.GetLiveSnapshotRequest{SessionId: sid}}})
	if e != nil {
		return e
	}
	snapshot := result.GetLiveSnapshot()
	if snapshot == nil {
		return errors.New("missing live observation")
	}
	switch k := cmd.Kind.(type) {
	case *pb.Command_ResolveApproval:
		for _, ev := range snapshot.Actions {
			if a := ev.GetApprovalRequested(); a != nil && a.ApprovalId == k.ResolveApproval.ApprovalId {
				k.ResolveApproval.ObservedEventSeq = ev.Seq
				return nil
			}
		}
		return errors.New("approval not in current observation")
	case *pb.Command_AnswerQuestion:
		for _, ev := range snapshot.Actions {
			if q := ev.GetQuestionAsked(); q != nil && q.QuestionId == k.AnswerQuestion.QuestionId {
				k.AnswerQuestion.ObservedEventSeq = ev.Seq
				return nil
			}
		}
		return errors.New("question not in current observation")
	case *pb.Command_AnswerPlan:
		k.AnswerPlan.ObservedEventSeq = snapshot.PlanEventSeq
		k.AnswerPlan.ObservedScreenHash = snapshot.PlanScreenHash
	}
	return nil
}

type request struct {
	Label, CLI, Session string
	Command             json.RawMessage
	Cut                 bool
	After               uint64
}
type response struct {
	Code        string          `json:"code"`
	Unknown     bool            `json:"unknown"`
	Signed      bool            `json:"signed"`
	Error       string          `json:"error,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
	CutVerified bool            `json:"cutVerified,omitempty"`
	Calls       int             `json:"calls,omitempty"`
	Session     string          `json:"session,omitempty"`
}

func (r *run) rpc(ctx context.Context, q request) response {
	cmd := &pb.Command{}
	if e := protojson.Unmarshal(q.Command, cmd); e != nil {
		return response{Error: e.Error(), Code: "driver_invalid"}
	}
	if e := r.observe(ctx, q.Session, cmd); e != nil {
		return response{Error: e.Error(), Code: "driver_observation"}
	}
	id := remoteclient.NewRequestID()
	if send := cmd.GetSend(); send != nil && send.RequestId == "" {
		send.RequestId = id
	}
	env, e := r.client.Seal(r.machine, q.Session, id, time.Now().Add(3*time.Minute), cmd)
	if e != nil {
		return response{Error: e.Error(), Code: "driver_seal"}
	}
	var result *pb.CommandResult
	verified := false
	if q.Cut {
		prefix := filepath.Join(r.root, "trace", id)
		write(prefix+".hold", []byte("hold"))
		defer os.Remove(prefix + ".hold")
		first := make(chan error, 1)
		go func() { _, err := r.client.Forward(ctx, r.machine, env); first <- err }()
		if e = poll(ctx, "admitted callback", func() bool { _, err := os.Stat(prefix + ".entered"); return err == nil }); e != nil {
			return response{Error: e.Error(), Code: "driver_barrier"}
		}
		before := r.bridge.ready.Load()
		r.bridge.drop()
		select {
		case err := <-first:
			if !remoteclient.Uncertain(err) {
				return response{Error: "broken path was not unknown", Code: "driver_cut"}
			}
		case <-ctx.Done():
			return response{Error: ctx.Err().Error(), Code: "driver_cut"}
		}
		if e = poll(ctx, "replacement authenticated Ready", func() bool { return r.bridge.ready.Load() > before }); e != nil {
			return response{Error: e.Error(), Code: "driver_ready"}
		}
		if _, err := r.client.Forward(ctx, r.machine, env); !remoteclient.Uncertain(err) {
			return response{Error: "running retry was final", Code: "driver_duplicate"}
		}
		os.Remove(prefix + ".hold")
		var operationErr error
		e = poll(ctx, "original signed result", func() bool {
			result, operationErr = r.client.Forward(ctx, r.machine, env)
			return !remoteclient.Uncertain(operationErr)
		})
		if e == nil {
			e = operationErr
		}
		if e == nil {
			var err error
			again, err := r.client.Forward(ctx, r.machine, env)
			if err != nil || !proto.Equal(result, again) {
				return response{Error: "cached result mismatch", Code: "driver_cache"}
			}
			verified = true
		}
	} else {
		result, e = r.client.Forward(ctx, r.machine, env)
	}
	out := response{Code: "ok", Signed: e == nil, CutVerified: verified}
	if e != nil {
		out.Code = "driver_error"
		out.Error = e.Error()
		out.Unknown = remoteclient.Uncertain(e)
		var ce *remoteclient.CommandError
		if errors.As(e, &ce) {
			out.Code = ce.Code
			out.Signed = ce.FromDaemon
		}
	}
	if result != nil {
		out.Body, _ = protojson.Marshal(result)
		session := result.GetCreateSession().GetSession()
		if session == nil {
			session = result.GetForkSession().GetSession()
		}
		if session == nil {
			session = result.GetResumeSession().GetSession()
		}
		if session != nil {
			out.Session = session.Id
			r.native[session.Id] = q.CLI
		}
	}
	if raw, err := os.ReadFile(filepath.Join(r.root, "trace", id+".count")); err == nil {
		out.Calls = len(raw)
	}
	if q.Cut && out.Calls != 1 {
		out.Code = "driver_execution_count"
		out.Error = "handler did not execute exactly once"
		out.Unknown = true
	}
	record := map[string]any{"label": q.Label, "cli": q.CLI, "code": out.Code, "unknown": out.Unknown, "signed": out.Signed, "cutVerified": out.CutVerified, "calls": out.Calls}
	raw, _ := json.Marshal(record)
	r.results.Write(append(raw, '\n'))
	return out
}
func (r *run) serve(w http.ResponseWriter, h *http.Request) {
	if h.Header.Get("X-Transport-Test") != string(mustRead(filepath.Join(r.root, "control-token"))) {
		http.Error(w, "unauthorized", 401)
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, 150*time.Second)
	defer cancel()
	var q request
	if e := json.NewDecoder(h.Body).Decode(&q); e != nil {
		http.Error(w, "bad input", 400)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	switch h.URL.Path {
	case "/rpc":
		enc.Encode(r.rpc(ctx, q))
	case "/ready":
		e := r.ready(ctx, q.Session)
		if e != nil {
			enc.Encode(response{Code: "driver_ready", Error: e.Error()})
		} else {
			enc.Encode(response{Code: "ok"})
		}
	case "/screen":
		raw := r.screen(q.Session)
		enc.Encode(map[string]any{"raw": raw, "screen": r.adapter(q.Session).Analyze(raw)})
	case "/events":
		s, e := r.find(q.Session)
		if e != nil {
			enc.Encode(response{Error: e.Error()})
			return
		}
		rows, e := r.client.Events(ctx, s, q.After)
		if e != nil {
			enc.Encode(response{Error: e.Error()})
			return
		}
		out := []json.RawMessage{}
		for _, v := range rows {
			b, _ := protojson.Marshal(v)
			out = append(out, b)
		}
		enc.Encode(out)
	case "/sessions":
		rows, e := r.client.Sessions(ctx)
		if e != nil {
			enc.Encode(response{Error: e.Error()})
			return
		}
		out := []json.RawMessage{}
		for _, v := range rows {
			b, _ := protojson.Marshal(v.Info)
			out = append(out, b)
		}
		enc.Encode(out)
	case "/restart":
		before := r.bridge.ready.Load()
		r.restart()
		e := poll(ctx, "restarted Ready", func() bool { return r.bridge.ready.Load() > before })
		if e != nil {
			enc.Encode(response{Code: "driver_ready", Error: e.Error()})
		} else {
			enc.Encode(response{Code: "ok"})
		}
	default:
		http.Error(w, "not found", 404)
	}
}
func mustRead(p string) []byte { b, e := os.ReadFile(p); must(e); return bytes.TrimSpace(b) }
func main() {
	root := os.Getenv("RA_DEV_DIR")
	if !local(root) || os.Getenv("REMOTE_AGENT_HOME") != filepath.Join(root, "bootstrap") {
		panic("explicit isolated bootstrap HOME required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	env := map[string]string{}
	for _, v := range os.Environ() {
		k, x, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(k, "REMOTE_AGENT_") && !strings.HasPrefix(k, "RA_") {
			env[k] = x
		}
	}
	env["RA_DEV_DIR"] = root
	env["REMOTE_AGENT_TMUX_SOCKET"] = "ra-t4-" + fmt.Sprint(os.Getpid())
	env["CODEX_HOME"] = filepath.Join(root, "candidate/native-codex")
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(root, "candidate/native-claude")
	env["GEMINI_CLI_HOME"] = filepath.Join(root, "candidate/native-agy")
	env["REMOTE_AGENT_HOME"] = filepath.Join(root, "server")
	must(os.MkdirAll(env["REMOTE_AGENT_HOME"], 0700))
	seed := make([]byte, 24)
	_, e := rand.Read(seed)
	must(e)
	env["RA_SETUP_TOKEN"] = hex.EncodeToString(seed)
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	must(e)
	addr := ln.Addr().String()
	ln.Close()
	stopServer := start(exec.Command(filepath.Join(root, "bin/remote-agent-server"), "--listen", addr, "--db", filepath.Join(root, "relay.db"), "--max-accounts", "2"), env, filepath.Join(root, "server.log"))
	defer stopServer()
	must(poll(ctx, "local server", func() bool {
		res, e := http.Get("http://" + addr + "/healthz")
		if e != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	}))
	u, _ := url.Parse("http://" + addr)
	b := &bridge{target: u, proxy: httputil.NewSingleHostReverseProxy(u), streams: map[*websocket.Conn]bool{}}
	front := httptest.NewServer(b)
	defer front.Close()
	defer b.drop()
	auth, e := remoteclient.NewAuthority(ctx, front.URL, env["RA_SETUP_TOKEN"])
	must(e)
	env["REMOTE_AGENT_HOME"] = filepath.Join(root, "candidate/machine")
	must(updater.EnsureState(env["REMOTE_AGENT_HOME"]))
	_, ed, e := ed25519.GenerateKey(nil)
	must(e)
	x, _, e := deviceauth.NewXKey()
	must(e)
	machine, e := auth.Enroll(ctx, ed, x, deviceauth.Machine)
	must(e)
	must(machine.Endpoint.Save(filepath.Join(env["REMOTE_AGENT_HOME"], "security-v3.db")))
	machine.Endpoint.Close()
	must(os.Setenv("REMOTE_AGENT_HOME", env["REMOTE_AGENT_HOME"]))
	fmt.Println("ISOLATION verified before account save", env["REMOTE_AGENT_HOME"])
	must((&relay.Account{Server: front.URL, Root: auth.Root, DeviceSeed: ed.Seed(), DeviceID: machine.ID, Security: &relay.SecurityConfig{Pin: auth.Pin, XPrivate: x}}).Save())
	endpoint, e := deviceauth.InitEndpoint("", auth.Pin, auth.States, auth.Proofs, auth.Member.ID, auth.Ed.Seed(), auth.X, auth.Lease)
	must(e)
	defer endpoint.Close()
	client := remoteclient.NewClient(front.URL, auth.Token, auth.Member.ID, endpoint)
	defer client.Close()
	delete(env, "RA_SETUP_TOKEN")
	env["HOME"] = filepath.Join(root, "candidate/native-home")
	must(os.MkdirAll(env["HOME"], 0700))
	write(filepath.Join(env["HOME"], ".zshenv"), []byte("export PATH='"+strings.ReplaceAll(os.Getenv("PATH"), "'", "'\"'\"'")+"'\nexport DISABLE_AUTOUPDATER=1\n"))
	env["DISABLE_AUTOUPDATER"] = "1"
	for _, name := range []string{"CLAUDE", "CODEX"} {
		bin := os.Getenv("RA_REAL_" + name)
		if bin == "" {
			bin, e = exec.LookPath(strings.ToLower(name))
			must(e)
		}
		env["RA_"+name+"_BIN"] = bin
	}
	var vars strings.Builder
	for _, k := range []string{"HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "GEMINI_CLI_HOME", "DISABLE_AUTOUPDATER", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		fmt.Fprintf(&vars, "%s=%s\n", k, env[k])
	}
	write(filepath.Join(env["REMOTE_AGENT_HOME"], "agent.env"), []byte(vars.String()))
	log, e := os.OpenFile(filepath.Join(root, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(e)
	defer log.Close()
	r := &run{root: root, project: filepath.Join(root, "candidate/project"), socket: env["REMOTE_AGENT_TMUX_SOCKET"], machine: machine.ID, binary: filepath.Join(root, "bin/remote-agent"), env: env, client: client, ctx: ctx, bridge: b, native: map[string]string{}, results: log}
	r.restart()
	defer func() { r.stopDaemon(); _ = exec.Command("tmux", "-L", r.socket, "kill-server").Run() }()
	must(poll(ctx, "machine Ready", func() bool { return b.ready.Load() > 0 }))
	token := make([]byte, 16)
	_, e = rand.Read(token)
	must(e)
	write(filepath.Join(root, "control-token"), []byte(hex.EncodeToString(token)))
	control := httptest.NewServer(http.HandlerFunc(r.serve))
	defer control.Close()
	write(filepath.Join(root, "control-url"), []byte(control.URL))
	write(filepath.Join(root, "socket"), []byte(r.socket))
	fmt.Println("READY isolated transport controller")
	<-ctx.Done()
}
