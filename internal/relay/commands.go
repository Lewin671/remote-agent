package relay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// maxAhead bounds how far in the future a command's deadline may be. It
// also bounds how long request IDs must be remembered.
const maxAhead = 10 * time.Minute

// commandLog makes remote commands run at most once. A request ID is
// recorded as started before its command runs and with its result after;
// both are on disk before the next step. A retry gets the stored result.
// After a crash between the two, the outcome is unknown and the command is
// never run again. Entries are kept until their deadline has passed, after
// which the command is rejected anyway.
type commandLog struct {
	path string

	mu       sync.Mutex
	entries  map[string]*commandEntry
	inflight map[string]chan struct{}
	loadErr  error // unreadable deduplication state must never be treated as empty
}

type commandEntry struct {
	DeadlineMs int64  `json:"deadline_ms"`
	Result     []byte `json:"result,omitempty"` // CommandResult; nil while running
}

func openCommandLog(path string) *commandLog {
	c := &commandLog{path: path, entries: map[string]*commandEntry{}, inflight: map[string]chan struct{}{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c
	}
	if err != nil {
		c.loadErr = err
		return c
	}
	if err := json.Unmarshal(b, &c.entries); err != nil {
		c.loadErr = err
		return c
	}
	if c.entries == nil {
		c.loadErr = errors.New("null command log")
	}
	for _, e := range c.entries {
		if e == nil || e.DeadlineMs <= 0 {
			c.loadErr = errors.New("invalid command log entry")
			break
		}
	}
	return c
}

// errUnknownOutcome is the result for a command that was running when a
// previous daemon stopped.
func errUnknownOutcome() *pb.CommandResult {
	return &pb.CommandResult{ErrorCode: "aborted", ErrorMessage: "the daemon stopped while running this command; its outcome is unknown and it was not run again"}
}

// do runs fn once per request ID and returns its result. A request ID
// seen before gets its stored result; otherwise a non-nil reject, such as
// an expired deadline, is returned without recording anything.
func (c *commandLog) do(ctx context.Context, id string, deadline int64, reject *pb.CommandResult, fn func() *pb.CommandResult) *pb.CommandResult {
	if c.loadErr != nil {
		return &pb.CommandResult{ErrorCode: "failed_precondition", ErrorMessage: "remote commands disabled: cannot read command deduplication state; repair commands.json and restart the daemon"}
	}
	for {
		if ctx.Err() != nil {
			return &pb.CommandResult{ErrorCode: "canceled", ErrorMessage: "the command connection closed"}
		}
		c.mu.Lock()
		if wait, ok := c.inflight[id]; ok {
			c.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return &pb.CommandResult{ErrorCode: "canceled", ErrorMessage: "the command connection closed"}
			}
			continue
		}
		if e, ok := c.entries[id]; ok {
			c.mu.Unlock()
			if e.Result == nil {
				return errUnknownOutcome()
			}
			r := &pb.CommandResult{}
			if proto.Unmarshal(e.Result, r) != nil {
				return errUnknownOutcome()
			}
			return r
		}
		if reject != nil {
			c.mu.Unlock()
			return reject
		}
		done := make(chan struct{})
		c.inflight[id] = done
		c.entries[id] = &commandEntry{DeadlineMs: deadline}
		err := c.saveLocked()
		c.mu.Unlock()
		if err != nil {
			c.finish(id, done, nil)
			return &pb.CommandResult{ErrorCode: "internal", ErrorMessage: "cannot record the command: " + err.Error()}
		}
		r := fn()
		c.finish(id, done, r)
		return r
	}
}

// finish stores r for id, or forgets id if r is nil because the command
// never ran.
func (c *commandLog) finish(id string, done chan struct{}, r *pb.CommandResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r == nil {
		delete(c.entries, id)
	} else if b, err := proto.Marshal(r); err == nil {
		c.entries[id].Result = b
	}
	// A failed save leaves the command recorded as started, so a retry
	// after a restart reports an unknown outcome instead of running it.
	_ = c.saveLocked()
	delete(c.inflight, id)
	close(done)
}

func (c *commandLog) saveLocked() error {
	now := time.Now().UnixMilli()
	for id, e := range c.entries {
		if _, running := c.inflight[id]; !running && e.DeadlineMs < now-time.Minute.Milliseconds() {
			delete(c.entries, id)
		}
	}
	b, err := json.Marshal(c.entries)
	if err != nil {
		return err
	}
	return writeFileSync(c.path, b)
}
