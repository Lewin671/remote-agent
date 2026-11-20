package transport

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Relay recovery and directed rate budgets must run on the monotonic clock:
// fed wall time, a one-hour step back refuses redials for about an hour.
func TestBudgetsUseMonotonicMilliseconds(t *testing.T) {
	// time.Time prints its monotonic reading as "m=±seconds".
	if !strings.Contains(processStart.String(), " m=") {
		t.Fatal("processStart has no monotonic reading")
	}
	if got := MonotonicMs(processStart.Add(1500 * time.Millisecond)); got != 1500 {
		t.Fatalf("MonotonicMs offset = %d, want 1500", got)
	}
	before := time.Since(processStart).Milliseconds()
	got := MonotonicMs(time.Now())
	if after := time.Since(processStart).Milliseconds(); got < before || got > after {
		t.Fatalf("MonotonicMs(time.Now()) = %d, want within [%d, %d]", got, before, after)
	}
}

// Every linkcore budget or retry call in the daemon must convert with
// MonotonicMs; passing UnixMilli reintroduces the wall-clock stall.
func TestLinkcoreBudgetCallsAvoidWallClock(t *testing.T) {
	call := regexp.MustCompile(`\.(Take|Failed|DelayMs)\([^\n]*\.UnixMilli\(\)`)
	checked := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "shared/linkcore") {
			return nil
		}
		checked++
		for i, line := range strings.Split(string(src), "\n") {
			if call.MatchString(line) {
				t.Errorf("%s:%d passes wall-clock milliseconds to linkcore: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 3 {
		t.Fatalf("only %d linkcore users found under internal/", checked)
	}
}
