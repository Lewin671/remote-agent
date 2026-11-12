package daemon

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"google.golang.org/protobuf/proto"
)

// Distinct valid definitions, about 5.5 KB each. Random printable text avoids
// making a large list look artificially cheap to send because it all repeats.
func fillRetainedScheduleTasks(d *Daemon, count int) {
	random := rand.New(rand.NewPCG(1, 2))
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := len(d.tasks); i < count; i++ {
		message := make([]byte, 5000)
		for j := range message {
			message[j] = alphabet[random.IntN(len(alphabet))]
		}
		id := fmt.Sprintf("retained-%04d", i)
		d.tasks[id] = &schedule.Task{ID: id, Revision: 1, Status: "cancelled",
			Spec:        schedule.Spec{Title: "Retained task", Message: string(message), Trigger: schedule.Trigger{Type: "interval", IntervalSeconds: 3600}, Target: schedule.Target{Type: "resume_session", Session: "0123456789abcdef"}},
			CreationKey: fmt.Sprintf("fixture:%d", i), CreationHash: strings.Repeat("a", 64)}
	}
}

func scheduleSpecJSON(t *testing.T, spec schedule.Spec) []byte {
	t.Helper()
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(spec); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestScheduleAdmissionCapsKeepBookkeepingAvailable(t *testing.T) {
	for _, total := range []int{1000, 1001} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			h := scheduleHarness(t)
			spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600})
			original := createTask(t, h, spec, "original")
			fillRetainedScheduleTasks(h.d, total)
			if err := h.d.saveSchedulesLocked(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.d.dir, "schedules.json")
			state, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(state) <= 1<<20 {
				t.Fatal("fixture must exceed the rejected file-wide byte cap")
			}
			reload := func() {
				t.Helper()
				if err := h.d.loadSchedules(); err != nil {
					t.Fatalf("load at capacity: %v", err)
				}
			}
			reload()
			for _, scope := range []string{"", h.s.meta.ID} {
				_, err := h.d.scheduleCommandScoped(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: scheduleSpecJSON(t, spec), RequestId: "new"}, originLocal, scope)
				if connect.CodeOf(err) != connect.CodeResourceExhausted {
					t.Fatalf("new creation at capacity (scope=%q): %v", scope, err)
				}
			}
			// Deduplication precedes admission, including after restart.
			retry, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: scheduleSpecJSON(t, spec), RequestId: "original"}, originLocal)
			var duplicate schedule.Task
			if err != nil || json.Unmarshal(retry, &duplicate) != nil || duplicate.ID != original.ID {
				t.Fatalf("creation retry: %s %v", retry, err)
			}
			manage := func(op string) {
				t.Helper()
				if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: original.ID}, originLocal); err != nil {
					t.Fatalf("%s at capacity: %v", op, err)
				}
				reload()
			}
			manage("pause")
			manage("resume")
			// The scheduler must still reserve, deliver and persist an execution.
			now := readTask(t, h.d, original.ID).NextRunAt.Add(time.Second)
			h.d.tickSchedules(now)
			reload()
			active := readTask(t, h.d, original.ID).Active
			if active == nil || active.Status != "queued" {
				t.Fatal("run did not start and persist at capacity")
			}
			completeTask(h, active, now)
			h.d.tickSchedules(now.Add(time.Second))
			reload()
			got := readTask(t, h.d, original.ID)
			if got.Active != nil || len(got.Runs) != 1 || got.Runs[0].Status != "completed" || !got.Runs[0].Notified {
				t.Fatalf("finish/notification bookkeeping at capacity: %+v", got)
			}
			h.d.tickSchedules(now.Add(2 * time.Second))
			reload()
			if got := readTask(t, h.d, original.ID); got.Dispatches != 1 || got.Active != nil || len(got.Runs) != 1 {
				t.Fatal("persisted finish was repeated after reload")
			}
			manage("cancel")
			if readTask(t, h.d, original.ID).Status != "cancelled" {
				t.Fatal("cancel was not saved")
			}
			// A small definition edit is allowed even above the total creation cap.
			edited := readTask(t, h.d, original.ID).Spec
			edited.Title += " edited"
			if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "update", TaskId: original.ID, SpecJson: scheduleSpecJSON(t, edited)}, originLocal); err != nil {
				t.Fatalf("edit at capacity: %v", err)
			}
			edited.Message += " more"
			if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "update", TaskId: original.ID, SpecJson: scheduleSpecJSON(t, edited)}, originLocal); err != nil {
				t.Fatalf("bounded definition growth at capacity: %v", err)
			}
			reload()
			manage("cancel")
			if len(h.d.tasks) != total {
				t.Fatal("admission or bookkeeping lost/added a task")
			}
		})
	}
}

func TestScheduleCreationBelowCountCapIgnoresFileSize(t *testing.T) {
	for _, total := range []int{200, 999} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			h := scheduleHarness(t)
			fillRetainedScheduleTasks(h.d, total)
			if err := h.d.saveSchedulesLocked(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(h.d.dir, "schedules.json"))
			if err != nil {
				t.Fatal(err)
			}
			if len(data) <= 1<<20 {
				t.Fatal("fixture must exceed the former file budget")
			}
			spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600})
			createTask(t, h, spec, "")
			if err := h.d.loadSchedules(); err != nil {
				t.Fatal(err)
			}
			if len(h.d.tasks) != total+1 {
				t.Fatal("creation below the count cap was lost")
			}
		})
	}
}

func TestScheduleDefinitionGrowthAdmission(t *testing.T) {
	for _, mcp := range []bool{false, true} {
		t.Run(fmt.Sprintf("mcp=%t", mcp), func(t *testing.T) {
			h := scopedHarness(t, "ask")
			scope := ""
			if mcp {
				scope = h.s.meta.ID
			}
			spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600})
			command := func(op, id string, spec schedule.Spec) ([]byte, error) {
				return h.d.scheduleCommandScoped(&pb.ScheduleRequest{Operation: op, TaskId: id, SessionId: h.s.meta.ID, SpecJson: scheduleSpecJSON(t, spec)}, originLocal, scope)
			}
			large := spec
			large.Message = strings.Repeat("<", 12000) // 72,000 serialized bytes; raw request <64 KiB
			if _, err := command("create", "", large); connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("oversized creation: %v", err)
			}
			if len(h.d.tasks) != 0 {
				t.Fatal("rejected creation retained a task")
			}
			raw, err := command("create", "", spec)
			if err != nil {
				t.Fatal(err)
			}
			var task schedule.Task
			if err := json.Unmarshal(raw, &task); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.d.dir, "schedules.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := command("update", task.ID, large); connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("oversized growth: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || readTask(t, h.d, task.ID).Revision != task.Revision {
				t.Fatal("rejected growth changed the definition or durable state")
			}
			// Two individually bounded additions may produce a definition larger than
			// one request's budget. Subsequent small edits and reductions still work.
			smaller := spec
			smaller.Message = strings.Repeat("<", 10000)
			if _, err := command("update", task.ID, smaller); err != nil {
				t.Fatal(err)
			}
			if _, err := command("update", task.ID, large); err != nil {
				t.Fatalf("bounded increment into a large definition: %v", err)
			}
			large = readTask(t, h.d, task.ID).Spec
			large.Title += " edited"
			if _, err := command("update", task.ID, large); err != nil {
				t.Fatalf("lightweight edit of large definition: %v", err)
			}
			if err := h.d.loadSchedules(); err != nil {
				t.Fatalf("load oversized definition: %v", err)
			}
			if _, err := command("update", task.ID, spec); err != nil {
				t.Fatalf("reduction of large definition: %v", err)
			}
		})
	}
}

// Measurement only: list still returns the complete set. Neither pagination
// nor a new wire error/truncation policy is introduced by admission quotas.
func TestScheduleListPayloadSizes(t *testing.T) {
	h := scheduleHarness(t)
	var exceedsFrame, exceedsPlain bool
	for _, count := range []int{190, 400, 600, 1000} {
		fillRetainedScheduleTasks(h.d, count)
		data, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "list"}, originLocal)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := proto.Marshal(&pb.CommandResult{Kind: &pb.CommandResult_Schedule{Schedule: &pb.ScheduleResponse{ResultJson: data}}})
		if err != nil {
			t.Fatal(err)
		}
		var packed bytes.Buffer
		compressor, err := flate.NewWriter(&packed, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := compressor.Write(plain); err != nil {
			t.Fatal(err)
		}
		if err := compressor.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("tasks=%d JSON=%d protobuf=%d DEFLATE=%d", count, len(data), len(plain), packed.Len())
		exceedsFrame = exceedsFrame || packed.Len() > 2<<20 // server maxFrameBytes, before envelope overhead
		exceedsPlain = exceedsPlain || len(plain) > deviceauth.MaxCipherBytes-16
	}
	if !exceedsFrame || !exceedsPlain {
		t.Fatal("fixture did not demonstrate both existing transport limits")
	}
}
