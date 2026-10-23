package deviceauth

import (
	"bytes"
	"compress/flate"
	"fmt"
	"math/rand"
	"sync"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func randomPackFixture(size int) []byte {
	random := make([]byte, size)
	_, _ = rand.New(rand.NewSource(1)).Read(random)
	return random
}

func packFixtures(t *testing.T) [][]byte {
	t.Helper()
	event, err := proto.Marshal(&pb.Event{
		SessionId: "session", Seq: 1,
		Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: string(bytes.Repeat([]byte("CLI test output\n"), 128))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return [][]byte{
		nil, []byte("small"), bytes.Repeat([]byte("x"), 256), bytes.Repeat([]byte("x"), 257),
		event, bytes.Repeat([]byte("test output\n"), 700), bytes.Repeat([]byte("terminal\n"), 7300), randomPackFixture(4096),
	}
}

func freshPack(t *testing.T, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := flate.NewWriter(&out, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if out.Len() >= len(plain) {
		return nil
	}
	return out.Bytes()
}

func TestPackMatchesFreshWriter(t *testing.T) {
	fixtures := packFixtures(t)
	for i, plain := range fixtures {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			want := freshPack(t, plain)
			// Alternate streams to catch dictionary or output left by a previous use.
			for _, other := range fixtures {
				pack(other)
				got := pack(plain)
				if !bytes.Equal(got, want) || (got == nil) != (want == nil) {
					t.Fatal("pooled output differs from a fresh writer")
				}
				if got != nil {
					decoded, err := Unpack(Header{Purpose: Event, Compression: 1}, got)
					if err != nil || !bytes.Equal(decoded, plain) {
						t.Fatalf("round trip: %v", err)
					}
				}
			}
		})
	}
}

func TestPackReturnedBytesOwned(t *testing.T) {
	plain := bytes.Repeat([]byte("original stream\n"), 128)
	got := pack(plain)
	want := freshPack(t, plain)
	if got == nil {
		t.Fatal("expected compression")
	}
	for i := range 32 {
		next := pack(bytes.Repeat([]byte{byte(i)}, 4096))
		if !bytes.Equal(got, want) {
			t.Fatal("subsequent compression overwrote returned bytes")
		}
		// Callers may also modify a returned slice without affecting other results.
		for j := range next {
			next[j] ^= 0xff
		}
		if !bytes.Equal(got, want) {
			t.Fatal("returned slices alias each other")
		}
	}
}

func TestPackConcurrent(t *testing.T) {
	fixtures := packFixtures(t)
	want := make([][]byte, len(fixtures))
	for i, plain := range fixtures {
		want[i] = freshPack(t, plain)
	}
	const workers = 16
	results := make([][][]byte, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Go(func() {
			<-start
			results[worker] = make([][]byte, len(fixtures))
			for i := range fixtures {
				index := (i + worker) % len(fixtures)
				results[worker][index] = pack(fixtures[index])
			}
		})
	}
	close(start)
	wg.Wait()
	// Check retained results after every writer has been returned and reused.
	for worker, outputs := range results {
		for i, got := range outputs {
			if !bytes.Equal(got, want[i]) || (got == nil) != (want[i] == nil) {
				t.Fatalf("worker %d fixture %d: output differs from a fresh writer", worker, i)
			}
		}
	}
}

func TestPackBufferCapacity(t *testing.T) {
	for _, capacity := range []int{1024, maxPackBufferCapacity, maxPackBufferCapacity + 1} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			c := &packCompressor{out: *bytes.NewBuffer(make([]byte, capacity))}
			c.resetBuffer()
			if c.out.Len() != 0 {
				t.Fatal("buffer contents retained")
			}
			want := capacity
			if capacity > maxPackBufferCapacity {
				want = 0
			}
			if c.out.Cap() != want {
				t.Fatalf("retained capacity %d, want %d", c.out.Cap(), want)
			}
		})
	}
}

func TestPackOversizedOutput(t *testing.T) {
	plain := append(randomPackFixture(maxPackBufferCapacity+4096), bytes.Repeat([]byte("x"), 65536)...)
	want := freshPack(t, plain)
	if len(want) <= maxPackBufferCapacity {
		t.Fatal("fixture must compress to more than the retained buffer cap")
	}
	got := pack(plain)
	next := bytes.Repeat([]byte("next stream\n"), 200)
	if !bytes.Equal(pack(next), freshPack(t, next)) {
		t.Fatal("compression after an oversized output differs from a fresh writer")
	}
	if !bytes.Equal(got, want) {
		t.Fatal("oversized output was lost or overwritten when its buffer was discarded")
	}
}

func TestPackAllocations(t *testing.T) {
	for _, plain := range [][]byte{bytes.Repeat([]byte("CLI output\n"), 200), randomPackFixture(4096)} {
		t.Run(fmt.Sprint(len(plain)), func(t *testing.T) {
			var result []byte
			fresh := testing.AllocsPerRun(50, func() { result = freshPack(t, plain) })
			reused := testing.AllocsPerRun(50, func() { result = pack(plain) })
			if reused >= fresh*0.75 {
				t.Fatalf("allocations not reduced: fresh=%.1f pooled=%.1f", fresh, reused)
			}
			if !bytes.Equal(result, freshPack(t, plain)) {
				t.Fatal("allocation check changed output")
			}
			t.Logf("allocations/attempt: fresh=%.1f pooled=%.1f", fresh, reused)
		})
	}
}
