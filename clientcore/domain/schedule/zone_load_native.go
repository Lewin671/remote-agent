//go:build !(js && wasm)

package schedule

import "time"

func loadIANALocation(canon string) (*time.Location, error) {
	return time.LoadLocation(canon)
}
