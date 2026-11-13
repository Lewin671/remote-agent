package deviceauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCompletionEvidenceIndependentOfCancellation(t *testing.T) {
	for _, diagnostic := range []error{nil, context.Canceled, context.DeadlineExceeded} {
		for _, final := range []bool{false, true} {
			for _, closeBeforeCompletion := range []bool{false, true} {
				t.Run(fmtCompletion(diagnostic, final, closeBeforeCompletion), func(t *testing.T) {
					c := newCurrentChain(t)
					path := filepath.Join(t.TempDir(), "endpoint.db")
					m := c.machineEndpoint(t, path)
					defer m.Close()
					cl := c.clientEndpoint(t)
					defer cl.Close()
					now := uint64(time.Now().UnixMilli())
					w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "completion-order-test", Deadline: now + 60000}, nil, commandBody(t))
					if err != nil {
						t.Fatal(err)
					}
					entered, release := make(chan struct{}), make(chan struct{})
					done := make(chan error, 1)
					var first []byte
					go func() {
						var e error
						first, e = m.Accept(w, nil, now, func(Header, []byte) (Completion, error) {
							close(entered)
							<-release
							// This body could have been marshalled successfully in either case.
							completion := Completion{result: []byte("settled")}
							if final {
								completion = Final(completion.result)
							}
							return completion, diagnostic
						})
						done <- e
					}()
					<-entered
					if closeBeforeCompletion {
						if err := m.Close(); err != nil {
							t.Fatal(err)
						}
					}
					close(release)
					err = <-done
					wantFinal := final && !closeBeforeCompletion
					if wantFinal {
						if err != nil || string(first) != "settled" {
							t.Fatalf("first %q %v", first, err)
						}
					} else if !errors.Is(err, ErrOutcomeUnknown) {
						t.Fatalf("want unknown, got %v", err)
					}
					m.Close()
					m, err = OpenEndpoint(path)
					if err != nil {
						t.Fatal(err)
					}
					defer m.Close()
					out, err := m.Accept(w, nil, now, func(Header, []byte) (Completion, error) { t.Fatal("executed twice"); return Final(nil), nil })
					if wantFinal {
						if err != nil || string(out) != "settled" {
							t.Fatalf("retry %q %v", out, err)
						}
					} else if !errors.Is(err, ErrOutcomeUnknown) {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
func fmtCompletion(err error, final, closed bool) string {
	s := "nil"
	if err != nil {
		s = err.Error()
	}
	if final {
		s += "/final"
	} else {
		s += "/undetermined"
	}
	if closed {
		s += "/closed-before"
	} else {
		s += "/closed-after"
	}
	return s
}
