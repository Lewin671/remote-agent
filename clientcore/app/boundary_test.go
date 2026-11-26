package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	stdcorev1 "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	coreProtoPkg = protoreflect.FullName("remoteagent.core.v1")
	wireProtoPkg = protoreflect.FullName("remoteagent.v1")
)

// Pure top-level domain/identity enums from remoteagent.v1 that remoteagent.core.v1
// is permitted to reuse directly (docs/architecture.md §5 壳的两层职责边界).
// Nested enums inside wire RPC messages (e.g. GetRecoveryContextResponse.Phase)
// are never allowed here.
var allowedWireEnums = map[protoreflect.FullName]bool{
	"remoteagent.v1.Agent":                  true,
	"remoteagent.v1.ApprovalMode":           true,
	"remoteagent.v1.SendMode":               true,
	"remoteagent.v1.PlanChoice":             true,
	"remoteagent.v1.ToolKind":               true,
	"remoteagent.v1.ToolBackground":         true,
	"remoteagent.v1.TurnOutcome":            true,
	"remoteagent.v1.DeviceKind":             true,
	"remoteagent.v1.UsageRangePreset":       true,
	"remoteagent.v1.UsageBucketGranularity": true,
	"remoteagent.v1.UsageSourceState":       true,
	"remoteagent.v1.ImageFormat":            true,
	"remoteagent.v1.NotifyKind":             true,
}

// Shrink-only allowlist of legacy remoteagent.v1 message / disallowed-enum edges
// still reachable from the core contract roots (Screen, ViewModel, Event, Effect).
//
// Rules:
//   - Never add new entries.
//   - When migrating a field off remoteagent.v1, delete its entry in the same commit;
//     leaving a resolved entry here fails the test.
var legacyWireAllowlist = map[string]string{
	// Legacy machine command bypass (retired in P2-4/P2-5/P4-1).
	"RunMachineCommandAction.command -> v1.Command":        "legacy-run-machine-command (P2-4/P2-5/P4-1)",
	"RunMachineCommandResultEffect.result -> v1.CommandResult": "legacy-run-machine-command (P2-4/P2-5/P4-1)",

	// Legacy typed RPC / stream / cache / envelope / terminal edges (retired in P2-5/P3-2/P4-1).
	"AccountPrefsFetchedEvent.wire_prefs -> v1.AccountPrefsObject":           "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"AccountPrefsPutResultEvent.wire_current -> v1.AccountPrefsObject":       "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheBootstrapLoadedEvent.wire_machines -> v1.Machine":                  "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheBootstrapLoadedEvent.wire_prefs -> v1.AccountPrefsObject":          "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheBootstrapLoadedEvent.wire_sessions -> v1.RemoteSession":            "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheEventsLoadedEvent.wire_events -> v1.StoredEvent":                   "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheWriteEffect.keep_session_owner -> v1.RemoteSession":                "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheWriteEffect.write_machine -> v1.Machine":                           "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheWriteEffect.write_prefs -> v1.AccountPrefsObject":                  "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheWriteEffect.write_session -> v1.RemoteSession":                     "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CacheWriteStoredEvents.events -> v1.StoredEvent":                        "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CommandOutcomeEvent.result -> v1.CommandResult":                         "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"CommandOutcomeEvent.sealed_result -> v1.Envelope":                       "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"DevicesFetchedEvent.wire_devices -> v1.Device":                          "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"DirectEventsReceivedEvent.wire_envelopes -> v1.Envelope":                "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"DispatchCommandEffect.sealed_envelope -> v1.Envelope":                   "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"EventsAppendedFrame.wire_events -> v1.StoredEvent":                      "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"EventsPageFetchedEvent.wire_events -> v1.StoredEvent":                   "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"FetchSessionsPageRpc.after -> v1.SessionCursor":                         "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"ListsFetchedEvent.next -> v1.SessionCursor":                             "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"ListsFetchedEvent.wire_devices -> v1.Device":                            "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"ListsFetchedEvent.wire_machines -> v1.Machine":                          "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"ListsFetchedEvent.wire_sessions -> v1.RemoteSession":                    "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"PlainAccountPrefs.prefs -> v1.ProjectPrefs":                             "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"PlainDevice.meta -> v1.DeviceMeta":                                      "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"PlainEvent.event -> v1.Event":                                           "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"PlainMachine.meta -> v1.MachineMeta":                                    "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"PlainSession.checkpoint -> v1.LiveSnapshot":                             "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"PlainSession.meta -> v1.Session":                                        "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"PutAccountPrefsRpc.next_prefs -> v1.ProjectPrefs":                       "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"PutAccountPrefsRpc.sealed_envelope -> v1.Envelope":                      "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"SessionByIdFetchedEvent.wire_session -> v1.RemoteSession":               "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"SessionsPageFetchedEvent.next -> v1.SessionCursor":                      "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"SessionsPageFetchedEvent.wire_sessions -> v1.RemoteSession":             "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"StreamFrameEvent.wire_account_prefs_updated -> v1.AccountPrefsObject":   "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"StreamFrameEvent.wire_device_updated -> v1.Device":                      "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"StreamFrameEvent.wire_machine_updated -> v1.Machine":                    "legacy-relay-pipe (P2-5/P3-2/P4-1)",
	"StreamFrameEvent.wire_session_updated -> v1.RemoteSession":              "legacy-relay-pipe (P2-5/P3-2/P4-1)",

	// Demo seeding event (only used by ?demo / preview fixtures).
	"DemoDevice.meta -> v1.DeviceMeta":          "demo-seed (P4-1)",
	"DemoMachine.meta -> v1.MachineMeta":        "demo-seed (P4-1)",
	"DemoSession.checkpoint -> v1.LiveSnapshot": "demo-seed (P4-1)",
	"DemoSession.meta -> v1.Session":            "demo-seed (P4-1)",
	"DemoSessionEvents.events -> v1.Event":      "demo-seed (P4-1)",
	"SeedDemoWorldEvent.prefs -> v1.ProjectPrefs": "demo-seed (P4-1)",
}

// Permitted remoteagent.core.v1 root entries in cmd/protoc-gen-go-lite/entries.txt.
var allowedCoreLiteEntries = map[string]bool{
	"in:remoteagent.core.v1.Screen":                    true,
	"in:remoteagent.core.v1.Event":                     true,
	"in:remoteagent.core.v1.PersistedDraftRecord":      true,
	"in:remoteagent.core.v1.PersistedOutboxRecord":     true,
	"out:remoteagent.core.v1.ViewModel":                true,
	"out:remoteagent.core.v1.EffectBatch":              true,
	"out:remoteagent.core.v1.Effect":                   true,
	"out:remoteagent.core.v1.PersistedDraftRecord":     true,
	"out:remoteagent.core.v1.PersistedOutboxRecord":    true,
	"strict:remoteagent.core.v1.PersistedDraftRecord":  true,
	"strict:remoteagent.core.v1.PersistedOutboxRecord": true,
}

// Shrink-only allowlist for legacy internal core roots still exported in entries.txt
// until corebind/projection.go (Android TestServer / EventProjectionTest) is retired.
var legacyCoreLiteEntriesAllowlist = map[string]string{
	"in:remoteagent.core.v1.EventBatch":       "legacy-corebind-projection",
	"out:remoteagent.core.v1.ProjectionState": "legacy-corebind-projection",
}

func shortWireName(d protoreflect.Descriptor) string {
	return "v1." + strings.TrimPrefix(string(d.FullName()), string(wireProtoPkg)+".")
}

// scanCoreWireReferences walks all reachable remoteagent.core.v1 messages starting
// from roots and returns every field referencing a remoteagent.v1 message or
// non-whitelisted enum, mapped to one example traversal path from a root.
func scanCoreWireReferences(roots []protoreflect.MessageDescriptor) map[string]string {
	violations := map[string]string{}
	visited := map[protoreflect.FullName]bool{}

	record := func(edge, path string) {
		if _, exists := violations[edge]; !exists {
			violations[edge] = path
		}
	}

	var walk func(md protoreflect.MessageDescriptor, path string)
	walk = func(md protoreflect.MessageDescriptor, path string) {
		if md == nil || md.ParentFile().Package() != coreProtoPkg {
			return
		}
		if visited[md.FullName()] {
			return
		}
		visited[md.FullName()] = true

		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			fieldPath := path + "." + string(fd.Name())

			checkField := func(f protoreflect.FieldDescriptor) {
				switch f.Kind() {
				case protoreflect.MessageKind, protoreflect.GroupKind:
					target := f.Message()
					if target == nil {
						return
					}
					switch target.ParentFile().Package() {
					case wireProtoPkg:
						edge := string(md.Name()) + "." + string(fd.Name()) + " -> " + shortWireName(target)
						record(edge, fieldPath)
					case coreProtoPkg:
						walk(target, fieldPath)
					}
				case protoreflect.EnumKind:
					ed := f.Enum()
					if ed == nil || ed.ParentFile().Package() != wireProtoPkg {
						return
					}
					_, isTopLevel := ed.Parent().(protoreflect.FileDescriptor)
					if !isTopLevel || !allowedWireEnums[ed.FullName()] {
						edge := string(md.Name()) + "." + string(fd.Name()) + " -> " + shortWireName(ed)
						record(edge, fieldPath)
					}
				}
			}

			if fd.IsMap() {
				checkField(fd.MapKey())
				checkField(fd.MapValue())
			} else {
				checkField(fd)
			}
		}
	}

	for _, root := range roots {
		walk(root, string(root.Name()))
	}
	return violations
}

func diffWireViolations(found map[string]string, allowlist map[string]string) (unexpected, stale []string) {
	for edge := range found {
		if _, ok := allowlist[edge]; !ok {
			unexpected = append(unexpected, edge)
		}
	}
	for edge := range allowlist {
		if _, ok := found[edge]; !ok {
			stale = append(stale, edge)
		}
	}
	sort.Strings(unexpected)
	sort.Strings(stale)
	return unexpected, stale
}

func parseCoreLiteEntries(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		dir := "both"
		name := line
		if prefix, rest, ok := strings.Cut(line, ":"); ok {
			dir = strings.TrimSpace(prefix)
			name = strings.TrimSpace(rest)
		}
		if strings.HasPrefix(name, string(coreProtoPkg)+".") {
			entries[dir+":"+name] = true
		}
	}
	return entries, nil
}

// TestCoreContractWireBoundary enforces the shell/core contract boundary
// (docs/architecture.md §5 壳的两层职责边界):
//   - No new remoteagent.v1 message or disallowed enum may enter Screen, ViewModel, Event, or Effect.
//   - The legacy allowlist is shrink-only: resolved entries must be deleted immediately.
//   - cmd/protoc-gen-go-lite/entries.txt may only export permitted core contract roots.
func TestCoreContractWireBoundary(t *testing.T) {
	roots := []protoreflect.MessageDescriptor{
		(&stdcorev1.Screen{}).ProtoReflect().Descriptor(),
		(&stdcorev1.ViewModel{}).ProtoReflect().Descriptor(),
		(&stdcorev1.Event{}).ProtoReflect().Descriptor(),
		(&stdcorev1.Effect{}).ProtoReflect().Descriptor(),
	}
	found := scanCoreWireReferences(roots)
	unexpected, stale := diffWireViolations(found, legacyWireAllowlist)
	for _, edge := range unexpected {
		t.Errorf("forbidden remoteagent.v1 reference in core contract: %q (path: %s)", edge, found[edge])
	}
	for _, edge := range stale {
		t.Errorf("stale legacyWireAllowlist entry %q (tag=%q): violation is gone, delete it", edge, legacyWireAllowlist[edge])
	}

	entriesPath := filepath.Join("..", "..", "cmd", "protoc-gen-go-lite", "entries.txt")
	coreEntries, err := parseCoreLiteEntries(entriesPath)
	if err != nil {
		t.Fatalf("read entries.txt: %v", err)
	}
	foundLegacyEntries := map[string]string{}
	for entry := range coreEntries {
		if allowedCoreLiteEntries[entry] {
			continue
		}
		foundLegacyEntries[entry] = entriesPath
	}
	unexpectedEntries, staleEntries := diffWireViolations(foundLegacyEntries, legacyCoreLiteEntriesAllowlist)
	for _, entry := range unexpectedEntries {
		t.Errorf("forbidden remoteagent.core.v1 root in entries.txt: %q", entry)
	}
	for _, entry := range staleEntries {
		t.Errorf("stale legacyCoreLiteEntriesAllowlist entry %q: remove it", entry)
	}
}

func TestCoreContractWireBoundaryGuardEnforcement(t *testing.T) {
	wireFile := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("remoteagent/v1/fake.proto"),
		Package: proto.String("remoteagent.v1"),
		Syntax:  proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name: proto.String("Agent"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: proto.String("AGENT_UNSPECIFIED"), Number: proto.Int32(0)},
				},
			},
			{
				Name: proto.String("UnlistedEnum"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: proto.String("UNLISTED_ENUM_UNSPECIFIED"), Number: proto.Int32(0)},
				},
			},
		},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("WirePayload")},
			{
				Name: proto.String("FakeResponse"),
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name: proto.String("Phase"),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: proto.String("PHASE_UNSPECIFIED"), Number: proto.Int32(0)},
						},
					},
				},
			},
		},
	}
	coreFile := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("remoteagent/core/v1/fake_view.proto"),
		Package:    proto.String("remoteagent.core.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"remoteagent/v1/fake.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("FakeRoot"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     proto.String("allowed_agent"),
						Number:   proto.Int32(1),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
						TypeName: proto.String(".remoteagent.v1.Agent"),
					},
					{
						Name:     proto.String("bad_msg"),
						Number:   proto.Int32(2),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: proto.String(".remoteagent.v1.WirePayload"),
					},
					{
						Name:     proto.String("bad_nested_enum"),
						Number:   proto.Int32(3),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
						TypeName: proto.String(".remoteagent.v1.FakeResponse.Phase"),
					},
					{
						Name:     proto.String("bad_unlisted_enum"),
						Number:   proto.Int32(4),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
						TypeName: proto.String(".remoteagent.v1.UnlistedEnum"),
					},
				},
			},
		},
	}
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{wireFile, coreFile},
	})
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v", err)
	}
	desc, err := files.FindDescriptorByName("remoteagent.core.v1.FakeRoot")
	if err != nil {
		t.Fatalf("FindDescriptorByName: %v", err)
	}

	found := scanCoreWireReferences([]protoreflect.MessageDescriptor{desc.(protoreflect.MessageDescriptor)})
	wantEdges := []string{
		"FakeRoot.bad_msg -> v1.WirePayload",
		"FakeRoot.bad_nested_enum -> v1.FakeResponse.Phase",
		"FakeRoot.bad_unlisted_enum -> v1.UnlistedEnum",
	}
	unexpected, stale := diffWireViolations(found, map[string]string{
		"FakeRoot.stale_entry -> v1.Gone": "fixed",
	})
	if len(unexpected) != len(wantEdges) {
		t.Fatalf("unexpected violations = %v, want %v", unexpected, wantEdges)
	}
	for i := range wantEdges {
		if unexpected[i] != wantEdges[i] {
			t.Fatalf("unexpected[%d] = %q, want %q", i, unexpected[i], wantEdges[i])
		}
	}
	if len(stale) != 1 || stale[0] != "FakeRoot.stale_entry -> v1.Gone" {
		t.Fatalf("stale allowlist entries = %v, want [FakeRoot.stale_entry -> v1.Gone]", stale)
	}
}
