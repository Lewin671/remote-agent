package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// maxSeen bounds how many request IDs are remembered for deduplication.
const maxSeen = 1000

// input is one message waiting to be submitted.
type input struct {
	Text      string `json:"text"`
	RequestID string `json:"request_id,omitempty"`
	// Inflight is set just before the message is written to the terminal:
	// it holds the history.jsonl offset at that moment. After a crash, the
	// history from there tells whether the message was submitted.
	Inflight *int64 `json:"inflight,omitempty"`
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
	tmp := filepath.Join(dir, "queue.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "queue.json"))
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
