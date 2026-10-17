package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
)

func TestEnsureUTF8(t *testing.T) {
	for _, c := range []struct {
		name                 string
		lcAll, lcCtype, lang string
		wantAll, wantCtype   string
	}{
		{"none", "", "", "", "", utf8Locale()},
		{"utf-8 LANG", "", "", "zh_CN.UTF-8", "", ""},
		{"ascii LANG", "", "", "C", "", utf8Locale()},
		{"utf-8 LC_CTYPE", "", "en_US.utf8", "C", "", "en_US.utf8"},
		{"ascii LC_ALL", "C", "", "en_US.UTF-8", utf8Locale(), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LC_ALL", c.lcAll)
			t.Setenv("LC_CTYPE", c.lcCtype)
			t.Setenv("LANG", c.lang)
			ensureUTF8()
			if got := os.Getenv("LC_ALL"); got != c.wantAll {
				t.Errorf("LC_ALL = %q, want %q", got, c.wantAll)
			}
			if got := os.Getenv("LC_CTYPE"); got != c.wantCtype {
				t.Errorf("LC_CTYPE = %q, want %q", got, c.wantCtype)
			}
		})
	}
}

// An approval that had waited longer than the retry window when the daemon
// restarted is still retried for the whole window: it runs from the moment
// the daemon was lost.
func TestHookRetriesFromWhenTheDaemonWasLost(t *testing.T) {
	synctest.Test(t, testHookRetriesFromWhenTheDaemonWasLost)
}

func testHookRetriesFromWhenTheDaemonWasLost(t *testing.T) {
	down := connect.NewError(connect.CodeUnavailable, errors.New("daemon gone"))
	const retryFor, pause = 200 * time.Millisecond, 10 * time.Millisecond
	calls := 0
	out, err := forwardHook(context.Background(), retryFor, pause, func(context.Context) ([]byte, error) {
		calls++
		switch {
		case calls == 1:
			time.Sleep(2 * retryFor) // the approval waits, then the daemon stops
			return nil, down
		case calls < 4:
			return nil, down // it is starting again
		}
		return []byte("asked again"), nil
	})
	if err != nil || string(out) != "asked again" || calls != 4 {
		t.Fatalf("out %q, err %v after %d calls", out, err, calls)
	}

	// A daemon that stays away is given up on after the window, and other
	// errors are not retried.
	calls = 0
	start := time.Now()
	if _, err := forwardHook(context.Background(), retryFor, pause, func(context.Context) ([]byte, error) { calls++; return nil, down }); err == nil {
		t.Fatal("no error")
	}
	if took := time.Since(start); took < retryFor || took > 5*retryFor || calls < 2 {
		t.Fatalf("gave up after %v and %d calls", took, calls)
	}
	calls = 0
	missing := connect.NewError(connect.CodeNotFound, errors.New("no such session"))
	if _, err := forwardHook(context.Background(), retryFor, pause, func(context.Context) ([]byte, error) { calls++; return nil, missing }); err == nil || calls != 1 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}
