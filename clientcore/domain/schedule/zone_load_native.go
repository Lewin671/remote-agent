//go:build !(js && wasm)

package schedule

import (
	"time"
	_ "time/tzdata"
)

func hasNativeTZData() bool { return true }

func loadIANALocation(canon string) (*time.Location, error) {
	return time.LoadLocation(canon)
}
