package antigravity

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const sampleAgyModelsOutput = `⠋ Fetching available models...⠙ Fetching available models...
gemini-4-argon-medium     Gemini 4 Argon (Medium)
gemini-4-argon-high       Gemini 4 Argon (High)
gemini-4-argon-low        Gemini 4 Argon (Low)
gemini-3.8-flash-high     Gemini 3.8 Flash (High)
gemini-3.8-flash-medium   Gemini 3.8 Flash (Medium)
gemini-3.8-flash-low      Gemini 3.8 Flash (Low)
claude-opus-5-5-low       Claude Opus 5.5 (Low)
claude-opus-5-5-medium    Claude Opus 5.5 (Medium)
claude-opus-5-5-high      Claude Opus 5.5 (High)
claude-sonnet-5-5-low     Claude Sonnet 5.5 (Low)
claude-sonnet-5-5-medium  Claude Sonnet 5.5 (Medium)
claude-sonnet-5-5-high    Claude Sonnet 5.5 (High)
gpt-oss-120b-medium       GPT-OSS 120B (Medium)
`

func TestParseModels(t *testing.T) {
	models, err := ParseModels(sampleAgyModelsOutput, true)
	if err != nil {
		t.Fatalf("ParseModels failed: %v", err)
	}

	expectedSlugs := []string{
		"gemini-4-argon",
		"gemini-3.8-flash",
		"claude-opus-5-5",
		"claude-sonnet-5-5",
		"gpt-oss-120b",
	}

	if len(models) != len(expectedSlugs) {
		t.Fatalf("expected %d models, got %d", len(expectedSlugs), len(models))
	}

	for i, slug := range expectedSlugs {
		if models[i].Slug != slug {
			t.Errorf("expected slug %q at index %d, got %q", slug, i, models[i].Slug)
		}
		if !models[i].AutoUnavailable {
			t.Errorf("expected AutoUnavailable=true for %s", slug)
		}
	}

	// Verify gemini-4-argon
	argon := models[0]
	if argon.DisplayName != "Gemini 4 Argon" {
		t.Errorf("expected 'Gemini 4 Argon', got %q", argon.DisplayName)
	}
	expectedEfforts := []string{"low", "medium", "high"}
	if !slices.Equal(argon.Efforts, expectedEfforts) {
		t.Errorf("expected efforts %v, got %v", expectedEfforts, argon.Efforts)
	}
	if argon.DefaultEffort != "medium" {
		t.Errorf("expected default effort 'medium', got %q", argon.DefaultEffort)
	}
	if argon.ContextWindow != 0 {
		t.Errorf("expected no inferred context window, got %d", argon.ContextWindow)
	}

	// Verify gpt-oss-120b
	gpt := models[4]
	if gpt.DisplayName != "GPT-OSS 120B" {
		t.Errorf("expected 'GPT-OSS 120B', got %q", gpt.DisplayName)
	}
	if !slices.Equal(gpt.Efforts, []string{"medium"}) {
		t.Errorf("expected efforts ['medium'], got %v", gpt.Efforts)
	}
	if gpt.ContextWindow != 0 {
		t.Errorf("expected no inferred context window, got %d", gpt.ContextWindow)
	}
}

func TestCheckEffort(t *testing.T) {
	for _, e := range []string{"", "low", "medium", "high"} {
		if err := CheckEffort(e); err != nil {
			t.Errorf("CheckEffort(%q) returned unexpected error: %v", e, err)
		}
	}
	for _, e := range []string{"max", "xhigh", "turbo", "invalid"} {
		if err := CheckEffort(e); err == nil {
			t.Errorf("CheckEffort(%q) expected error, got nil", e)
		}
	}
}

func TestModelCacheClonesAndUpdatesAccountFlags(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	script := `#!/bin/sh
[ "$RA_AGY_METADATA_TEST" = configured ] || exit 3
case "$1" in
--version) echo 'Antigravity CLI 1.4.12';;
models) printf '%s\n' 'gemini-3.8-flash-low Gemini 3.8 Flash (Low)' 'gemini-3.8-flash-medium Gemini 3.8 Flash (Medium)';;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := New(bin, dir)
	a.SetCommandEnv(map[string]string{"RA_AGY_METADATA_TEST": "configured"})
	if a.Version() != "1.4.12" {
		t.Fatal("metadata command did not receive agent environment")
	}
	mustWrite(t, filepath.Join(dir, "settings.json"), []byte(`{"model":"Gemini 3.8 Flash (Low)"}`))
	list, err := a.Models()
	if err != nil {
		t.Fatal(err)
	}
	if list.DefaultModel != "gemini-3.8-flash" {
		t.Fatal(list)
	}
	list.Models[0].Slug = "mutated"
	a.SetAutoUnavailable(true)
	list, err = a.Models()
	if err != nil {
		t.Fatal(err)
	}
	if list.Models[0].Slug != "gemini-3.8-flash" || !list.Models[0].AutoUnavailable {
		t.Fatal("cache was mutable or auto flag stale")
	}
}
