package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func strPtr(s string) *string { return &s }
func int32Ptr(v int32) *int32 { return &v }
func labelPtr(l descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto_Label {
	return &l
}
func typePtr(t descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto_Type {
	return &t
}

func TestReachableClosureAndPruning(t *testing.T) {
	req := &pluginpb.CodeGeneratorRequest{
		Parameter: strPtr("paths=source_relative"),
		FileToGenerate: []string{
			"remoteagent/core/v1/app.proto",
			"remoteagent/v1/wire.proto",
			"remoteagent/v1/unused.proto",
		},
		ProtoFile: []*descriptorpb.FileDescriptorProto{
			{
				Name:    strPtr("remoteagent/v1/wire.proto"),
				Package: strPtr("remoteagent.v1"),
				Syntax:  strPtr("proto3"),
				Options: &descriptorpb.FileOptions{GoPackage: strPtr("example.com/gen/remoteagent/v1;v1")},
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name: strPtr("UsedEnum"),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: strPtr("USED_ENUM_UNSPECIFIED"), Number: int32Ptr(0)},
						},
					},
					{
						Name: strPtr("UnusedEnum"),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: strPtr("UNUSED_ENUM_UNSPECIFIED"), Number: int32Ptr(0)},
						},
					},
				},
				MessageType: []*descriptorpb.DescriptorProto{
					{Name: strPtr("CoreShared")},
					{
						Name: strPtr("CoreReferenced"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     strPtr("kind"),
								Number:   int32Ptr(1),
								Label:    labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
								Type:     typePtr(descriptorpb.FieldDescriptorProto_TYPE_ENUM),
								TypeName: strPtr(".remoteagent.v1.UsedEnum"),
							},
						},
					},
					{
						Name: strPtr("EntryRoot"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     strPtr("leaf"),
								Number:   int32Ptr(1),
								Label:    labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
								Type:     typePtr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
								TypeName: strPtr(".remoteagent.v1.EntryLeaf"),
							},
						},
					},
					{Name: strPtr("EntryLeaf")},
					{Name: strPtr("DeadMessage")},
				},
			},
			{
				Name:    strPtr("remoteagent/v1/unused.proto"),
				Package: strPtr("remoteagent.v1"),
				Syntax:  strPtr("proto3"),
				Options: &descriptorpb.FileOptions{GoPackage: strPtr("example.com/gen/remoteagent/v1;v1")},
				MessageType: []*descriptorpb.DescriptorProto{
					{Name: strPtr("OnlyServerRpc")},
				},
			},
			{
				Name:       strPtr("remoteagent/core/v1/app.proto"),
				Package:    strPtr("remoteagent.core.v1"),
				Syntax:     strPtr("proto3"),
				Dependency: []string{"remoteagent/v1/wire.proto"},
				Options:    &descriptorpb.FileOptions{GoPackage: strPtr("example.com/gen/remoteagent/core/v1;corev1")},
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: strPtr("CoreEvent"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     strPtr("ref"),
								Number:   int32Ptr(1),
								Label:    labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
								Type:     typePtr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
								TypeName: strPtr(".remoteagent.v1.CoreReferenced"),
							},
							{
								Name:     strPtr("shared"),
								Number:   int32Ptr(2),
								Label:    labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
								Type:     typePtr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
								TypeName: strPtr(".remoteagent.v1.CoreShared"),
							},
						},
					},
					{
						Name: strPtr("CoreView"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     strPtr("shared"),
								Number:   int32Ptr(1),
								Label:    labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
								Type:     typePtr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
								TypeName: strPtr(".remoteagent.v1.CoreShared"),
							},
						},
					},
				},
			},
		},
	}

	entriesFile := filepath.Join(t.TempDir(), "entries.txt")
	entries := strings.Join([]string{
		"# comment",
		"in: remoteagent.core.v1.CoreEvent",
		"out: remoteagent.core.v1.CoreView",
		"clone: remoteagent.v1.EntryRoot",
		"",
	}, "\n")
	if err := os.WriteFile(entriesFile, []byte(entries), 0o600); err != nil {
		t.Fatal(err)
	}

	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	reach, err := computeReachable(plugin.Files, entriesFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plugin.Files {
		if !f.Generate || !fileHasReachable(f, reach) {
			continue
		}
		generateFile(plugin, f, reach)
	}
	resp := plugin.Response()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", *resp.Error)
	}

	files := make(map[string]string)
	for _, gf := range resp.File {
		files[gf.GetName()] = gf.GetContent()
	}
	if _, ok := files["remoteagent/v1/unused.pb.go"]; ok {
		t.Fatal("expected unused.pb.go to be skipped when no types are reachable")
	}
	wireGo, ok := files["remoteagent/v1/wire.pb.go"]
	if !ok {
		t.Fatal("expected wire.pb.go to be generated")
	}
	appGo, ok := files["remoteagent/core/v1/app.pb.go"]
	if !ok {
		t.Fatal("expected app.pb.go to be generated")
	}
	for _, want := range []string{
		"type UsedEnum",
		"type CoreReferenced struct",
		"func (m *CoreReferenced) Unmarshal(",
		"type CoreShared struct",
		"func (m *CoreShared) Size() int",
		"func (m *CoreShared) Marshal()",
		"func (m *CoreShared) Unmarshal(",
		"type EntryRoot struct",
		"func (m *EntryRoot) Clone()",
		"type EntryLeaf struct",
		"func (m *EntryLeaf) Clone()",
	} {
		if !strings.Contains(wireGo, want) {
			t.Fatalf("wire.pb.go missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"UnusedEnum",
		"DeadMessage",
		"OnlyServerRpc",
		"Reset()",
		"func (m *CoreReferenced) Size()",
		"func (m *CoreReferenced) Marshal()",
		"func (m *CoreReferenced) Clone()",
		"func (m *CoreShared) Clone()",
		"func (m *EntryRoot) Size()",
		"func (m *EntryRoot) Marshal()",
		"func (m *EntryRoot) Unmarshal(",
	} {
		if strings.Contains(wireGo, unwanted) {
			t.Fatalf("wire.pb.go should not contain %q", unwanted)
		}
	}
	for _, want := range []string{
		"func (m *CoreEvent) Unmarshal(",
		"func (m *CoreView) Size() int",
		"func (m *CoreView) Marshal()",
	} {
		if !strings.Contains(appGo, want) {
			t.Fatalf("app.pb.go missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"Reset()",
		"func (m *CoreEvent) Size()",
		"func (m *CoreEvent) Marshal()",
		"func (m *CoreEvent) Clone()",
		"func (m *CoreView) Unmarshal(",
		"func (m *CoreView) Clone()",
	} {
		if strings.Contains(appGo, unwanted) {
			t.Fatalf("app.pb.go should not contain %q", unwanted)
		}
	}
}

func TestUnknownEntryAndDirectiveReturnError(t *testing.T) {
	req := &pluginpb.CodeGeneratorRequest{
		Parameter:      strPtr("paths=source_relative"),
		FileToGenerate: []string{"remoteagent/core/v1/app.proto"},
		ProtoFile: []*descriptorpb.FileDescriptorProto{
			{
				Name:    strPtr("remoteagent/core/v1/app.proto"),
				Package: strPtr("remoteagent.core.v1"),
				Syntax:  strPtr("proto3"),
				Options: &descriptorpb.FileOptions{GoPackage: strPtr("example.com/gen/remoteagent/core/v1;corev1")},
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name:  strPtr("SampleEnum"),
						Value: []*descriptorpb.EnumValueDescriptorProto{{Name: strPtr("SAMPLE_ENUM_UNSPECIFIED"), Number: int32Ptr(0)}},
					},
				},
			},
		},
	}
	for _, content := range []string{
		"remoteagent.v1.DoesNotExist\n",
		"bogus: remoteagent.core.v1.SampleEnum\n",
		"clone: remoteagent.core.v1.SampleEnum\n",
	} {
		entriesFile := filepath.Join(t.TempDir(), "entries.txt")
		if err := os.WriteFile(entriesFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		plugin, err := protogen.Options{}.New(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := computeReachable(plugin.Files, entriesFile); err == nil {
			t.Fatalf("expected error for entries %q", content)
		}
	}
	_ = proto.Marshal
}
