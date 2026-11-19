package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A clean environment still carries the pane's terminal type: a login
// shell expects one, and its terminal is this pane.
func TestEnvListKeepsThePaneTerm(t *testing.T) {
	t.Setenv("TERM", "screen-256color")
	env := envList(map[string]string{"PATH": "/usr/bin"})
	if !slices.Contains(env, "TERM=screen-256color") || !slices.Contains(env, "PATH=/usr/bin") {
		t.Fatalf("env %q", env)
	}
	// A variable the file names itself wins.
	if env := envList(map[string]string{"TERM": "xterm", "PATH": "/bin"}); !slices.Contains(env, "TERM=xterm") {
		t.Fatalf("env %q", env)
	}
}

// loadEnvFile reads the variables and removes the file: what it held
// (possibly credentials) must not linger on disk.
func TestLoadEnvFileReadsAndDeletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env.json")
	if err := os.WriteFile(path, []byte(`{"A":"1","B":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := loadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if env["A"] != "1" || env["B"] != "two" {
		t.Fatalf("env %v", env)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the environment file is still there: %v", err)
	}
}

func TestJoinPicturesSaysToConfirmHereBeforeTheAuthority(t *testing.T) {
	got := joinPictures([]string{"🐶", "🍎", "🚗", "🌙"})
	if !strings.Contains(got, "🐶  🍎  🚗  🌙") {
		t.Fatalf("the pictures are missing: %q", got)
	}
	here, authority := strings.Index(got, "confirm here first"), strings.Index(got, "then allow it on the authority client")
	if here < 0 || authority < here {
		t.Fatalf("the order is not said: %q", got)
	}
}

func TestAgentEnvReadsOnlyCurrentFile(t *testing.T) {
	for _, tc := range []struct {
		name            string
		current, legacy string
		want            string
	}{
		{name: "missing"},
		{name: "legacy-only", legacy: "RA_TEST_ENV=old\n"},
		{name: "malformed-legacy", legacy: "not an environment assignment\n"},
		{name: "current", current: "# shared CLI environment\nexport RA_TEST_ENV='current'\n", want: "current"},
		{name: "both", current: "RA_TEST_ENV=current\n", legacy: "RA_TEST_ENV=old\n", want: "current"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("REMOTE_AGENT_HOME", dir)
			for name, content := range map[string]string{"agent.env": tc.current, "codex.env": tc.legacy} {
				if content != "" {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			env, err := agentEnv()
			if err != nil {
				t.Fatal(err)
			}
			if got := env["RA_TEST_ENV"]; got != tc.want {
				t.Fatalf("RA_TEST_ENV=%q, want %q", got, tc.want)
			}
			if tc.want == "" && len(env) != 0 {
				t.Fatalf("missing current file should return empty configuration: %v", env)
			}
		})
	}
}

func TestParseNewOptions(t *testing.T) {
	defaultCwd := "/workspace/default"
	getwd := func() (string, error) { return defaultCwd, nil }
	getEnv := func() map[string]string { return map[string]string{"PATH": "/usr/bin"} }

	req, title, err := parseNewOptions(
		[]string{"--agent", "agy", "--cwd", "/workspace/custom", "--model", "gemini-pro-orchestrator", "--mode", "auto", "--title", "  UI   Redesign  ", "--prompt", "start now", "--", "--add-dir", "/tmp/extra"},
		strings.NewReader(""),
		getwd,
		getEnv,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Agent != pb.Agent_AGENT_ANTIGRAVITY || req.Cwd != "/workspace/custom" || req.Model != "gemini-pro-orchestrator" || req.Effort != "" || req.Mode != pb.ApprovalMode_APPROVAL_MODE_AUTO {
		t.Fatalf("unexpected request fields: %+v", req)
	}
	if title != "UI Redesign" || req.Prompt != "start now" || !slices.Equal(req.Args, []string{"--add-dir", "/tmp/extra"}) || req.Env["PATH"] != "/usr/bin" {
		t.Fatalf("unexpected title/prompt/args/env: title=%q req=%+v", title, req)
	}

	// Stdin prompt ("-") + --login-shell + --standard-context
	req2, _, err := parseNewOptions(
		[]string{"--agent=claude", "--standard-context", "--login-shell", "--prompt", "-"},
		strings.NewReader("hello from stdin\n\n"),
		getwd,
		getEnv,
	)
	if err != nil {
		t.Fatalf("stdin/login-shell error: %v", err)
	}
	if req2.Agent != pb.Agent_AGENT_CLAUDE || req2.Cwd != defaultCwd || !req2.StandardContext || len(req2.Env) != 0 || req2.Prompt != "hello from stdin" {
		t.Fatalf("unexpected stdin/login-shell request: %+v", req2)
	}

	// Validation errors and --help
	if _, _, err := parseNewOptions([]string{"--help"}, strings.NewReader(""), getwd, getEnv); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected ErrHelp, got %v", err)
	}
	for _, bad := range [][]string{
		{"--agent", "nonexistent"},
		{"--mode", "invalid"},
		{"--title", strings.Repeat("字", 61)},
		{"unflagged-positional-arg"},
		{"--unknown-flag"},
	} {
		if _, _, err := parseNewOptions(bad, strings.NewReader(""), getwd, getEnv); err == nil {
			t.Fatalf("expected error for %v, got nil", bad)
		}
	}
}

type newAndListTestHandler struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	created *pb.CreateSessionRequest
	renamed *pb.RenameSessionRequest
}

func (h *newAndListTestHandler) CreateSession(_ context.Context, req *connect.Request[pb.CreateSessionRequest]) (*connect.Response[pb.CreateSessionResponse], error) {
	h.created = req.Msg
	return connect.NewResponse(&pb.CreateSessionResponse{
		Session: &pb.Session{Id: "abcdef0123456789", Agent: req.Msg.Agent, Cwd: req.Msg.Cwd, Model: req.Msg.Model, Status: pb.SessionStatus_SESSION_STATUS_RUNNING, CreatedAt: timestamppb.Now()},
	}), nil
}

func (h *newAndListTestHandler) RenameSession(_ context.Context, req *connect.Request[pb.RenameSessionRequest]) (*connect.Response[pb.RenameSessionResponse], error) {
	h.renamed = req.Msg
	return connect.NewResponse(&pb.RenameSessionResponse{}), nil
}

func (h *newAndListTestHandler) ListSessions(_ context.Context, _ *connect.Request[pb.ListSessionsRequest]) (*connect.Response[pb.ListSessionsResponse], error) {
	return connect.NewResponse(&pb.ListSessionsResponse{
		Sessions: []*pb.Session{{
			Id: "abcdef0123456789", Agent: pb.Agent_AGENT_ANTIGRAVITY, Status: pb.SessionStatus_SESSION_STATUS_RUNNING,
			Model: "gemini-pro-orchestrator", Title: "Settings Redesign", Cwd: "/workspace/project", CreatedAt: timestamppb.Now(),
		}},
	}), nil
}

func (h *newAndListTestHandler) GetSession(_ context.Context, req *connect.Request[pb.GetSessionRequest]) (*connect.Response[pb.GetSessionResponse], error) {
	return connect.NewResponse(&pb.GetSessionResponse{
		Session: &pb.Session{
			Id: "abcdef0123456789", Agent: pb.Agent_AGENT_ANTIGRAVITY, Status: pb.SessionStatus_SESSION_STATUS_RUNNING,
			Model: "gemini-pro-orchestrator", Title: "Settings Redesign", Cwd: "/workspace/project", CreatedAt: timestamppb.Now(),
		},
	}), nil
}

func (h *newAndListTestHandler) GetSettings(_ context.Context, _ *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	return connect.NewResponse(&pb.GetSettingsResponse{
		Settings: &pb.DaemonSettings{
			DisabledAgents: []pb.Agent{pb.Agent_AGENT_CLAUDE},
			Antigravity:    &pb.AgentDefaults{Mode: pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS},
		},
	}), nil
}

func (h *newAndListTestHandler) ListModels(_ context.Context, req *connect.Request[pb.ListModelsRequest]) (*connect.Response[pb.ListModelsResponse], error) {
	if req.Msg.Agent == pb.Agent_AGENT_ANTIGRAVITY {
		return connect.NewResponse(&pb.ListModelsResponse{
			DefaultModel: "gemini-4-neon",
			Models: []*pb.ModelInfo{
				{Slug: "gemini-4-neon", DisplayName: "Gemini 4 Neon", Efforts: []string{"low", "medium", "high"}},
				{Slug: "gemini-pro-orchestrator", DisplayName: "Orchestrator (Carbon)"},
			},
		}), nil
	}
	return connect.NewResponse(&pb.ListModelsResponse{
		DefaultModel: "gpt-6-sol",
		Models:       []*pb.ModelInfo{{Slug: "gpt-6-sol", DisplayName: "GPT-6-Sol", Efforts: []string{"low", "medium", "high"}}},
	}), nil
}

func (h *newAndListTestHandler) SetModel(_ context.Context, _ *connect.Request[pb.SetModelRequest]) (*connect.Response[pb.SetModelResponse], error) {
	return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this CLI does not support that"))
}

func TestSessionAndModelsCommandsEndToEnd(t *testing.T) {
	dir, err := os.MkdirTemp("", "ra-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("REMOTE_AGENT_HOME", dir)
	h := &newAndListTestHandler{}
	mux := http.NewServeMux()
	path, handler := remoteagentv1connect.NewLocalServiceHandler(h, connect.WithInterceptors(protocol.Local()))
	mux.Handle(path, handler)
	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	captureStdout := func(fn func() error) string {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stdout
		os.Stdout = w
		runErr := fn()
		w.Close()
		os.Stdout = saved
		b, readErr := io.ReadAll(r)
		r.Close()
		if runErr != nil || readErr != nil {
			t.Fatalf("err=%v readErr=%v", runErr, readErr)
		}
		return string(b)
	}

	out := captureStdout(func() error {
		return runSession([]string{"create", "--agent", "agy", "--cwd", dir, "--model", "gemini-pro-orchestrator", "--title", "Settings Redesign", "--prompt", "implement it", "--login-shell"})
	})
	if strings.TrimSpace(out) != "abcdef0123456789" {
		t.Fatalf("unexpected stdout: %q", out)
	}
	if h.created == nil || h.created.Model != "gemini-pro-orchestrator" || h.created.Prompt != "implement it" || len(h.created.Env) != 0 {
		t.Fatalf("unexpected CreateSession request: %+v", h.created)
	}
	if h.renamed == nil || h.renamed.SessionId != "abcdef0123456789" || h.renamed.Title != "Settings Redesign" {
		t.Fatalf("unexpected RenameSession request: %+v", h.renamed)
	}

	tableOut := captureStdout(func() error { return runSession([]string{"list"}) })
	if !strings.Contains(tableOut, "TITLE") || !strings.Contains(tableOut, "Settings Redesign") {
		t.Fatalf("session list table missing title: %q", tableOut)
	}

	jsonListOut := captureStdout(func() error { return runSession([]string{"list", "--json"}) })
	var arr []map[string]any
	if err := json.Unmarshal([]byte(jsonListOut), &arr); err != nil || len(arr) != 1 || arr[0]["title"] != "Settings Redesign" {
		t.Fatalf("bad session list --json output: %q (%v)", jsonListOut, err)
	}

	jsonSingleOut := captureStdout(func() error { return runSession([]string{"get", "abcdef01", "--json"}) })
	var obj map[string]any
	if err := json.Unmarshal([]byte(jsonSingleOut), &obj); err != nil || obj["id"] != "abcdef0123456789" {
		t.Fatalf("bad session get --json output: %q (%v)", jsonSingleOut, err)
	}

	modelsTable := captureStdout(func() error { return runModels([]string{"agy"}) })
	for _, want := range []string{"Agent: agy (Antigravity)", "mode=full (machine)", "Orchestrator (Carbon)", "-\n"} {
		if !strings.Contains(modelsTable, want) {
			t.Fatalf("models agy missing %q:\n%s", want, modelsTable)
		}
	}

	allModelsJSON := captureStdout(func() error { return runModels([]string{"--json"}) })
	var allSummaries []agentModelsSummary
	if err := json.Unmarshal([]byte(allModelsJSON), &allSummaries); err != nil || len(allSummaries) != 2 {
		t.Fatalf("expected disabled claude skipped in models --json, got %+v (%v)", allSummaries, err)
	}

	if err := runSession([]string{"model", "abcdef01", "gemini-pro-orchestrator"}); err == nil || !strings.Contains(err.Error(), "session create --agent agy") {
		t.Fatalf("expected Antigravity launch-only model hint, got %v", err)
	}

	for _, flat := range []string{"new", "ls", "list", "attach", "send", "cancel", "resume-input", "events", "approve", "deny", "answer", "interrupt", "stop", "resume", "fork", "model", "mode", "rename", "compact", "plan"} {
		if removedSessionCommands[flat] == "" {
			t.Fatalf("missing migration target for removed command %q", flat)
		}
	}
}
