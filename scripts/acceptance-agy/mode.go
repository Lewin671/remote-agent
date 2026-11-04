package main

import (
	"errors"

	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

// The native footer can still report the previous busy state after the
// composer is idle. Only this authenticated, pre-key refusal may be retried;
// an unknown delivery outcome must stop the run without another attempt.
func waitModeObservation(wait func(string, func() bool) error, attempt func() error) error {
	var result error
	if err := wait("fresh native mode observation", func() bool {
		result = attempt()
		var remote *remoteclient.CommandError
		return !errors.As(result, &remote) || remote.Message != "no status line mode observation"
	}); err != nil {
		return err
	}
	return result
}
