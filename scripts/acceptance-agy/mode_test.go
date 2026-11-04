package main

import (
	"errors"
	"testing"

	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

func TestModeObservationRetriesOnlyTheAuthenticatedPreKeyRefusal(t *testing.T) {
	for _, message := range []string{"no status line mode observation", "the relay supplied no authenticated outcome; retry the same request", "mode switch delivery unconfirmed"} {
		t.Run(message, func(t *testing.T) {
			first := &remoteclient.CommandError{Code: "unknown", Message: message}
			calls := 0
			wait := func(_ string, ready func() bool) error {
				if ready() {
					return nil
				}
				if ready() {
					return nil
				}
				return errors.New("still waiting")
			}
			err := waitModeObservation(wait, func() error {
				calls++
				if calls == 1 {
					return first
				}
				return nil
			})
			if message == "no status line mode observation" {
				if err != nil || calls != 2 {
					t.Fatalf("observation refusal was not retried: %d %v", calls, err)
				}
			} else if err != first || calls != 1 {
				t.Fatalf("unknown native outcome was retried: %d %v", calls, err)
			}
		})
	}
}
