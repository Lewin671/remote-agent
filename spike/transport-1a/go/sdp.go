package main

import (
	"example.com/transport-sdk-lab/labwire"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateSDP(raw []byte) error { return labwire.ValidateSDP(raw) }
func runSDPVectors(dir string) {
	manifest, err := os.ReadFile(filepath.Join(dir, "vectors.tsv"))
	must(err)
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			panic("vector row")
		}
		raw, err := os.ReadFile(filepath.Join(dir, parts[0]))
		must(err)
		if (validateSDP(raw) == nil) != (parts[1] == "1") {
			panic("vector " + parts[0])
		}
		count++
	}
	fmt.Printf("Go SDP vectors: %d passed\n", count)
}
