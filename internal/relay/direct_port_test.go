package relay

import (
	"testing"

	"github.com/Lewin671/remote-agent/internal/transport/direct"
)

func TestDirectPortSetting(t *testing.T) {
	for value, want := range map[string]int{"": direct.DefaultPort, "0": 0, "50000": 50000, "x": 0, "70000": 0, "-1": 0} {
		if got := directPort(value); got != want {
			t.Errorf("directPort(%q) = %d, want %d", value, got, want)
		}
	}
}
