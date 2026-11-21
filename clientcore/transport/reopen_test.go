package transport

import (
	"testing"

	"github.com/Lewin671/remote-agent/shared/linkcore"
)

// Guards the terminal stream's reopen rule: it waits for the main connection,
// backs off with full jitter while reopening keeps failing, starts over after
// five stable seconds, and shares the process budget with other streams.
func TestReopen(t *testing.T) {
	var b linkcore.Budget
	var r Reopen
	now := int64(0)
	if w := r.Closed(now, false, true, 0.5, &b); w.Open {
		t.Fatal("a stream that never opened waits for the main connection")
	}
	if w := r.MainOnline(now, true, &b); !w.Open {
		t.Fatal("reopens at once when the main connection is back")
	}
	// A transient open failure during an active reopen streak keeps backing off and retrying.
	if w := r.Closed(now, false, true, 0.5, &b); w.Open || !w.Set || w.At != now+500 {
		t.Fatalf("transient reopen failure must schedule backoff timer at +500ms, got %+v", w)
	}
	now += 500
	if w := r.Timer(now, true, &b); !w.Open {
		t.Fatalf("expected reopen after transient streak timer at %d, got %+v", now, w)
	}
	// Reset streak cleanly and verify opened closures still back off via 0.5 x 1 s, 0.5 x 2 s.
	w := r.Live(now)
	now = w.At
	r.Timer(now, true, &b)
	now += 2000
	if w := r.Closed(now, true, true, 0.5, &b); !w.Open {
		t.Fatal("first close after stability reopens at once")
	}
	for _, wait := range []int64{500, 1000} {
		w := r.Closed(now, true, true, 0.5, &b)
		if w.Open || !w.Set || w.At != now+wait {
			t.Fatalf("wait %d: %+v", wait, w)
		}
		if w := r.Timer(now+wait-1, true, &b); w.Open {
			t.Fatal("not before the wait")
		}
		now += wait
		if w := r.Timer(now, true, &b); !w.Open {
			t.Fatalf("opens after %d", wait)
		}
	}
	w = r.Live(now)
	now = w.At
	r.Timer(now, true, &b)
	now += 2000
	if w := r.Closed(now, true, true, 0.5, &b); !w.Open {
		t.Fatal("a stream that was stable reopens without waiting")
	}
	if w := r.Manual(); !w.Open {
		t.Fatal("manual reconnect opens now")
	}

	var shared linkcore.Budget
	streams := make([]Reopen, 5)
	opened := 0
	var at int64
	for i := range streams {
		w := streams[i].Closed(0, true, true, 0.5, &shared)
		if w.Open {
			opened++
		} else {
			at = w.At
		}
	}
	if opened != 4 || at != 500 || !streams[4].Timer(500, true, &shared).Open {
		t.Fatalf("five streams share the 4-token process budget: %d opened, fifth at %d", opened, at)
	}
}
