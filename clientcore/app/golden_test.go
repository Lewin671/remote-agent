package app

import (
	"bytes"
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

var updateGolden = flag.Bool("update", false, "update testdata/screens golden files")

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

type goldenStepOutput struct {
	Effects json.RawMessage            `json:"effects"`
	Views   map[string]json.RawMessage `json:"views,omitempty"`
}

type goldenFile struct {
	Steps []goldenStepOutput `json:"steps"`
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

func stdToCompactJSON(t testing.TB, msg proto.Message) json.RawMessage {
	t.Helper()
	out, err := (protojson.MarshalOptions{
		UseProtoNames: false,
		Indent:        "  ",
	}).Marshal(msg)
	if err != nil {
		t.Fatalf("protojson marshal %T: %v", msg, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, out); err != nil {
		t.Fatalf("compact json: %v", err)
	}
	return compact.Bytes()
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
		goldenPath := filepath.Join(dir, name+".golden.json")
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

			var actual goldenFile
			actualPB := &stdcorev1.ScreenGoldenFile{
				Steps: make([]*stdcorev1.ScreenGoldenStep, 0, len(in.Steps)),
			}

			for i, step := range in.Steps {
				ev := stdJSONToLite[stdcorev1.Event, *stdcorev1.Event, corev1.Event, *corev1.Event](t, step.Event)
				effects := c.Dispatch(ev)
				stdEffects := liteToStd[*corev1.EffectBatch, stdcorev1.EffectBatch](t, effects)
				stepOut := goldenStepOutput{
					Effects: stdToCompactJSON(t, stdEffects),
				}
				stepPB := &stdcorev1.ScreenGoldenStep{
					Effects: stdEffects,
				}
				if len(step.Views) > 0 {
					stepOut.Views = make(map[string]json.RawMessage, len(step.Views))
					// Sort view probes by name within a step so binary slice order is
					// deterministic and matches sorted-key iteration on all clients.
					probes := append([]screenProbe(nil), step.Views...)
					sort.Slice(probes, func(a, b int) bool { return probes[a].Name < probes[b].Name })
					stepPB.Views = make([]*stdcorev1.ScreenGoldenView, 0, len(probes))
					for _, probe := range probes {
						sc := stdJSONToLite[stdcorev1.Screen, *stdcorev1.Screen, corev1.Screen, *corev1.Screen](t, probe.Screen)
						vm, err := c.View(sc)
						if err != nil {
							t.Fatalf("step %d view %s: %v", i, probe.Name, err)
						}
						stdVM := liteToStd[*corev1.ViewModel, stdcorev1.ViewModel](t, vm)
						stepOut.Views[probe.Name] = stdToCompactJSON(t, stdVM)
						stepPB.Views = append(stepPB.Views, &stdcorev1.ScreenGoldenView{
							Name:      probe.Name,
							ViewModel: stdVM,
						})
					}
				}
				actual.Steps = append(actual.Steps, stepOut)
				actualPB.Steps = append(actualPB.Steps, stepPB)
			}

			gotBytes, err := json.MarshalIndent(actual, "", "  ")
			if err != nil {
				t.Fatalf("marshal actual golden: %v", err)
			}
			gotBytes = append(gotBytes, '\n')

			gotPBBytes, err := detMarshal.Marshal(actualPB)
			if err != nil {
				t.Fatalf("marshal actual binary golden: %v", err)
			}

			if *updateGolden {
				if err := os.WriteFile(goldenPath, gotBytes, 0o644); err != nil {
					t.Fatalf("write golden json: %v", err)
				}
				if err := os.WriteFile(goldenPBPath, gotPBBytes, 0o644); err != nil {
					t.Fatalf("write golden pb: %v", err)
				}
				return
			}

			wantBytes, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s (run with -update to create): %v", goldenPath, err)
			}
			if !bytes.Equal(gotBytes, wantBytes) {
				t.Fatalf("golden mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", goldenPath, string(gotBytes), string(wantBytes))
			}

			wantPBBytes, err := os.ReadFile(goldenPBPath)
			if err != nil {
				t.Fatalf("read binary golden %s (run with -update to create): %v", goldenPBPath, err)
			}
			if !bytes.Equal(gotPBBytes, wantPBBytes) {
				t.Fatalf("binary golden mismatch for %s (run `cd clientcore && go test ./app -run TestScreenGoldens -update`)", goldenPBPath)
			}

			// Verify binary golden round-trips cleanly and matches the JSON views.
			var decoded stdcorev1.ScreenGoldenFile
			if err := proto.Unmarshal(wantPBBytes, &decoded); err != nil {
				t.Fatalf("unmarshal binary golden %s: %v", goldenPBPath, err)
			}
			if !proto.Equal(&decoded, actualPB) {
				t.Fatalf("binary golden %s decoded message differs from expected", goldenPBPath)
			}
		})
	}
}
