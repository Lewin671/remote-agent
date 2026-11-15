package labwire

import "testing"

func TestSenderAndBatcher(t *testing.T) {
	check := func(ok bool) {
		t.Helper()
		if !ok {
			t.Fatal("sender/batcher invariant")
		}
	}
	s := NewSender(10000)
	var rows [][]byte
	for i := 0; i < 33; i++ {
		rows = append(rows, []byte{byte(i)})
	}
	for _, r := range rows[:32] {
		check(s.Enqueue(r, 0))
	}
	check(!s.Enqueue(rows[32], 0))
	check(len(s.Tick(0)) == 32)
	check(len(s.Tick(499)) == 0)
	check(len(s.Tick(500)) == 32)
	check(len(s.Tick(1500)) == 32)
	check(len(s.Tick(2000)) == 0)
	for _, r := range rows[:32] {
		check(s.Ack(Hash(r)))
	}
	check(s.Enqueue(rows[32], 2000))
	check(len(s.Tick(2000)) == 1)
	s.Limited(Hash(rows[32]), 2000, 9000)
	check(len(s.Tick(9999)) == 0)
	check(len(s.Tick(10000)) == 0 && s.TimedOut && len(s.Pending) == 0)
	check(s.Backoff(10000, .5) == 10500)
	for i := uint64(1); i <= 1000; i++ {
		s.NetworkChanged(i)
	}
	check(s.RetryAt == 10500)
	check(s.Backoff(10500, .5) == 11500)
	p := NewSender(10000)
	check(p.Enqueue(rows[0], 0))
	check(len(p.Tick(0)) == 1)
	p.Limited(Hash(rows[0]), 0, 800)
	check(len(p.Tick(500)) == 0)
	check(len(p.Tick(800)) == 1)
	check(len(p.Tick(1500)) == 1)
	p.Limited(Hash(rows[0]), 10001, 5)
	check(len(p.Tick(10001)) == 0)
	b := CandidateBatcher{Generation: 2}
	check(!b.Add([]byte{1}, 1, 0))
	for i := 0; i < 64; i++ {
		check(b.Add([]byte{byte(i)}, 2, 0))
	}
	check(!b.Add([]byte{65}, 2, 0))
	check(len(b.Flush(49, false)) == 0)
	check(len(b.Flush(50, false)) == 4)
	check(len(b.Flush(51, true)) == 0)
	check(!b.Add([]byte{1}, 2, 52))
}
