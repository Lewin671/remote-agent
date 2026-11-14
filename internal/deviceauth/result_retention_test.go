package deviceauth

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestWebResultRetentionMatchesDaemon(t *testing.T) {
	source, err := os.ReadFile("../../web/src/result-retention.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`RESULT_RETENTION_MS = ([0-9]+);`).FindSubmatch(source)
	if match == nil {
		t.Fatal("missing generated recovery window")
	}
	value, err := strconv.ParseInt(string(match[1]), 10, 64)
	if err != nil || value != ResultRetention.Milliseconds() {
		t.Fatal("web recovery window differs from daemon; run go generate ./internal/deviceauth")
	}
}
