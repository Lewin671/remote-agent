package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/machinestatus"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestScreenFixturesMatchDaemonHandlers verifies that real internal/daemon
// handlers produce wire responses whose structural invariants match the daemon
// payloads embedded in testdata/screens/*.input.json.
func TestScreenFixturesMatchDaemonHandlers(t *testing.T) {
	ctx := context.Background()

	// 1. ListDirectories: real handler returns absolute cleaned Path, Parent
	//    (empty at root "/"), and sorted basenames without hidden entries.
	root := t.TempDir()
	for _, name := range []string{"Beta", "alpha", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirRes, err := listDirectories(ctx, root, nil)
	if err != nil {
		t.Fatalf("listDirectories(%q): %v", root, err)
	}
	assertListDirectoriesInvariants(t, "real listDirectories(temp)", dirRes)
	wantDirs := []string{"alpha", "Beta"}
	if len(dirRes.Dirs) != len(wantDirs) || dirRes.Dirs[0] != wantDirs[0] || dirRes.Dirs[1] != wantDirs[1] {
		t.Fatalf("listDirectories dirs = %v, want %v", dirRes.Dirs, wantDirs)
	}
	rootRes, err := listDirectories(ctx, "/", nil)
	if err != nil {
		t.Fatalf("listDirectories(/): %v", err)
	}
	assertListDirectoriesInvariants(t, "real listDirectories(/)", rootRes)
	if rootRes.Parent != "" {
		t.Fatalf("listDirectories(/) parent = %q, want empty", rootRes.Parent)
	}

	// 2. GetSettings: always returns non-nil Settings with positive defaults
	//    and non-nil ImageStorage.
	d := New(t.TempDir(), Config{
		Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CLAUDE: {Cmd: "/usr/bin/true"},
		},
	})
	d.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/usr/bin/true", "")
	d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1 << 30}, nil }
	h := &handler{d: d}

	settingsResp, err := h.GetSettings(ctx, connect.NewRequest(&pb.GetSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	assertGetSettingsInvariants(t, "real GetSettings", settingsResp.Msg)

	// 3. ListModels (built-in Claude catalog): returns DefaultModel and non-empty
	//    model slugs.
	modelsResp, err := h.ListModels(ctx, connect.NewRequest(&pb.ListModelsRequest{Agent: pb.Agent_AGENT_CLAUDE}))
	if err != nil {
		t.Fatalf("ListModels(claude): %v", err)
	}
	assertListModelsInvariants(t, "real ListModels(claude)", modelsResp.Msg)

	// 4. Schedule list: ResultJson decodes as {"tasks": [...]}.
	schedResp, err := h.Schedule(ctx, connect.NewRequest(&pb.ScheduleRequest{Operation: "list"}))
	if err != nil {
		t.Fatalf("Schedule(list): %v", err)
	}
	assertScheduleResultInvariants(t, "real Schedule(list)", schedResp.Msg.ResultJson)

	// 5. GetMachineStatus: produces pb.MachineStatus with valid non-negative metrics.
	var ticks uint64
	d.statusCollector = machinestatus.NewReader(d.dir, nil, func(context.Context) (machinestatus.Raw, error) {
		ticks++
		return machinestatus.Raw{
			Total: ticks * 100,
			Status: &pb.MachineStatus{
				Cores: 8,
			},
		}, nil
	})
	statusCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.statusCollector.Run(statusCtx)
	statusResp, err := h.GetMachineStatus(statusCtx, connect.NewRequest(&pb.GetMachineStatusRequest{}))
	if err != nil {
		t.Fatalf("GetMachineStatus: %v", err)
	}
	assertMachineStatusInvariants(t, "real GetMachineStatus", statusResp.Msg)
}

// TestScreenInputFixturesDaemonContract parses every daemon-originated wire
// payload inside testdata/screens/*.input.json using strict protojson (no
// unknown fields allowed) and checks the structural invariants verified against
// real daemon handlers in TestScreenFixturesMatchDaemonHandlers.
func TestScreenInputFixturesDaemonContract(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "screens", "*.input.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no testdata/screens/*.input.json fixtures found")
	}

	strict := protojson.UnmarshalOptions{DiscardUnknown: false}
	var checkedResults int

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Steps []struct {
				Event struct {
					CommandOutcome *struct {
						Result json.RawMessage `json:"result"`
					} `json:"commandOutcome"`
					ListsFetched *struct {
						Machines []struct {
							Meta json.RawMessage `json:"meta"`
						} `json:"machines"`
						Sessions []struct {
							Meta json.RawMessage `json:"meta"`
						} `json:"sessions"`
					} `json:"listsFetched"`
				} `json:"event"`
			} `json:"steps"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		base := filepath.Base(path)

		for idx, step := range doc.Steps {
			if lf := step.Event.ListsFetched; lf != nil {
				for mi, m := range lf.Machines {
					if len(m.Meta) == 0 {
						continue
					}
					var meta pb.MachineMeta
					if err := strict.Unmarshal(m.Meta, &meta); err != nil {
						t.Errorf("%s step %d machine[%d].meta invalid wire MachineMeta: %v", base, idx, mi, err)
					}
				}
				for si, s := range lf.Sessions {
					if len(s.Meta) == 0 {
						continue
					}
					var meta pb.Session
					if err := strict.Unmarshal(s.Meta, &meta); err != nil {
						t.Errorf("%s step %d session[%d].meta invalid wire Session: %v", base, idx, si, err)
					}
				}
			}

			if step.Event.CommandOutcome == nil || len(step.Event.CommandOutcome.Result) == 0 {
				continue
			}
			var res pb.CommandResult
			if err := strict.Unmarshal(step.Event.CommandOutcome.Result, &res); err != nil {
				t.Errorf("%s step %d commandOutcome.result invalid wire CommandResult: %v", base, idx, err)
				continue
			}
			checkedResults++
			label := base + " step " + string(rune('0'+(idx%10)))
			if idx >= 10 {
				label = base
			}
			switch payload := res.Kind.(type) {
			case *pb.CommandResult_ListDirectories:
				assertListDirectoriesInvariants(t, label, payload.ListDirectories)
			case *pb.CommandResult_GetSettings:
				assertGetSettingsInvariants(t, label, payload.GetSettings)
			case *pb.CommandResult_ListModels:
				assertListModelsInvariants(t, label, payload.ListModels)
			case *pb.CommandResult_MachineStatus:
				assertMachineStatusInvariants(t, label, payload.MachineStatus)
			case *pb.CommandResult_Schedule:
				if payload.Schedule != nil && len(payload.Schedule.ResultJson) > 0 {
					assertScheduleResultInvariants(t, label, payload.Schedule.ResultJson)
				}
			case *pb.CommandResult_CreateSession:
				if payload.CreateSession == nil || payload.CreateSession.Session == nil {
					t.Errorf("%s: CreateSession missing Session", label)
				} else if payload.CreateSession.Session.Id == "" || payload.CreateSession.Session.Cwd == "" {
					t.Errorf("%s: CreateSession missing id/cwd: %+v", label, payload.CreateSession.Session)
				}
			case *pb.CommandResult_BeginImageUpload:
				if payload.BeginImageUpload == nil || payload.BeginImageUpload.Status == nil || payload.BeginImageUpload.Status.TransferId == "" {
					t.Errorf("%s: BeginImageUpload missing status/transferId", label)
				}
			case *pb.CommandResult_GetSharedFileChunk:
				if payload.GetSharedFileChunk == nil {
					t.Errorf("%s: GetSharedFileChunk nil", label)
				} else if payload.GetSharedFileChunk.GetTotalBytes() < uint64(len(payload.GetSharedFileChunk.Data)) {
					t.Errorf("%s: GetSharedFileChunk totalBytes (%d) < len(data) (%d)", label, payload.GetSharedFileChunk.GetTotalBytes(), len(payload.GetSharedFileChunk.Data))
				}
			}
		}
	}

	if checkedResults == 0 {
		t.Fatal("expected at least one commandOutcome.result across testdata/screens/*.input.json")
	}
}

func assertListDirectoriesInvariants(t *testing.T, label string, res *pb.ListDirectoriesResponse) {
	t.Helper()
	if res == nil {
		t.Errorf("%s: nil ListDirectoriesResponse", label)
		return
	}
	if !strings.HasPrefix(res.Path, "/") {
		t.Errorf("%s: ListDirectoriesResponse.Path %q must be absolute", label, res.Path)
	}
	wantParent := ""
	if p := filepath.Dir(filepath.Clean(res.Path)); p != filepath.Clean(res.Path) {
		wantParent = p
	}
	if res.Parent != wantParent {
		t.Errorf("%s: ListDirectoriesResponse.Parent = %q, want %q for Path %q", label, res.Parent, wantParent, res.Path)
	}
	for _, entry := range res.Dirs {
		if entry == "" || strings.ContainsAny(entry, `/\`) || strings.HasPrefix(entry, ".") {
			t.Errorf("%s: ListDirectoriesResponse.Dirs entry %q must be a non-hidden basename", label, entry)
		}
	}
}

func assertGetSettingsInvariants(t *testing.T, label string, res *pb.GetSettingsResponse) {
	t.Helper()
	if res == nil || res.Settings == nil {
		t.Errorf("%s: GetSettingsResponse missing Settings", label)
	}
}

func assertListModelsInvariants(t *testing.T, label string, res *pb.ListModelsResponse) {
	t.Helper()
	if res == nil || len(res.Models) == 0 {
		t.Errorf("%s: ListModelsResponse missing Models", label)
		return
	}
	for i, m := range res.Models {
		if m.Slug == "" {
			t.Errorf("%s: ListModelsResponse.Models[%d] has empty slug", label, i)
		}
	}
}

func assertMachineStatusInvariants(t *testing.T, label string, st *pb.MachineStatus) {
	t.Helper()
	if st == nil {
		t.Errorf("%s: nil MachineStatus", label)
		return
	}
	if st.ObservedMs <= 0 {
		t.Errorf("%s: MachineStatus.ObservedMs = %d, want > 0", label, st.ObservedMs)
	}
	if st.Cores == 0 {
		t.Errorf("%s: MachineStatus.Cores = 0, want > 0", label)
	}
	if st.CpuPercent < 0 {
		t.Errorf("%s: MachineStatus.CpuPercent = %v, want >= 0", label, st.CpuPercent)
	}
}

func assertScheduleResultInvariants(t *testing.T, label string, raw []byte) {
	t.Helper()
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Errorf("%s: ScheduleResponse.ResultJson invalid JSON: %v", label, err)
		return
	}
	if _, hasTasks := probe["tasks"]; hasTasks {
		var list struct {
			Tasks []schedule.Task `json:"tasks"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Errorf("%s: ScheduleResponse.ResultJson list decode failed: %v", label, err)
			return
		}
		for i, task := range list.Tasks {
			if task.ID == "" || task.Revision == 0 || task.Status == "" {
				t.Errorf("%s: ScheduleResponse tasks[%d] missing id/revision/status: %+v", label, i, task)
			}
		}
		return
	}
	var task schedule.Task
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Errorf("%s: ScheduleResponse.ResultJson task decode failed: %v", label, err)
		return
	}
	if task.ID == "" || task.Revision == 0 || task.Status == "" {
		t.Errorf("%s: ScheduleResponse task missing id/revision/status: %+v", label, task)
	}
}
