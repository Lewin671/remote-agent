package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lewin671/remote-agent/internal/paths"
)

// Storage diagnostics are a local-only snapshot of runtime state. Keep them
// outside session directories so an unwritable event directory need not hide
// the diagnostic too. This is observability, never a recovery/commit source.
type storageDiagnostic struct {
	ObservedAt time.Time `json:"observed_at"`
}

func storageDiagnosticPath(stateDir, id string) string {
	return filepath.Join(stateDir, "diagnostics", id+".json")
}

func (s *session) saveStorageDiagnostic() error {
	path := storageDiagnosticPath(paths.Dir(), s.meta.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(storageDiagnostic{ObservedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	return writeFileSync(path, append(b, '\n'))
}

func (s *session) clearStorageDiagnostic() {
	if err := os.Remove(storageDiagnosticPath(paths.Dir(), s.meta.ID)); err != nil && !os.IsNotExist(err) {
		if s.storageWarnAt.IsZero() || time.Since(s.storageWarnAt) >= time.Minute {
			slog.Error("cannot clear local storage diagnostic", "session", s.meta.ID, "err", errClass(err))
			s.storageWarnAt = time.Now()
		}
	}
}

// ReadSessionDiagnostics reads files directly for local commands. These data
// must never be copied into pb.Session: local.proto is shared with clients.
func ReadSessionDiagnostics(stateDir, id string) (string, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return "", errors.New("invalid session ID")
	}
	h, parseErr := loadParseHealth(filepath.Join(stateDir, "sessions", id))
	var parts []string
	if parseErr == nil {
		if detail := h.detail(); detail != "" {
			parts = append(parts, detail)
		}
	}
	b, storageErr := os.ReadFile(storageDiagnosticPath(stateDir, id))
	if os.IsNotExist(storageErr) {
		storageErr = nil
	} else if storageErr == nil {
		var st storageDiagnostic
		storageErr = json.Unmarshal(b, &st)
		if storageErr == nil {
			parts = append(parts, fmt.Sprintf("storage failure (observed %s)", st.ObservedAt.Format(time.RFC3339)))
		}
	}
	return strings.Join(parts, "; "), errors.Join(parseErr, storageErr)
}
