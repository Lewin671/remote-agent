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
	// Stage is how far writing it has got; it is saved before each write
	// to the terminal, so a restart knows what the input box may hold.
	Stage pb.InputStage `json:"stage,omitempty"`
	// Indexes are the N of the "[Image #N]" each pasted image became.
	Indexes []int32 `json:"indexes,omitempty"`
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
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	return writeFileSync(filepath.Join(dir, "queue.json"), b)
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
