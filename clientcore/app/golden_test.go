package app

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	stdcorev1 "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const updateGoldenCmd = "cd clientcore && CGO_ENABLED=0 go test ./app -run TestScreenGoldens -update"

var updateGolden = flag.Bool("update", false, "update testdata/screens/*.golden.pb golden files")

type screenProbe struct {
	Name   string          `json:"name"`
	Screen json.RawMessage `json:"screen"`
}

type fixtureStep struct {
	Event json.RawMessage `json:"event"`
	Views []screenProbe   `json:"views,omitempty"`
}

type fixtureInput struct {
	Steps []fixtureStep `json:"steps"`
}

func stdJSONToLite[Std any, PStd interface {
	*Std
	proto.Message
}, Lite any, PLite interface {
	*Lite
	Unmarshal([]byte) error
}](t testing.TB, raw json.RawMessage) PLite {
	t.Helper()
	var std Std
	if err := protojson.Unmarshal(raw, PStd(&std)); err != nil {
		t.Fatalf("protojson unmarshal %T: %v\ninput=%s", std, err, string(raw))
	}
	wire, err := proto.Marshal(PStd(&std))
	if err != nil {
		t.Fatalf("proto marshal %T: %v", std, err)
	}
	var lite Lite
	if err := PLite(&lite).Unmarshal(wire); err != nil {
		t.Fatalf("lite unmarshal %T: %v", lite, err)
	}
	return &lite
}

func liteToStd[Lite interface {
	Marshal() ([]byte, error)
}, Std any, PStd interface {
	*Std
	proto.Message
}](t testing.TB, l Lite) PStd {
	t.Helper()
	wire, err := l.Marshal()
	if err != nil {
		t.Fatalf("lite marshal %T: %v", l, err)
	}
	var std Std
	if err := proto.Unmarshal(wire, PStd(&std)); err != nil {
		t.Fatalf("std unmarshal %T: %v", std, err)
	}
	return PStd(&std)
}

func formatGoldenDiffJSON(msg proto.Message) string {
	out, err := (protojson.MarshalOptions{
		Multiline: true,
		Indent:    "  ",
	}).Marshal(msg)
	if err != nil {
		return "<marshal error: " + err.Error() + ">"
	}
	return string(out)
}

func TestScreenGoldens(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "screens")
	inputs, err := filepath.Glob(filepath.Join(dir, "*.input.json"))
	if err != nil {
		t.Fatalf("glob inputs: %v", err)
	}
	if len(inputs) == 0 {
		t.Fatalf("no *.input.json fixtures found in %s", dir)
	}
	sort.Strings(inputs)

	detMarshal := proto.MarshalOptions{Deterministic: true}

	for _, inPath := range inputs {
		base := filepath.Base(inPath)
		name := base[:len(base)-len(".input.json")]
		goldenPBPath := filepath.Join(dir, name+".golden.pb")

		t.Run(name, func(t *testing.T) {
			rawIn, err := os.ReadFile(inPath)
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			var in fixtureInput
			if err := json.Unmarshal(rawIn, &in); err != nil {
				t.Fatalf("parse input: %v", err)
			}

			c := New()
			defer c.Close()

			actualPB := &stdcorev1.ScreenGoldenFile{
				Steps: make([]*stdcorev1.ScreenGoldenStep, 0, len(in.Steps)),
			}
			seenViews := make(map[string]int)

			for i, step := range in.Steps {
				ev := stdJSONToLite[stdcorev1.Event, *stdcorev1.Event, corev1.Event, *corev1.Event](t, step.Event)
				effects := c.Dispatch(ev)
				stdEffects := liteToStd[*corev1.EffectBatch, stdcorev1.EffectBatch](t, effects)
				stepPB := &stdcorev1.ScreenGoldenStep{
					Effects: stdEffects,
				}
				if len(step.Views) > 0 {
					// Sort view probes by name within a step so binary slice order is
					// deterministic and matches sorted-key iteration on all clients.
					probes := append([]screenProbe(nil), step.Views...)
					sort.Slice(probes, func(a, b int) bool { return probes[a].Name < probes[b].Name })
					stepPB.Views = make([]*stdcorev1.ScreenGoldenView, 0, len(probes))
					for _, probe := range probes {
						if probe.Name == "" {
							t.Fatalf("step %d has empty view probe name", i)
						}
						if prevStep, dup := seenViews[probe.Name]; dup {
							t.Fatalf("duplicate view name %q (first at step %d, repeated at step %d)", probe.Name, prevStep, i)
						}
						seenViews[probe.Name] = i
						sc := stdJSONToLite[stdcorev1.Screen, *stdcorev1.Screen, corev1.Screen, *corev1.Screen](t, probe.Screen)
						vm, err := c.View(sc)
						if err != nil {
							t.Fatalf("step %d view %s: %v", i, probe.Name, err)
						}
						stdVM := liteToStd[*corev1.ViewModel, stdcorev1.ViewModel](t, vm)
						stepPB.Views = append(stepPB.Views, &stdcorev1.ScreenGoldenView{
							Name:      probe.Name,
							ViewModel: stdVM,
						})
					}
				}
				actualPB.Steps = append(actualPB.Steps, stepPB)
			}

			if *updateGolden {
				gotPBBytes, err := detMarshal.Marshal(actualPB)
				if err != nil {
					t.Fatalf("marshal actual binary golden: %v", err)
				}
				if err := os.WriteFile(goldenPBPath, gotPBBytes, 0o644); err != nil {
					t.Fatalf("write golden pb: %v", err)
				}
				return
			}

			wantPBBytes, err := os.ReadFile(goldenPBPath)
			if err != nil {
				t.Fatalf("read binary golden %s (run `%s` to generate): %v", goldenPBPath, updateGoldenCmd, err)
			}
			var wantPB stdcorev1.ScreenGoldenFile
			if err := proto.Unmarshal(wantPBBytes, &wantPB); err != nil {
				t.Fatalf("unmarshal binary golden %s (run `%s` to regenerate): %v", goldenPBPath, updateGoldenCmd, err)
			}
			if !proto.Equal(&wantPB, actualPB) {
				t.Fatalf("binary golden mismatch for %s (run `%s` to update):\n--- got ---\n%s\n--- want ---\n%s",
					goldenPBPath, updateGoldenCmd, formatGoldenDiffJSON(actualPB), formatGoldenDiffJSON(&wantPB))
			}
		})
	}
}
