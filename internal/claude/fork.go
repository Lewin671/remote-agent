package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CopiedFile lists, one per line, the uuid of every record the source's
// transcript held when a session was forked from it. Claude Code writes a
// fork's transcript with its first message, and starts it with the
// source's records under their own uuids (claude-code-notes §11).
const CopiedFile = "fork-copied"

// writeCopied saves the uuids of the records in the first upTo bytes of
// transcript, for Copied.
func writeCopied(dir, transcript string, upTo int64) error {
	f, err := os.Open(transcript)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.LimitReader(f, upTo), 64<<10)
	var out bytes.Buffer
	for {
		line, err := r.ReadBytes('\n')
		if id := recordUUID(line); id != "" {
			out.WriteString(id)
			out.WriteByte('\n')
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, CopiedFile), out.Bytes(), 0o600)
}

func recordUUID(line []byte) string {
	var r struct {
		UUID string `json:"uuid"`
	}
	if json.Unmarshal(line, &r) != nil {
		return ""
	}
	return r.UUID
}

// Copied tells the source's records in a fork's transcript by their uuid.
// Records without one (mode, queue operations) are the fork's own.
func (a *Adapter) Copied(dir string) func(line []byte) bool {
	b, err := os.ReadFile(filepath.Join(dir, CopiedFile))
	if err != nil {
		return nil
	}
	ids := map[string]bool{}
	for _, id := range strings.Fields(string(b)) {
		ids[id] = true
	}
	return func(line []byte) bool { return ids[recordUUID(line)] }
}
