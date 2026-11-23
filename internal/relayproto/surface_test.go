package relayproto_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/relayproto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var (
	initGolden     = flag.Bool("init", false, "create relay-visible wire surface golden file when missing")
	updateGolden   = flag.Bool("update", false, "update existing relay-visible wire surface golden file")
	additiveReason = flag.String("additive", "", "reason why additive relay surface changes do not bump relayproto.Version")
)

const goldenRelPath = "testdata/relay_surface.golden"

type surfaceGolden struct {
	RelayVersion int
	Additive     []string
	Entries      map[string]string
}

func currentRelaySurface(t *testing.T) map[string]string {
	t.Helper()
	entries := map[string]string{}

	collectFile(entries, pb.File_remoteagent_v1_relay_proto)
	collectFile(entries, pb.File_remoteagent_v1_envelope_proto)

	// From transport.proto, only the relay-visible outer framing types belong
	// to the relay surface; inner encrypted peerwire payloads are opaque to the relay.
	tf := pb.File_remoteagent_v1_transport_proto
	for _, enumName := range []protoreflect.Name{"FrameClass"} {
		ed := tf.Enums().ByName(enumName)
		if ed == nil {
			t.Fatalf("transport.proto missing enum %s", enumName)
		}
		collectEnum(entries, ed)
	}
	for _, msgName := range []protoreflect.Name{"DirectedPeerFrame", "PeerFrameDelivery", "PeerFrameResult"} {
		md := tf.Messages().ByName(msgName)
		if md == nil {
			t.Fatalf("transport.proto missing message %s", msgName)
		}
		collectMessage(entries, md)
	}

	collectDirectedPeerFrameSchema(t, entries)
	return entries
}

func collectFile(entries map[string]string, fd protoreflect.FileDescriptor) {
	for i := 0; i < fd.Enums().Len(); i++ {
		collectEnum(entries, fd.Enums().Get(i))
	}
	for i := 0; i < fd.Messages().Len(); i++ {
		collectMessage(entries, fd.Messages().Get(i))
	}
	for i := 0; i < fd.Services().Len(); i++ {
		sd := fd.Services().Get(i)
		for j := 0; j < sd.Methods().Len(); j++ {
			md := sd.Methods().Get(j)
			in := string(md.Input().FullName())
			if md.IsStreamingClient() {
				in = "stream " + in
			}
			out := string(md.Output().FullName())
			if md.IsStreamingServer() {
				out = "stream " + out
			}
			entries[fmt.Sprintf("rpc %s.%s", sd.FullName(), md.Name())] = in + "->" + out
		}
	}
}

func collectEnum(entries map[string]string, ed protoreflect.EnumDescriptor) {
	for i := 0; i < ed.Values().Len(); i++ {
		vd := ed.Values().Get(i)
		entries[fmt.Sprintf("enum %s.%d", ed.FullName(), vd.Number())] = string(vd.Name())
	}
}

func collectMessage(entries map[string]string, md protoreflect.MessageDescriptor) {
	if md.IsMapEntry() {
		return
	}
	for i := 0; i < md.Enums().Len(); i++ {
		collectEnum(entries, md.Enums().Get(i))
	}
	for i := 0; i < md.Messages().Len(); i++ {
		collectMessage(entries, md.Messages().Get(i))
	}
	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		entries[fmt.Sprintf("field %s.%d", md.FullName(), fd.Number())] = formatFieldType(fd)
	}
}

func formatFieldType(fd protoreflect.FieldDescriptor) string {
	if fd.IsMap() {
		return fmt.Sprintf("map<%s,%s>", formatScalarOrNamed(fd.MapKey()), formatScalarOrNamed(fd.MapValue()))
	}
	var prefix string
	switch {
	case fd.Cardinality() == protoreflect.Repeated:
		prefix = "repeated "
	case fd.HasOptionalKeyword():
		prefix = "optional "
	}
	spec := prefix + formatScalarOrNamed(fd)
	if oo := fd.ContainingOneof(); oo != nil && !oo.IsSynthetic() {
		spec += "@oneof(" + string(oo.Name()) + ")"
	}
	return spec
}

func formatScalarOrNamed(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return fmt.Sprintf("message(%s)", fd.Message().FullName())
	case protoreflect.EnumKind:
		return fmt.Sprintf("enum(%s)", fd.Enum().FullName())
	default:
		return fd.Kind().String()
	}
}

var (
	directedSchemaLineRe  = regexp.MustCompile(`(?m)^\s*"DirectedPeerFrame":\s*\{(.*)\},\s*$`)
	directedSchemaEntryRe = regexp.MustCompile(`\{\s*(\d+)\s*,\s*"([^"]+)"\s*,\s*(true|false)\s*\}`)
)

func collectDirectedPeerFrameSchema(t *testing.T, entries map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "shared", "peerwire", "schema_data.go"))
	if err != nil {
		t.Fatalf("read shared/peerwire/schema_data.go: %v", err)
	}
	lineMatch := directedSchemaLineRe.FindSubmatch(raw)
	if len(lineMatch) != 2 {
		t.Fatal("DirectedPeerFrame schema rule not found in shared/peerwire/schema_data.go")
	}
	matches := directedSchemaEntryRe.FindAllSubmatch(lineMatch[1], -1)
	if len(matches) == 0 {
		t.Fatal("DirectedPeerFrame schema rule has no entries")
	}
	for _, m := range matches {
		fieldNum := string(m[1])
		ruleType := string(m[2])
		if string(m[3]) == "true" {
			ruleType += ":repeated"
		}
		entries["schema DirectedPeerFrame."+fieldNum] = ruleType
	}
}

func parseGolden(raw []byte) (surfaceGolden, error) {
	g := surfaceGolden{Entries: map[string]string{}}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "relay_version:") {
			vStr := strings.TrimSpace(strings.TrimPrefix(line, "relay_version:"))
			v, err := strconv.Atoi(vStr)
			if err != nil || v < 1 {
				return g, fmt.Errorf("line %d: invalid relay_version %q", i+1, vStr)
			}
			g.RelayVersion = v
			continue
		}
		if strings.HasPrefix(line, "additive:") {
			reason := strings.TrimSpace(strings.TrimPrefix(line, "additive:"))
			if reason == "" {
				return g, fmt.Errorf("line %d: empty additive reason", i+1)
			}
			g.Additive = append(g.Additive, reason)
			continue
		}
		sep := ":"
		if strings.HasPrefix(line, "enum ") {
			sep = "="
		}
		k, v, ok := strings.Cut(line, sep)
		if !ok || k == "" || v == "" {
			return g, fmt.Errorf("line %d: malformed entry %q", i+1, line)
		}
		if _, dup := g.Entries[k]; dup {
			return g, fmt.Errorf("line %d: duplicate entry %q", i+1, k)
		}
		g.Entries[k] = v
	}
	if g.RelayVersion < 1 {
		return g, fmt.Errorf("missing relay_version header")
	}
	return g, nil
}

func formatGolden(g surfaceGolden) string {
	var b strings.Builder
	b.WriteString("# Relay-visible wire surface fingerprint.\n")
	b.WriteString("# Regenerate with: go test ./internal/relayproto -update [-additive \"<reason>\"]\n")
	fmt.Fprintf(&b, "relay_version: %d\n", g.RelayVersion)
	for _, reason := range g.Additive {
		fmt.Fprintf(&b, "additive: %s\n", reason)
	}
	b.WriteString("\n")
	keys := make([]string, 0, len(g.Entries))
	for k := range g.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sep := ":"
		if strings.HasPrefix(k, "enum ") {
			sep = "="
		}
		fmt.Fprintf(&b, "%s%s%s\n", k, sep, g.Entries[k])
	}
	return b.String()
}

func diffSurface(oldEntries, newEntries map[string]string) (breaking, added []string) {
	for k, oldV := range oldEntries {
		newV, ok := newEntries[k]
		if !ok {
			breaking = append(breaking, fmt.Sprintf("removed %s (%s)", k, oldV))
		} else if newV != oldV {
			breaking = append(breaking, fmt.Sprintf("changed %s: %s -> %s", k, oldV, newV))
		}
	}
	for k, newV := range newEntries {
		if _, ok := oldEntries[k]; !ok {
			added = append(added, fmt.Sprintf("added %s (%s)", k, newV))
		}
	}
	sort.Strings(breaking)
	sort.Strings(added)
	return breaking, added
}

func planGoldenUpdate(old surfaceGolden, newEntries map[string]string, newVersion int, additive string) (surfaceGolden, error) {
	breaking, added := diffSurface(old.Entries, newEntries)
	updated := surfaceGolden{
		RelayVersion: newVersion,
		Entries:      newEntries,
	}
	if newVersion == old.RelayVersion {
		if len(breaking) > 0 {
			return surfaceGolden{}, fmt.Errorf("breaking relay-visible changes require bumping relayproto.Version (still %d):\n  %s",
				newVersion, strings.Join(breaking, "\n  "))
		}
		updated.Additive = append([]string(nil), old.Additive...)
		if len(added) > 0 {
			reason := strings.TrimSpace(additive)
			if reason == "" {
				return surfaceGolden{}, fmt.Errorf("additive relay-visible changes without bumping relayproto.Version require -additive \"<reason>\":\n  %s",
					strings.Join(added, "\n  "))
			}
			updated.Additive = append(updated.Additive, reason)
		}
	} else if reason := strings.TrimSpace(additive); reason != "" {
		updated.Additive = []string{reason}
	}
	return updated, nil
}

func writeGoldenFile(path string, current map[string]string, version int, init, update bool, additive string) error {
	if init && update {
		return fmt.Errorf("cannot combine -init and -update")
	}
	var next surfaceGolden
	raw, err := os.ReadFile(path)
	if init {
		if err == nil {
			return fmt.Errorf("golden file %s already exists; use -update instead of -init", path)
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat golden %s: %w", path, err)
		}
		next = surfaceGolden{RelayVersion: version, Entries: current}
		if r := strings.TrimSpace(additive); r != "" {
			next.Additive = []string{r}
		}
	} else {
		if os.IsNotExist(err) {
			return fmt.Errorf("golden file %s does not exist; refusing -update so breaking changes cannot bypass checks (use -init only for initial creation)", path)
		}
		if err != nil {
			return fmt.Errorf("read existing golden: %w", err)
		}
		old, err := parseGolden(raw)
		if err != nil {
			return fmt.Errorf("parse existing golden: %w", err)
		}
		next, err = planGoldenUpdate(old, current, version, additive)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(formatGolden(next)), 0644)
}

func TestRelayVisibleSurfaceGolden(t *testing.T) {
	current := currentRelaySurface(t)

	if *initGolden || *updateGolden {
		if err := writeGoldenFile(goldenRelPath, current, relayproto.Version, *initGolden, *updateGolden, *additiveReason); err != nil {
			t.Fatal(err)
		}
		return
	}

	raw, err := os.ReadFile(goldenRelPath)
	if err != nil {
		t.Fatalf("read %s: %v (initialize with: go test ./internal/relayproto -init)", goldenRelPath, err)
	}
	golden, err := parseGolden(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", goldenRelPath, err)
	}
	breaking, added := diffSurface(golden.Entries, current)
	if len(breaking) > 0 || len(added) > 0 {
		var msg strings.Builder
		msg.WriteString("relay-visible wire surface changed since golden snapshot:\n")
		for _, b := range breaking {
			fmt.Fprintf(&msg, "  BREAKING: %s\n", b)
		}
		for _, a := range added {
			fmt.Fprintf(&msg, "  ADDITIVE: %s\n", a)
		}
		msg.WriteString("Decide whether relayproto.Version must be bumped, then run:\n")
		msg.WriteString("  go test ./internal/relayproto -update [-additive \"<reason>\"]")
		t.Fatal(msg.String())
	}
	if golden.RelayVersion != relayproto.Version {
		t.Fatalf("golden relay_version (%d) != relayproto.Version (%d); run `go test ./internal/relayproto -update`",
			golden.RelayVersion, relayproto.Version)
	}
}

func TestRelaySurfaceUpdateRules(t *testing.T) {
	base := surfaceGolden{
		RelayVersion: 17,
		Entries: map[string]string{
			"enum remoteagent.v1.FrameClass.1":     "SIGNAL",
			"enum remoteagent.v1.FrameClass.2":     "CONTROL",
			"field remoteagent.v1.LoginRequest.4":  "uint32",
			"schema DirectedPeerFrame.1":           "b",
			"schema DirectedPeerFrame.2":           "e2",
		},
	}

	clone := func(m map[string]string) map[string]string {
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}

	// 1. Deleting a field without bumping RelayVersion -> rejected.
	delField := clone(base.Entries)
	delete(delField, "field remoteagent.v1.LoginRequest.4")
	if _, err := planGoldenUpdate(base, delField, 17, "not additive"); err == nil {
		t.Fatal("deleted field accepted without bumping RelayVersion")
	}

	// 2. Renumbering a field without bumping RelayVersion -> rejected.
	renum := clone(base.Entries)
	delete(renum, "field remoteagent.v1.LoginRequest.4")
	renum["field remoteagent.v1.LoginRequest.5"] = "uint32"
	if _, err := planGoldenUpdate(base, renum, 17, ""); err == nil {
		t.Fatal("renumbered field accepted without bumping RelayVersion")
	}

	// 3. Changing a field type without bumping RelayVersion -> rejected.
	retype := clone(base.Entries)
	retype["field remoteagent.v1.LoginRequest.4"] = "string"
	if _, err := planGoldenUpdate(base, retype, 17, ""); err == nil {
		t.Fatal("retyped field accepted without bumping RelayVersion")
	}

	// 4. Deleting an enum value without bumping RelayVersion -> rejected.
	delEnum := clone(base.Entries)
	delete(delEnum, "enum remoteagent.v1.FrameClass.2")
	if _, err := planGoldenUpdate(base, delEnum, 17, ""); err == nil {
		t.Fatal("deleted enum value accepted without bumping RelayVersion")
	}

	// 5. Purely additive change without -additive reason -> rejected.
	addField := clone(base.Entries)
	addField["field remoteagent.v1.LoginRequest.5"] = "string"
	if _, err := planGoldenUpdate(base, addField, 17, ""); err == nil {
		t.Fatal("additive change accepted without reason")
	}

	// 6. Purely additive change WITH reason -> accepted and recorded in golden.
	updated, err := planGoldenUpdate(base, addField, 17, "optional diagnostic field ignored by older relays")
	if err != nil {
		t.Fatalf("additive change with reason rejected: %v", err)
	}
	parsed, err := parseGolden([]byte(formatGolden(updated)))
	if err != nil {
		t.Fatalf("round-trip golden: %v", err)
	}
	if len(parsed.Additive) != 1 || parsed.Additive[0] != "optional diagnostic field ignored by older relays" {
		t.Fatalf("unexpected additive notes: %#v", parsed.Additive)
	}

	// 7. Breaking change WITH RelayVersion bump -> accepted and clears old additive notes.
	withOldNote := base
	withOldNote.Additive = []string{"old note"}
	bumped, err := planGoldenUpdate(withOldNote, retype, 18, "")
	if err != nil {
		t.Fatalf("breaking change with RelayVersion bump rejected: %v", err)
	}
	if bumped.RelayVersion != 18 || len(bumped.Additive) != 0 {
		t.Fatalf("unexpected bumped golden: %+v", bumped)
	}

	// 8. -update refuses a missing golden file (prevents delete-and-regenerate bypass);
	// -init creates a missing golden file and refuses an existing one.
	goldenPath := filepath.Join(t.TempDir(), "relay_surface.golden")
	if err := writeGoldenFile(goldenPath, base.Entries, 17, false, true, ""); err == nil {
		t.Fatal("-update accepted a missing golden file")
	}
	if err := writeGoldenFile(goldenPath, base.Entries, 17, true, true, ""); err == nil {
		t.Fatal("combined -init and -update accepted")
	}
	if err := writeGoldenFile(goldenPath, base.Entries, 17, true, false, ""); err != nil {
		t.Fatalf("-init failed on missing golden file: %v", err)
	}
	if err := writeGoldenFile(goldenPath, base.Entries, 17, true, false, ""); err == nil {
		t.Fatal("-init overwrote an existing golden file")
	}
}
