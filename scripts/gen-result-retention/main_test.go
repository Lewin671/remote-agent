package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedClientRetentionMatchesPolicy(t *testing.T) {
	for language, path := range map[string]string{
		"typescript": "web/src/result-retention.gen.ts",
		"kotlin":     "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/ResultRetention.gen.kt",
		"swift":      "ios/RemoteAgentCore/Sources/RACore/ResultRetention.gen.swift",
	} {
		t.Run(language, func(t *testing.T) {
			got, err := os.ReadFile(filepath.Join("../..", path))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != generated(language) {
				t.Fatalf("regenerate %s with scripts/gen-result-retention -language %s -output %s", path, language, path)
			}
		})
	}
}
