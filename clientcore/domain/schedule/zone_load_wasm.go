//go:build js && wasm

package schedule

import "time"

// On js/wasm without tzdata or zoneinfo filesystem, defer non-UTC resolution to the shell.
func loadIANALocation(canon string) (*time.Location, error) {
	if canon == zoneEtcUTC || canon == zoneEtcGMT {
		return time.UTC, nil
	}
	return nil, errInvalidZone
}
