package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// maxSeen bounds how many request IDs are remembered for deduplication.
const maxSeen = 1000

// input is one message waiting to be submitted.
type input struct {
	Text      string `json:"text"`
	RequestID string `json:"request_id,omitempty"`
	// Images are pasted before the text, in order.
	Images []queuedImage `json:"images,omitempty"`
	// Origin is who sent it.
	Origin *storedOrigin `json:"origin,omitempty"`
	// Mode is how it is delivered while a turn runs: steer (the default)
	// is written as soon as the input box is free, queue after the turn.
	Mode pb.SendMode `json:"mode,omitempty"`
	// Paused: the turn was interrupted before it was written; it waits
	// for ResumeInput or CancelInput.
	Paused bool `json:"paused,omitempty"`
	// Stage is how far writing it has got; it is saved before each write
	// to the terminal, so a restart knows what the input box may hold.
	Stage pb.InputStage `json:"stage,omitempty"`
	// Indexes are the N of the "[Image #N]" each pasted image became.
	Indexes []int32 `json:"indexes,omitempty"`
	// matched: a user message in the CLI's record was tied to it.
	matched bool
	// received: a held message whose user message is logged, so the model
	// got it, while its outcome is still to be logged (held.go).
	received bool
	// Inflight is set just before the message is written to the terminal:
	// it holds the history.jsonl offset at that moment. After a crash, the
	// history from there tells whether the message was submitted.
	Inflight *int64 `json:"inflight,omitempty"`
	// InflightSeq is the last event sequence number at that moment; a
	// matching user message after it also confirms submission.
	InflightSeq uint64 `json:"inflight_seq,omitempty"`
}

// queueState is queue.json: accepted input that has not been submitted,
// and recent request IDs. It is saved before a send is acknowledged.
type queueState struct {
	Queue []input  `json:"queue"`
	Seen  []string `json:"seen,omitempty"`
	// Held are messages the CLI took into its own queue during a turn
	// (Claude Code) and the model has not received yet (held.go).
	Held []input `json:"held,omitempty"`
}

func loadQueue(dir string) (*queueState, error) {
	b, err := os.ReadFile(filepath.Join(dir, "queue.json"))
	if os.IsNotExist(err) {
		return &queueState{}, nil
	}
	if err != nil {
		return nil, err
	}
	q := &queueState{}
	if err := json.Unmarshal(b, q); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *queueState) save(dir string) error {
	return q.saveWith(dir, writeFileSync)
}

func (q *queueState) saveWith(dir string, write func(string, []byte) error) error {
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	return write(filepath.Join(dir, "queue.json"), b)
}

// writing reports whether in has started to be written: it can no longer
// move in the queue.
func (in input) writing() bool {
	return in.Inflight != nil || in.Stage > pb.InputStage_INPUT_STAGE_WAITING
}

// queued reports whether in waits for the end of the turn.
func (in input) queued() bool { return in.Mode == pb.SendMode_SEND_MODE_QUEUE }

// insertAt is where a new message goes. The queue keeps, after a message
// being written, the steer messages, then the queue messages, then the
// paused ones, each in the order they came: a steer does not wait behind
// messages held for the end of the turn, and nothing waits behind paused
// ones.
func (q *queueState) insertAt(in input) int {
	if in.Paused {
		return len(q.Queue)
	}
	for i, o := range q.Queue {
		if o.writing() {
			continue
		}
		if o.Paused || !in.queued() && o.queued() {
			return i
		}
	}
	return len(q.Queue)
}

// insert puts in at its place and returns its 1-based position.
func (q *queueState) insert(in input) int {
	i := q.insertAt(in)
	q.Queue = append(q.Queue[:i:i], append([]input{in}, q.Queue[i:]...)...)
	return i + 1
}

func (q *queueState) seen(id string) bool {
	for _, s := range q.Seen {
		if s == id {
			return true
		}
	}
	return false
}

func (q *queueState) remember(id string) {
	q.Seen = append(q.Seen, id)
	if len(q.Seen) > maxSeen {
		q.Seen = q.Seen[len(q.Seen)-maxSeen:]
	}
}
