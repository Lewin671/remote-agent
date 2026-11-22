// Command protoc-gen-go-lite generates minimal, reflection-free Go protobuf structs and
// direction-aware litewire Marshal/Unmarshal/Clone methods covering the subset of proto3 used in this repo.
package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"
)

const maxSwitchChunk = 12

var (
	litewirePkg = protogen.GoImportPath("github.com/Lewin671/remote-agent/clientcore/gen/litewire")
	mathPkg     = protogen.GoImportPath("math")
	slicesPkg   = protogen.GoImportPath("slices")
	unsafePkg   = protogen.GoImportPath("unsafe")
)

type cycleEdge struct {
	msg   *protogen.Message
	field *protogen.Field
}

type msgFlags struct {
	marshal   bool
	unmarshal bool
	clone     bool
	strict    bool
}

func (a msgFlags) or(b msgFlags) msgFlags {
	return msgFlags{
		marshal:   a.marshal || b.marshal,
		unmarshal: a.unmarshal || b.unmarshal,
		clone:     a.clone || b.clone,
		strict:    a.strict || b.strict,
	}
}

type reachSet struct {
	msgs  map[*protogen.Message]msgFlags
	enums map[*protogen.Enum]bool
}

func main() {
	var flags flag.FlagSet
	entriesPath := flags.String("entries", "", "path to file listing extra protobuf full names to include")
	protogen.Options{ParamFunc: flags.Set}.Run(func(plugin *protogen.Plugin) error {
		plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		reach, err := computeReachable(plugin.Files, *entriesPath)
		if err != nil {
			return err
		}
		cyclic := computeCyclicSubEdges(plugin.Files, reach)
		for _, f := range plugin.Files {
			if !f.Generate || !fileHasReachable(f, reach) {
				continue
			}
			generateFile(plugin, f, reach, cyclic)
		}
		return nil
	})
}

func computeReachable(files []*protogen.File, entriesPath string) (*reachSet, error) {
	if entriesPath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(entriesPath)
	if err != nil {
		return nil, err
	}

	msgByName := make(map[string]*protogen.Message)
	enumByName := make(map[string]*protogen.Enum)
	var indexMsgs func([]*protogen.Message)
	indexMsgs = func(msgs []*protogen.Message) {
		for _, m := range msgs {
			if m.Desc.IsMapEntry() {
				continue
			}
			msgByName[string(m.Desc.FullName())] = m
			for _, e := range m.Enums {
				enumByName[string(e.Desc.FullName())] = e
			}
			indexMsgs(m.Messages)
		}
	}
	for _, f := range files {
		for _, e := range f.Enums {
			enumByName[string(e.Desc.FullName())] = e
		}
		indexMsgs(f.Messages)
	}

	reach := &reachSet{
		msgs:  make(map[*protogen.Message]msgFlags),
		enums: make(map[*protogen.Enum]bool),
	}
	var queue []*protogen.Message

	markMsg := func(m *protogen.Message, add msgFlags) {
		if m == nil || m.Desc.IsMapEntry() {
			return
		}
		cur := reach.msgs[m]
		next := cur.or(add)
		if next == cur {
			return
		}
		reach.msgs[m] = next
		queue = append(queue, m)
	}
	markEnum := func(e *protogen.Enum) {
		if e != nil {
			reach.enums[e] = true
		}
	}

	for _, line := range strings.Split(string(raw), "\n") {
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = line[:idx]
		}
		entry := strings.TrimSpace(line)
		if entry == "" {
			continue
		}
		flags := msgFlags{marshal: true, unmarshal: true}
		name := entry
		if colon := strings.IndexByte(entry, ':'); colon >= 0 {
			kind := strings.TrimSpace(entry[:colon])
			name = strings.TrimSpace(entry[colon+1:])
			switch kind {
			case "in":
				flags = msgFlags{unmarshal: true}
			case "strict":
				flags = msgFlags{unmarshal: true, strict: true}
			case "out":
				flags = msgFlags{marshal: true}
			case "clone":
				flags = msgFlags{clone: true}
			default:
				return nil, errors.New("protoc-gen-go-lite: unknown entry directive " + kind + " in " + entriesPath)
			}
		}
		if m, ok := msgByName[name]; ok {
			markMsg(m, flags)
			continue
		}
		if e, ok := enumByName[name]; ok {
			if flags.clone && !flags.marshal && !flags.unmarshal {
				return nil, errors.New("protoc-gen-go-lite: clone directive cannot target enum " + name)
			}
			markEnum(e)
			continue
		}
		msg := "protoc-gen-go-lite: unknown entry " + name + " in " + entriesPath
		return nil, errors.New(msg)
	}

	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		flags := reach.msgs[m]
		for _, f := range m.Fields {
			if f.Desc.IsMap() {
				vf := f.Message.Fields[1]
				if vf.Message != nil {
					markMsg(vf.Message, flags)
				}
				if vf.Enum != nil {
					markEnum(vf.Enum)
				}
				continue
			}
			if f.Message != nil {
				markMsg(f.Message, flags)
			}
			if f.Enum != nil {
				markEnum(f.Enum)
			}
		}
	}

	return reach, nil
}

func fileHasReachable(f *protogen.File, reach *reachSet) bool {
	if reach == nil {
		return true
	}
	for _, e := range f.Enums {
		if reach.enums[e] {
			return true
		}
	}
	var hasMsg func([]*protogen.Message) bool
	hasMsg = func(msgs []*protogen.Message) bool {
		for _, m := range msgs {
			if m.Desc.IsMapEntry() {
				continue
			}
			if _, ok := reach.msgs[m]; ok {
				return true
			}
			for _, e := range m.Enums {
				if reach.enums[e] {
					return true
				}
			}
			if hasMsg(m.Messages) {
				return true
			}
		}
		return false
	}
	return hasMsg(f.Messages)
}

func isExtraUnmarshalField(f *protogen.Field) bool {
	return f.Desc.IsMap() ||
		isRealOneof(f) ||
		(f.Desc.IsList() && (f.Desc.Kind() == protoreflect.MessageKind || f.Desc.Kind() == protoreflect.EnumKind)) ||
		(isOptionalPtr(f) && f.Desc.Kind() == protoreflect.EnumKind)
}

func msgFlagsFor(msg *protogen.Message, reach *reachSet) (msgFlags, bool) {
	if reach == nil {
		return msgFlags{marshal: true, unmarshal: true, clone: true}, true
	}
	f, ok := reach.msgs[msg]
	return f, ok
}

func computeCyclicSubEdges(files []*protogen.File, reach *reachSet) map[cycleEdge]bool {
	byPkg := make(map[protogen.GoImportPath][]*protogen.Message)
	var collect func([]*protogen.Message)
	collect = func(msgs []*protogen.Message) {
		for _, m := range msgs {
			if m.Desc.IsMapEntry() {
				continue
			}
			collect(m.Messages)
			if flags, ok := msgFlagsFor(m, reach); ok && (flags.marshal || flags.unmarshal) {
				byPkg[m.GoIdent.GoImportPath] = append(byPkg[m.GoIdent.GoImportPath], m)
			}
		}
	}
	for _, f := range files {
		collect(f.Messages)
	}

	cyclic := make(map[cycleEdge]bool)
	for _, msgs := range byPkg {
		slices.SortFunc(msgs, func(a, b *protogen.Message) int {
			return cmp.Compare(a.Desc.FullName(), b.Desc.FullName())
		})
		state := make(map[*protogen.Message]uint8, len(msgs))
		var visit func(*protogen.Message)
		visit = func(u *protogen.Message) {
			state[u] = 1
			flags, _ := msgFlagsFor(u, reach)
			for _, f := range u.Fields {
				if f.Desc.IsMap() || isRealOneof(f) || f.Desc.Kind() != protoreflect.MessageKind {
					continue
				}
				if f.Desc.IsList() && !flags.marshal {
					continue
				}
				v := f.Message
				if v == nil || v.GoIdent.GoImportPath != u.GoIdent.GoImportPath {
					continue
				}
				switch state[v] {
				case 1:
					cyclic[cycleEdge{msg: u, field: f}] = true
				case 0:
					visit(v)
				}
			}
			state[u] = 2
		}
		for _, m := range msgs {
			if state[m] == 0 {
				visit(m)
			}
		}
	}
	return cyclic
}

func generateFile(plugin *protogen.Plugin, file *protogen.File, reach *reachSet, cyclic map[cycleEdge]bool) {
	filename := file.GeneratedFilenamePrefix + ".pb.go"
	g := plugin.NewGeneratedFile(filename, file.GoImportPath)
	g.P("// Code generated by cmd/protoc-gen-go-lite. DO NOT EDIT.")
	g.P("// source: ", file.Desc.Path())
	g.P()
	g.P("package ", file.GoPackageName)
	g.P()

	for _, enum := range file.Enums {
		if reach == nil || reach.enums[enum] {
			genEnum(g, enum)
		}
	}
	var initStmts []string
	for _, msg := range file.Messages {
		genMessage(g, msg, reach, cyclic, &initStmts)
	}
	if len(initStmts) > 0 {
		g.P("func init() {")
		for _, stmt := range initStmts {
			g.P("\t", stmt)
		}
		g.P("}")
		g.P()
	}
}

func genEnum(g *protogen.GeneratedFile, enum *protogen.Enum) {
	name := enum.GoIdent.GoName
	g.P("type ", name, " int32")
	g.P()
	g.P("const (")
	for _, val := range enum.Values {
		g.P("\t", val.GoIdent.GoName, " ", name, " = ", val.Desc.Number())
	}
	g.P(")")
	g.P()
}

func fieldGoType(g *protogen.GeneratedFile, f *protogen.Field) string {
	base := scalarOrMsgGoType(g, f)
	switch {
	case f.Desc.IsMap():
		k := scalarOrMsgGoType(g, f.Message.Fields[0])
		v := scalarOrMsgGoType(g, f.Message.Fields[1])
		return "map[" + k + "]" + v
	case f.Desc.IsList():
		return "[]" + base
	case isOptionalPtr(f):
		return "*" + base
	default:
		return base
	}
}

func scalarOrMsgGoType(g *protogen.GeneratedFile, f *protogen.Field) string {
	switch f.Desc.Kind() {
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.EnumKind:
		return g.QualifiedGoIdent(f.Enum.GoIdent)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "[]byte"
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return "*" + g.QualifiedGoIdent(f.Message.GoIdent)
	default:
		panic(fmt.Sprintf("unsupported field kind: %v", f.Desc.Kind()))
	}
}

func zeroExpr(f *protogen.Field) string {
	if f.Desc.IsList() || f.Desc.IsMap() {
		return "nil"
	}
	switch f.Desc.Kind() {
	case protoreflect.BoolKind:
		return "false"
	case protoreflect.StringKind:
		return `""`
	case protoreflect.BytesKind, protoreflect.MessageKind, protoreflect.GroupKind:
		return "nil"
	default:
		return "0"
	}
}

func isRealOneof(f *protogen.Field) bool {
	return f.Oneof != nil && !f.Oneof.Desc.IsSynthetic()
}

func isOptionalPtr(f *protogen.Field) bool {
	return f.Oneof != nil && f.Oneof.Desc.IsSynthetic() &&
		f.Desc.Kind() != protoreflect.MessageKind && f.Desc.Kind() != protoreflect.BytesKind
}

func isPackable(f *protogen.Field) bool {
	switch f.Desc.Kind() {
	case protoreflect.BoolKind, protoreflect.EnumKind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
		protoreflect.FloatKind, protoreflect.DoubleKind:
		return true
	default:
		return false
	}
}

func fixedPackedElemSize(kind protoreflect.Kind) (int, bool) {
	switch kind {
	case protoreflect.BoolKind:
		return 1, true
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return 4, true
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return 8, true
	default:
		return 0, false
	}
}

func wireTypeExpr(g *protogen.GeneratedFile, kind protoreflect.Kind) string {
	switch kind {
	case protoreflect.BoolKind, protoreflect.EnumKind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Uint32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Uint64Kind:
		return g.QualifiedGoIdent(litewirePkg.Ident("VarintType"))
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return g.QualifiedGoIdent(litewirePkg.Ident("Fixed32Type"))
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return g.QualifiedGoIdent(litewirePkg.Ident("Fixed64Type"))
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind:
		return g.QualifiedGoIdent(litewirePkg.Ident("BytesType"))
	default:
		panic(fmt.Sprintf("unsupported wire kind: %v", kind))
	}
}

func genMessage(g *protogen.GeneratedFile, msg *protogen.Message, reach *reachSet, cyclic map[cycleEdge]bool, initStmts *[]string) {
	if msg.Desc.IsMapEntry() {
		return
	}
	for _, enum := range msg.Enums {
		if reach == nil || reach.enums[enum] {
			genEnum(g, enum)
		}
	}
	for _, nested := range msg.Messages {
		genMessage(g, nested, reach, cyclic, initStmts)
	}
	flags, ok := msgFlagsFor(msg, reach)
	if !ok {
		return
	}

	name := msg.GoIdent.GoName
	g.P("type ", name, " struct {")
	if len(msg.Fields) > 0 {
		g.P("\t_ [0]func()")
	}
	emittedOneofs := make(map[string]bool)
	for _, f := range msg.Fields {
		if isRealOneof(f) {
			if emittedOneofs[f.Oneof.GoName] {
				continue
			}
			emittedOneofs[f.Oneof.GoName] = true
			g.P("\t", f.Oneof.GoName, " is", name, "_", f.Oneof.GoName)
			continue
		}
		g.P("\t", f.GoName, " ", fieldGoType(g, f))
	}
	if flags.strict {
		g.P("\thasUnknown bool")
	}
	g.P("}")
	g.P()
	if flags.strict {
		g.P("func (m *", name, ") HasUnknown() bool { return m != nil && m.hasUnknown }")
		g.P()
	}

	for _, o := range msg.Oneofs {
		if o.Desc.IsSynthetic() {
			continue
		}
		iface := "is" + name + "_" + o.GoName
		g.P("type ", iface, " interface { ", iface, "() }")
		for _, f := range o.Fields {
			wrap := f.GoIdent.GoName
			g.P("type ", wrap, " struct { _ [0]func(); ", f.GoName, " ", fieldGoType(g, f), " }")
			g.P("func (*", wrap, ") ", iface, "() {}")
		}
		g.P()
	}

	for _, o := range msg.Oneofs {
		if o.Desc.IsSynthetic() {
			continue
		}
		iface := "is" + name + "_" + o.GoName
		g.P("func (m *", name, ") Get", o.GoName, "() ", iface, " {")
		g.P("\tif m != nil { return m.", o.GoName, " }")
		g.P("\treturn nil")
		g.P("}")
	}
	for _, f := range msg.Fields {
		if isRealOneof(f) {
			wrap := f.GoIdent.GoName
			g.P("func (m *", name, ") Get", f.GoName, "() ", fieldGoType(g, f), " {")
			g.P("\tif x, ok := m.Get", f.Oneof.GoName, "().(*", wrap, "); ok && x != nil { return x.", f.GoName, " }")
			g.P("\treturn ", zeroExpr(f))
			g.P("}")
		} else if isOptionalPtr(f) {
			base := scalarOrMsgGoType(g, f)
			g.P("func (m *", name, ") Get", f.GoName, "() ", base, " {")
			g.P("\tif m != nil && m.", f.GoName, " != nil { return *m.", f.GoName, " }")
			g.P("\treturn ", zeroExpr(f))
			g.P("}")
		} else {
			g.P("func (m *", name, ") Get", f.GoName, "() ", fieldGoType(g, f), " {")
			g.P("\tif m != nil { return m.", f.GoName, " }")
			g.P("\treturn ", zeroExpr(f))
			g.P("}")
		}
	}
	g.P()

	if flags.clone {
		genClone(g, msg)
	}
	if flags.marshal || flags.unmarshal {
		genTableDescAndMethods(g, msg, flags, cyclic, initStmts)
	}
}

func emitCloneOneofSwitch(g *protogen.GeneratedFile, indent, cpExpr, oneofGoName string, fields []*protogen.Field, cloneSlices func() string, withReturn bool) {
	g.P(indent, "switch v := m.", oneofGoName, ".(type) {")
	for _, of := range fields {
		wrap := of.GoIdent.GoName
		g.P(indent, "case *", wrap, ":")
		g.P(indent, "\tif v != nil {")
		switch of.Desc.Kind() {
		case protoreflect.MessageKind:
			g.P(indent, "\t\t", cpExpr, ".", oneofGoName, " = &", wrap, "{", of.GoName, ": v.", of.GoName, ".Clone()}")
		case protoreflect.BytesKind:
			g.P(indent, "\t\t", cpExpr, ".", oneofGoName, " = &", wrap, "{", of.GoName, ": ", cloneSlices(), "(v.", of.GoName, ")}")
		default:
			g.P(indent, "\t\tw := *v; ", cpExpr, ".", oneofGoName, " = &w")
		}
		g.P(indent, "\t}")
		if withReturn {
			g.P(indent, "\treturn true")
		}
	}
	g.P(indent, "}")
}

func genClone(g *protogen.GeneratedFile, msg *protogen.Message) {
	name := msg.GoIdent.GoName
	g.P("func (m *", name, ") Clone() *", name, " {")
	g.P("\tif m == nil { return nil }")
	g.P("\tcp := *m")
	cloneSlices := func() string { return g.QualifiedGoIdent(slicesPkg.Ident("Clone")) }
	emittedOneofs := make(map[string]bool)
	type chunkedCloneOneof struct {
		oneofName string
		chunks    [][]*protogen.Field
	}
	var chunkedOneofs []chunkedCloneOneof
	for _, f := range msg.Fields {
		if isRealOneof(f) {
			if emittedOneofs[f.Oneof.GoName] {
				continue
			}
			emittedOneofs[f.Oneof.GoName] = true
			if len(f.Oneof.Fields) > maxSwitchChunk {
				var chunks [][]*protogen.Field
				for i := 0; i < len(f.Oneof.Fields); i += maxSwitchChunk {
					end := min(i+maxSwitchChunk, len(f.Oneof.Fields))
					chunks = append(chunks, f.Oneof.Fields[i:end])
				}
				chunkedOneofs = append(chunkedOneofs, chunkedCloneOneof{oneofName: f.Oneof.GoName, chunks: chunks})
				var calls []string
				for ci := range chunks {
					calls = append(calls, fmt.Sprintf("m.clone%sPart%d(&cp)", f.Oneof.GoName, ci))
				}
				g.P("\tif m.", f.Oneof.GoName, " != nil {")
				g.P("\t\t_ = ", strings.Join(calls, " || "))
				g.P("\t}")
			} else {
				emitCloneOneofSwitch(g, "\t", "cp", f.Oneof.GoName, f.Oneof.Fields, cloneSlices, false)
			}
			continue
		}
		switch {
		case f.Desc.IsMap():
			valField := f.Message.Fields[1]
			g.P("\tif m.", f.GoName, " != nil {")
			g.P("\t\tcp.", f.GoName, " = make(", fieldGoType(g, f), ", len(m.", f.GoName, "))")
			g.P("\t\tfor k, v := range m.", f.GoName, " {")
			if valField.Desc.Kind() == protoreflect.MessageKind {
				g.P("\t\t\tcp.", f.GoName, "[k] = v.Clone()")
			} else if valField.Desc.Kind() == protoreflect.BytesKind {
				g.P("\t\t\tcp.", f.GoName, "[k] = ", cloneSlices(), "(v)")
			} else {
				g.P("\t\t\tcp.", f.GoName, "[k] = v")
			}
			g.P("\t\t}")
			g.P("\t}")
		case f.Desc.IsList():
			switch f.Desc.Kind() {
			case protoreflect.MessageKind:
				g.P("\tif m.", f.GoName, " != nil {")
				g.P("\t\tcp.", f.GoName, " = make(", fieldGoType(g, f), ", len(m.", f.GoName, "))")
				g.P("\t\tfor i, v := range m.", f.GoName, " { cp.", f.GoName, "[i] = v.Clone() }")
				g.P("\t}")
			case protoreflect.BytesKind:
				g.P("\tif m.", f.GoName, " != nil {")
				g.P("\t\tcp.", f.GoName, " = make(", fieldGoType(g, f), ", len(m.", f.GoName, "))")
				g.P("\t\tfor i, v := range m.", f.GoName, " { cp.", f.GoName, "[i] = ", cloneSlices(), "(v) }")
				g.P("\t}")
			default:
				g.P("\tcp.", f.GoName, " = ", cloneSlices(), "(m.", f.GoName, ")")
			}
		case isOptionalPtr(f):
			g.P("\tif m.", f.GoName, " != nil { v := *m.", f.GoName, "; cp.", f.GoName, " = &v }")
		case f.Desc.Kind() == protoreflect.MessageKind:
			g.P("\tcp.", f.GoName, " = m.", f.GoName, ".Clone()")
		case f.Desc.Kind() == protoreflect.BytesKind:
			g.P("\tcp.", f.GoName, " = ", cloneSlices(), "(m.", f.GoName, ")")
		}
	}
	g.P("\treturn &cp")
	g.P("}")
	g.P()
	for _, co := range chunkedOneofs {
		for ci, chunk := range co.chunks {
			g.P("//go:noinline")
			g.P("func (m *", name, ") clone", co.oneofName, "Part", ci, "(cp *", name, ") bool {")
			emitCloneOneofSwitch(g, "\t", "cp", co.oneofName, chunk, cloneSlices, true)
			g.P("\treturn false")
			g.P("}")
			g.P()
		}
	}
}

type customMarshalSlot struct {
	idx   int
	field *protogen.Field
}

type tableEntrySpec struct {
	field     *protogen.Field
	customIdx int
}

func litewireFieldKind(g *protogen.GeneratedFile, kind protoreflect.Kind) string {
	var id string
	switch kind {
	case protoreflect.BoolKind:
		id = "KindBool"
	case protoreflect.EnumKind, protoreflect.Int32Kind:
		id = "KindInt32"
	case protoreflect.Uint32Kind:
		id = "KindUint32"
	case protoreflect.Int64Kind:
		id = "KindInt64"
	case protoreflect.Uint64Kind:
		id = "KindUint64"
	case protoreflect.Sint32Kind:
		id = "KindSint32"
	case protoreflect.Sint64Kind:
		id = "KindSint64"
	case protoreflect.Fixed32Kind:
		id = "KindFixed32"
	case protoreflect.Sfixed32Kind:
		id = "KindSfixed32"
	case protoreflect.FloatKind:
		id = "KindFloat"
	case protoreflect.Fixed64Kind:
		id = "KindFixed64"
	case protoreflect.Sfixed64Kind:
		id = "KindSfixed64"
	case protoreflect.DoubleKind:
		id = "KindDouble"
	case protoreflect.StringKind:
		id = "KindString"
	case protoreflect.BytesKind:
		id = "KindBytes"
	case protoreflect.MessageKind:
		id = "KindMessage"
	default:
		panic(fmt.Sprintf("unsupported litewire field kind: %v", kind))
	}
	return g.QualifiedGoIdent(litewirePkg.Ident(id))
}

func litewireFieldFlagsExpr(g *protogen.GeneratedFile, f *protogen.Field) string {
	var parts []string
	if isOptionalPtr(f) {
		parts = append(parts, g.QualifiedGoIdent(litewirePkg.Ident("FlagOptional")))
	}
	if f.Desc.IsList() {
		parts = append(parts, g.QualifiedGoIdent(litewirePkg.Ident("FlagRepeated")))
		if isPackable(f) {
			parts = append(parts, g.QualifiedGoIdent(litewirePkg.Ident("FlagPacked")))
		}
	}
	if isExtraUnmarshalField(f) {
		parts = append(parts, g.QualifiedGoIdent(litewirePkg.Ident("FlagMarshalOnly")))
	}
	return strings.Join(parts, " | ")
}

func descIdentFor(g *protogen.GeneratedFile, msg *protogen.Message) string {
	return g.QualifiedGoIdent(protogen.GoIdent{
		GoName:       "LitewireDesc_" + msg.GoIdent.GoName,
		GoImportPath: msg.GoIdent.GoImportPath,
	})
}

func genTableDescAndMethods(g *protogen.GeneratedFile, msg *protogen.Message, flags msgFlags, cyclic map[cycleEdge]bool, initStmts *[]string) {
	name := msg.GoIdent.GoName
	descName := "LitewireDesc_" + name
	msgDescType := g.QualifiedGoIdent(litewirePkg.Ident("MessageDesc"))
	fieldType := g.QualifiedGoIdent(litewirePkg.Ident("Field"))
	numberType := g.QualifiedGoIdent(litewirePkg.Ident("Number"))
	kindCustom := g.QualifiedGoIdent(litewirePkg.Ident("KindCustom"))
	unsafePtr := g.QualifiedGoIdent(unsafePkg.Ident("Pointer"))
	unsafeOffsetof := g.QualifiedGoIdent(unsafePkg.Ident("Offsetof"))

	var tableEntries []tableEntrySpec
	var customSlots []customMarshalSlot
	var extraUnmarshalFields []*protogen.Field
	emittedOneofs := make(map[string]bool)

	for _, f := range msg.Fields {
		if isRealOneof(f) {
			if flags.unmarshal {
				extraUnmarshalFields = append(extraUnmarshalFields, f)
			}
			if emittedOneofs[f.Oneof.GoName] {
				continue
			}
			emittedOneofs[f.Oneof.GoName] = true
			if flags.marshal {
				slotIdx := len(customSlots) + 1
				customSlots = append(customSlots, customMarshalSlot{idx: slotIdx, field: f})
				tableEntries = append(tableEntries, tableEntrySpec{customIdx: slotIdx})
			}
			continue
		}
		if f.Desc.IsMap() {
			if flags.unmarshal {
				extraUnmarshalFields = append(extraUnmarshalFields, f)
			}
			if flags.marshal {
				slotIdx := len(customSlots) + 1
				customSlots = append(customSlots, customMarshalSlot{idx: slotIdx, field: f})
				tableEntries = append(tableEntries, tableEntrySpec{customIdx: slotIdx})
			}
			continue
		}
		if isExtraUnmarshalField(f) {
			if flags.unmarshal {
				extraUnmarshalFields = append(extraUnmarshalFields, f)
			}
			if flags.marshal {
				tableEntries = append(tableEntries, tableEntrySpec{field: f})
			}
			continue
		}
		tableEntries = append(tableEntries, tableEntrySpec{field: f})
	}

	g.P("var ", descName, " = ", msgDescType, "{")
	if flags.strict {
		g.P("\tStrict:        true,")
		g.P("\tHasUnknownOff: ", unsafeOffsetof, "(", name, "{}.hasUnknown),")
	}
	if flags.unmarshal {
		g.P("\tNew:           func() ", unsafePtr, " { return ", unsafePtr, "(&", name, "{}) },")
	}
	if len(tableEntries) > 0 {
		g.P("\tFields: []", fieldType, "{")
		for idx, te := range tableEntries {
			if te.customIdx > 0 {
				g.P("\t\t{Num: ", numberType, "(", te.customIdx, "), Kind: ", kindCustom, "},")
				continue
			}
			f := te.field
			line := fmt.Sprintf("\t\t{Num: %d, Offset: %s(%s{}.%s), Kind: %s",
				f.Desc.Number(), unsafeOffsetof, name, f.GoName, litewireFieldKind(g, f.Desc.Kind()))
			if fExpr := litewireFieldFlagsExpr(g, f); fExpr != "" {
				line += ", Flags: " + fExpr
			}
			if f.Desc.Kind() == protoreflect.MessageKind {
				targetDesc := "&" + descIdentFor(g, f.Message)
				if cyclic[cycleEdge{msg: msg, field: f}] {
					*initStmts = append(*initStmts, fmt.Sprintf("%s.Fields[%d].Sub = %s", descName, idx, targetDesc))
				} else {
					line += ", Sub: " + targetDesc
				}
			}
			line += "},"
			g.P(line)
		}
		g.P("\t},")
	}
	g.P("}")
	g.P()

	if flags.marshal {
		appendTable := g.QualifiedGoIdent(litewirePkg.Ident("AppendTable"))
		g.P("func (m *", name, ") Marshal() ([]byte, error) {")
		g.P("\treturn m.AppendMarshal(nil), nil")
		g.P("}")
		g.P()
		g.P("func (m *", name, ") AppendMarshal(b []byte) []byte {")
		g.P("\treturn ", appendTable, "(b, ", unsafePtr, "(m), &", descName, ")")
		g.P("}")
		g.P()
		if len(customSlots) > 0 {
			genExtraMarshal(g, msg, customSlots)
			*initStmts = append(*initStmts, descName+".ExtraMarshal = extraMarshal_"+name)
		}
	}

	if flags.unmarshal {
		unmarshalTable := g.QualifiedGoIdent(litewirePkg.Ident("UnmarshalTable"))
		g.P("func (m *", name, ") Unmarshal(b []byte) error {")
		g.P("\treturn ", unmarshalTable, "(b, ", unsafePtr, "(m), &", descName, ")")
		g.P("}")
		g.P()
		if len(extraUnmarshalFields) > 0 {
			genExtraUnmarshal(g, msg, extraUnmarshalFields, flags.strict)
			*initStmts = append(*initStmts, descName+".ExtraUnmarshal = extraUnmarshal_"+name)
		}
	}
}

func emitMarshalCustomSlot(g *protogen.GeneratedFile, indent string, f *protogen.Field, name string, chunkedOneofs *[]chunkedMarshalOneof) {
	appendMessage := g.QualifiedGoIdent(litewirePkg.Ident("AppendMessage"))
	beginMessage := g.QualifiedGoIdent(litewirePkg.Ident("BeginMessage"))
	endMessage := g.QualifiedGoIdent(litewirePkg.Ident("EndMessage"))
	numberType := g.QualifiedGoIdent(litewirePkg.Ident("Number"))
	marshalerType := g.QualifiedGoIdent(litewirePkg.Ident("Marshaler"))

	if isRealOneof(f) {
		if len(f.Oneof.Fields) > maxSwitchChunk {
			var chunks [][]*protogen.Field
			for i := 0; i < len(f.Oneof.Fields); i += maxSwitchChunk {
				end := min(i+maxSwitchChunk, len(f.Oneof.Fields))
				chunks = append(chunks, f.Oneof.Fields[i:end])
			}
			*chunkedOneofs = append(*chunkedOneofs, chunkedMarshalOneof{oneofName: f.Oneof.GoName, chunks: chunks})
			g.P(indent, "if m.", f.Oneof.GoName, " != nil {")
			g.P(indent, "\tvar ok bool")
			for ci := range chunks {
				if ci == 0 {
					g.P(indent, "\tb, ok = m.appendMarshal", f.Oneof.GoName, "Part", ci, "(b)")
				} else {
					g.P(indent, "\tif !ok { b, ok = m.appendMarshal", f.Oneof.GoName, "Part", ci, "(b) }")
				}
			}
			g.P(indent, "}")
			return
		}
		msgCount := 0
		for _, of := range f.Oneof.Fields {
			if of.Desc.Kind() == protoreflect.MessageKind {
				msgCount++
			}
		}
		if msgCount >= 2 {
			g.P(indent, "var onum ", numberType)
			g.P(indent, "var omsg ", marshalerType)
		}
		g.P(indent, "switch x := m.", f.Oneof.GoName, ".(type) {")
		for _, of := range f.Oneof.Fields {
			wrap := of.GoIdent.GoName
			g.P(indent, "case *", wrap, ":")
			if msgCount >= 2 && of.Desc.Kind() == protoreflect.MessageKind {
				g.P(indent, "\tif x != nil && x.", of.GoName, " != nil { onum, omsg = ", of.Desc.Number(), ", x.", of.GoName, " }")
			} else {
				g.P(indent, "\tif x != nil {")
				emitMarshalField(g, indent+"\t\t", "b", of, "x."+of.GoName, true)
				g.P(indent, "\t}")
			}
		}
		g.P(indent, "}")
		if msgCount >= 2 {
			g.P(indent, "if omsg != nil { b = ", appendMessage, "(b, onum, omsg) }")
		}
		return
	}

	num := f.Desc.Number()
	kf := f.Message.Fields[0]
	vf := f.Message.Fields[1]
	g.P(indent, "for k, v := range m.", f.GoName, " {")
	g.P(indent, "\tvar start int")
	g.P(indent, "\tb, start = ", beginMessage, "(b, ", num, ")")
	emitMarshalField(g, indent+"\t", "b", kf, "k", true)
	if vf.Desc.Kind() == protoreflect.MessageKind {
		// A nil value is still written, as an empty message.
		g.P(indent, "\tb = ", appendMessage, "(b, 2, v)")
	} else {
		emitMarshalField(g, indent+"\t", "b", vf, "v", true)
	}
	g.P(indent, "\tb = ", endMessage, "(b, start)")
	g.P(indent, "}")
}

type chunkedMarshalOneof struct {
	oneofName string
	chunks    [][]*protogen.Field
}

func genExtraMarshal(g *protogen.GeneratedFile, msg *protogen.Message, slots []customMarshalSlot) {
	name := msg.GoIdent.GoName
	appendMessage := g.QualifiedGoIdent(litewirePkg.Ident("AppendMessage"))
	numberType := g.QualifiedGoIdent(litewirePkg.Ident("Number"))
	marshalerType := g.QualifiedGoIdent(litewirePkg.Ident("Marshaler"))
	unsafePtr := g.QualifiedGoIdent(unsafePkg.Ident("Pointer"))

	var chunkedOneofs []chunkedMarshalOneof
	g.P("func extraMarshal_", name, "(base ", unsafePtr, ", b []byte, idx ", numberType, ") []byte {")
	g.P("\tm := (*", name, ")(base)")
	if len(slots) == 1 {
		emitMarshalCustomSlot(g, "\t", slots[0].field, name, &chunkedOneofs)
	} else {
		g.P("\tswitch idx {")
		for _, s := range slots {
			g.P("\tcase ", s.idx, ":")
			emitMarshalCustomSlot(g, "\t\t", s.field, name, &chunkedOneofs)
		}
		g.P("\t}")
	}
	g.P("\treturn b")
	g.P("}")
	g.P()
	for _, co := range chunkedOneofs {
		for ci, chunk := range co.chunks {
			msgCount := 0
			for _, of := range chunk {
				if of.Desc.Kind() == protoreflect.MessageKind {
					msgCount++
				}
			}
			g.P("//go:noinline")
			g.P("func (m *", name, ") appendMarshal", co.oneofName, "Part", ci, "(b []byte) ([]byte, bool) {")
			if msgCount >= 2 {
				g.P("\tvar onum ", numberType)
				g.P("\tvar omsg ", marshalerType)
			}
			g.P("\tswitch x := m.", co.oneofName, ".(type) {")
			for _, of := range chunk {
				wrap := of.GoIdent.GoName
				g.P("\tcase *", wrap, ":")
				if msgCount >= 2 && of.Desc.Kind() == protoreflect.MessageKind {
					g.P("\t\tif x != nil && x.", of.GoName, " != nil { onum, omsg = ", of.Desc.Number(), ", x.", of.GoName, " }")
				} else {
					g.P("\t\tif x != nil {")
					emitMarshalField(g, "\t\t\t", "b", of, "x."+of.GoName, true)
					g.P("\t\t}")
					g.P("\t\treturn b, true")
				}
			}
			g.P("\tdefault:")
			g.P("\t\treturn b, false")
			g.P("\t}")
			if msgCount >= 2 {
				g.P("\tif omsg != nil { b = ", appendMessage, "(b, onum, omsg) }")
			}
			g.P("\treturn b, true")
			g.P("}")
			g.P()
		}
	}
}

func emitMarshalField(g *protogen.GeneratedFile, indent, buf string, f *protogen.Field, expr string, always bool) {
	num := f.Desc.Number()
	suffix := ""
	if always {
		suffix = "Always"
	}
	switch f.Desc.Kind() {
	case protoreflect.MessageKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendMessage"))
		g.P(indent, "if ", expr, " != nil { ", buf, " = ", fn, "(", buf, ", ", num, ", ", expr, ") }")
	case protoreflect.BytesKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendBytes" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", ", expr, ")")
	case protoreflect.StringKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendString" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", ", expr, ")")
	case protoreflect.BoolKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendBool" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", ", expr, ")")
	case protoreflect.EnumKind, protoreflect.Int32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendInt32" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", int32(", expr, "))")
	case protoreflect.Int64Kind, protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendVarint" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", uint64(", expr, "))")
	case protoreflect.Sint32Kind, protoreflect.Sint64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendZigZag" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", int64(", expr, "))")
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendFixed32" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", uint32(", expr, "))")
	case protoreflect.FloatKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendFloat" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", ", expr, ")")
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendFixed64" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", uint64(", expr, "))")
	case protoreflect.DoubleKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("AppendDouble" + suffix))
		g.P(indent, buf, " = ", fn, "(", buf, ", ", num, ", ", expr, ")")
	default:
		panic(fmt.Sprintf("unsupported kind: %v", f.Desc.Kind()))
	}
}

func emitMarshalPackedElem(g *protogen.GeneratedFile, indent, buf string, f *protogen.Field, expr string) {
	appendVarint := g.QualifiedGoIdent(litewirePkg.Ident("AppendRawVarint"))
	switch f.Desc.Kind() {
	case protoreflect.BoolKind:
		encodeBool := g.QualifiedGoIdent(litewirePkg.Ident("EncodeBool"))
		g.P(indent, buf, " = ", appendVarint, "(", buf, ", ", encodeBool, "(", expr, "))")
	case protoreflect.EnumKind, protoreflect.Int32Kind:
		g.P(indent, buf, " = ", appendVarint, "(", buf, ", uint64(int64(", expr, ")))")
	case protoreflect.Int64Kind, protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		g.P(indent, buf, " = ", appendVarint, "(", buf, ", uint64(", expr, "))")
	case protoreflect.Sint32Kind, protoreflect.Sint64Kind:
		encodeZigZag := g.QualifiedGoIdent(litewirePkg.Ident("EncodeZigZag"))
		g.P(indent, buf, " = ", appendVarint, "(", buf, ", ", encodeZigZag, "(int64(", expr, ")))")
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind:
		appendFixed32 := g.QualifiedGoIdent(litewirePkg.Ident("AppendRawFixed32"))
		g.P(indent, buf, " = ", appendFixed32, "(", buf, ", uint32(", expr, "))")
	case protoreflect.FloatKind:
		appendFixed32 := g.QualifiedGoIdent(litewirePkg.Ident("AppendRawFixed32"))
		float32bits := g.QualifiedGoIdent(mathPkg.Ident("Float32bits"))
		g.P(indent, buf, " = ", appendFixed32, "(", buf, ", ", float32bits, "(", expr, "))")
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		appendFixed64 := g.QualifiedGoIdent(litewirePkg.Ident("AppendRawFixed64"))
		g.P(indent, buf, " = ", appendFixed64, "(", buf, ", uint64(", expr, "))")
	case protoreflect.DoubleKind:
		appendFixed64 := g.QualifiedGoIdent(litewirePkg.Ident("AppendRawFixed64"))
		float64bits := g.QualifiedGoIdent(mathPkg.Ident("Float64bits"))
		g.P(indent, buf, " = ", appendFixed64, "(", buf, ", ", float64bits, "(", expr, "))")
	}
}

func scalarIntoHelper(kind protoreflect.Kind) string {
	switch kind {
	case protoreflect.StringKind:
		return "ReadStringInto"
	case protoreflect.BytesKind:
		return "ReadBytesInto"
	case protoreflect.BoolKind:
		return "ReadBoolInto"
	case protoreflect.Int32Kind:
		return "ReadInt32Into"
	case protoreflect.Uint32Kind:
		return "ReadUint32Into"
	case protoreflect.Int64Kind:
		return "ReadInt64Into"
	case protoreflect.Uint64Kind:
		return "ReadUint64Into"
	case protoreflect.Sint32Kind:
		return "ReadSint32Into"
	case protoreflect.Sint64Kind:
		return "ReadSint64Into"
	case protoreflect.Fixed32Kind:
		return "ReadFixed32Into"
	case protoreflect.Sfixed32Kind:
		return "ReadSfixed32Into"
	case protoreflect.FloatKind:
		return "ReadFloatInto"
	case protoreflect.Fixed64Kind:
		return "ReadFixed64Into"
	case protoreflect.Sfixed64Kind:
		return "ReadSfixed64Into"
	case protoreflect.DoubleKind:
		return "ReadDoubleInto"
	default:
		return ""
	}
}

func fieldChunkStats(fields []*protogen.Field) (sharedSub, allBytesWire bool) {
	msgFieldCount := 0
	allBytesWire = len(fields) > 0
	for _, f := range fields {
		if !f.Desc.IsMap() && f.Desc.Kind() == protoreflect.MessageKind {
			msgFieldCount++
		}
		if !(f.Desc.IsMap() || f.Desc.Kind() == protoreflect.MessageKind || f.Desc.Kind() == protoreflect.BytesKind || f.Desc.Kind() == protoreflect.StringKind) {
			allBytesWire = false
		}
	}
	return msgFieldCount >= 2, allBytesWire
}

func emitExtraUnmarshalChunk(g *protogen.GeneratedFile, indent string, fields []*protogen.Field, strict bool) {
	bytesType := g.QualifiedGoIdent(litewirePkg.Ident("BytesType"))
	sharedSub, allBytesWire := fieldChunkStats(fields)
	if sharedSub {
		subIface := "Unmarshaler"
		if strict {
			subIface = "StrictUnmarshaler"
		}
		g.P(indent, "var sub ", g.QualifiedGoIdent(litewirePkg.Ident(subIface)))
	}
	inner := indent
	if allBytesWire {
		g.P(indent, "if wtyp == ", bytesType, " {")
		inner = indent + "\t"
	}
	g.P(inner, "switch num {")
	for _, f := range fields {
		g.P(inner, "case ", f.Desc.Number(), ":")
		emitUnmarshalField(g, inner+"\t", f, strict, sharedSub, allBytesWire, "return false, err", "*bp = b; return true, nil")
	}
	g.P(inner, "}")
	if sharedSub {
		if strict {
			readStrict := g.QualifiedGoIdent(litewirePkg.Ident("ReadStrictMessage"))
			g.P(inner, "if sub != nil { if err := ", readStrict, "(&b, sub, &m.hasUnknown); err != nil { return false, err }; *bp = b; return true, nil }")
		} else {
			readMessage := g.QualifiedGoIdent(litewirePkg.Ident("ReadMessage"))
			g.P(inner, "if sub != nil { if err := ", readMessage, "(&b, sub); err != nil { return false, err }; *bp = b; return true, nil }")
		}
	}
	if allBytesWire {
		g.P(indent, "}")
	}
}

func genExtraUnmarshal(g *protogen.GeneratedFile, msg *protogen.Message, fields []*protogen.Field, strict bool) {
	name := msg.GoIdent.GoName
	numberType := g.QualifiedGoIdent(litewirePkg.Ident("Number"))
	wireType := g.QualifiedGoIdent(litewirePkg.Ident("Type"))
	unsafePtr := g.QualifiedGoIdent(unsafePkg.Ident("Pointer"))

	if len(fields) > maxSwitchChunk {
		sorted := slices.Clone(fields)
		slices.SortFunc(sorted, func(a, b *protogen.Field) int {
			return cmp.Compare(a.Desc.Number(), b.Desc.Number())
		})
		var chunks [][]*protogen.Field
		for i := 0; i < len(sorted); i += maxSwitchChunk {
			end := min(i+maxSwitchChunk, len(sorted))
			chunks = append(chunks, sorted[i:end])
		}
		g.P("func extraUnmarshal_", name, "(base ", unsafePtr, ", num ", numberType, ", wtyp ", wireType, ", bp *[]byte) (bool, error) {")
		g.P("\tm := (*", name, ")(base)")
		g.P("\tswitch {")
		for ci, chunk := range chunks {
			maxNum := chunk[len(chunk)-1].Desc.Number()
			g.P("\tcase num <= ", maxNum, ":")
			g.P("\t\treturn m.extraUnmarshalPart", ci, "(num, wtyp, bp)")
		}
		g.P("\t}")
		g.P("\treturn false, nil")
		g.P("}")
		g.P()
		for ci, chunk := range chunks {
			g.P("//go:noinline")
			g.P("func (m *", name, ") extraUnmarshalPart", ci, "(num ", numberType, ", wtyp ", wireType, ", bp *[]byte) (bool, error) {")
			g.P("\tb := *bp")
			emitExtraUnmarshalChunk(g, "\t", chunk, strict)
			g.P("\treturn false, nil")
			g.P("}")
			g.P()
		}
		return
	}

	g.P("func extraUnmarshal_", name, "(base ", unsafePtr, ", num ", numberType, ", wtyp ", wireType, ", bp *[]byte) (bool, error) {")
	g.P("\tm := (*", name, ")(base)")
	g.P("\tb := *bp")
	emitExtraUnmarshalChunk(g, "\t", fields, strict)
	g.P("\treturn false, nil")
	g.P("}")
	g.P()
}

func emitUnmarshalField(g *protogen.GeneratedFile, indent string, f *protogen.Field, strict, sharedSub, knownBytesWire bool, retErr, doneField string) {
	bytesType := g.QualifiedGoIdent(litewirePkg.Ident("BytesType"))
	readBytesRaw := g.QualifiedGoIdent(litewirePkg.Ident("ReadBytesRaw"))
	readMessage := g.QualifiedGoIdent(litewirePkg.Ident("ReadMessage"))
	readStrict := g.QualifiedGoIdent(litewirePkg.Ident("ReadStrictMessage"))
	readTag := g.QualifiedGoIdent(litewirePkg.Ident("ReadTag"))
	skip := g.QualifiedGoIdent(litewirePkg.Ident("Skip"))

	if f.Desc.IsMap() {
		kf := f.Message.Fields[0]
		vf := f.Message.Fields[1]
		inner := indent
		if !knownBytesWire {
			g.P(indent, "if wtyp == ", bytesType, " {")
			inner = indent + "\t"
		}
		g.P(inner, "raw, err := ", readBytesRaw, "(&b)")
		g.P(inner, "if err != nil { ", retErr, " }")
		g.P(inner, "var mk ", scalarOrMsgGoType(g, kf))
		g.P(inner, "var mv ", scalarOrMsgGoType(g, vf))
		if vf.Desc.Kind() == protoreflect.MessageKind {
			g.P(inner, "mv = &", g.QualifiedGoIdent(vf.Message.GoIdent), "{}")
		}
		g.P(inner, "for len(raw) > 0 {")
		g.P(inner, "\tmnum, mwtyp, err := ", readTag, "(&raw)")
		g.P(inner, "\tif err != nil { ", retErr, " }")
		g.P(inner, "\tswitch mnum {")
		g.P(inner, "\tcase 1:")
		emitUnmarshalScalarOrBytes(g, inner+"\t\t", "raw", "mwtyp", kf, retErr, func(val string) {
			g.P(inner, "\t\tmk = ", val)
			g.P(inner, "\t\tcontinue")
		})
		g.P(inner, "\tcase 2:")
		if vf.Desc.Kind() == protoreflect.MessageKind {
			g.P(inner, "\t\tif mwtyp == ", bytesType, " {")
			if strict {
				g.P(inner, "\t\t\tif err := ", readStrict, "(&raw, mv, &m.hasUnknown); err != nil { ", retErr, " }")
			} else {
				g.P(inner, "\t\t\tif err := ", readMessage, "(&raw, mv); err != nil { ", retErr, " }")
			}
			g.P(inner, "\t\t\tcontinue")
			g.P(inner, "\t\t}")
		} else {
			emitUnmarshalScalarOrBytes(g, inner+"\t\t", "raw", "mwtyp", vf, retErr, func(val string) {
				g.P(inner, "\t\tmv = ", val)
				g.P(inner, "\t\tcontinue")
			})
		}
		g.P(inner, "\t}")
		if strict {
			g.P(inner, "\tm.hasUnknown = true")
		}
		g.P(inner, "\tif err := ", skip, "(&raw, mnum, mwtyp); err != nil { ", retErr, " }")
		g.P(inner, "}")
		g.P(inner, "if m.", f.GoName, " == nil { m.", f.GoName, " = make(", fieldGoType(g, f), ") }")
		g.P(inner, "m.", f.GoName, "[mk] = mv")
		g.P(inner, doneField)
		if !knownBytesWire {
			g.P(indent, "}")
		}
		return
	}

	if f.Desc.IsList() {
		if isPackable(f) {
			g.P(indent, "if wtyp == ", bytesType, " {")
			g.P(indent, "\traw, err := ", readBytesRaw, "(&b)")
			g.P(indent, "\tif err != nil { ", retErr, " }")
			g.P(indent, "\tfor len(raw) > 0 {")
			emitUnmarshalRawScalarInto(g, indent+"\t\t", "raw", f, retErr, func(val string) {
				g.P(indent, "\t\tm.", f.GoName, " = append(m.", f.GoName, ", ", val, ")")
			})
			g.P(indent, "\t}")
			g.P(indent, "\t", doneField)
			g.P(indent, "}")
		}
		if f.Desc.Kind() == protoreflect.MessageKind {
			inner := indent
			if !knownBytesWire {
				g.P(indent, "if wtyp == ", bytesType, " {")
				inner = indent + "\t"
			}
			g.P(inner, "elem := &", g.QualifiedGoIdent(f.Message.GoIdent), "{}")
			g.P(inner, "m.", f.GoName, " = append(m.", f.GoName, ", elem)")
			if sharedSub {
				g.P(inner, "sub = elem")
			} else if strict {
				g.P(inner, "if err := ", readStrict, "(&b, elem, &m.hasUnknown); err != nil { ", retErr, " }")
				g.P(inner, doneField)
			} else {
				g.P(inner, "if err := ", readMessage, "(&b, elem); err != nil { ", retErr, " }")
				g.P(inner, doneField)
			}
			if !knownBytesWire {
				g.P(indent, "}")
			}
			return
		}
		emitUnmarshalScalarOrBytes(g, indent, "b", "wtyp", f, retErr, func(val string) {
			g.P(indent, "\tm.", f.GoName, " = append(m.", f.GoName, ", ", val, ")")
			g.P(indent, "\t", doneField)
		})
		return
	}

	if f.Desc.Kind() == protoreflect.MessageKind {
		inner := indent
		if !knownBytesWire {
			g.P(indent, "if wtyp == ", bytesType, " {")
			inner = indent + "\t"
		}
		if isRealOneof(f) {
			wrap := f.GoIdent.GoName
			g.P(inner, "w, _ := m.", f.Oneof.GoName, ".(*", wrap, ")")
			g.P(inner, "if w == nil || w.", f.GoName, " == nil {")
			g.P(inner, "\tw = &", wrap, "{", f.GoName, ": &", g.QualifiedGoIdent(f.Message.GoIdent), "{}}")
			g.P(inner, "\tm.", f.Oneof.GoName, " = w")
			g.P(inner, "}")
			if sharedSub {
				g.P(inner, "sub = w.", f.GoName)
			} else if strict {
				g.P(inner, "if err := ", readStrict, "(&b, w.", f.GoName, ", &m.hasUnknown); err != nil { ", retErr, " }")
				g.P(inner, doneField)
			} else {
				g.P(inner, "if err := ", readMessage, "(&b, w.", f.GoName, "); err != nil { ", retErr, " }")
				g.P(inner, doneField)
			}
		} else {
			g.P(inner, "if m.", f.GoName, " == nil { m.", f.GoName, " = &", g.QualifiedGoIdent(f.Message.GoIdent), "{} }")
			if sharedSub {
				g.P(inner, "sub = m.", f.GoName)
			} else if strict {
				g.P(inner, "if err := ", readStrict, "(&b, m.", f.GoName, ", &m.hasUnknown); err != nil { ", retErr, " }")
				g.P(inner, doneField)
			} else {
				g.P(inner, "if err := ", readMessage, "(&b, m.", f.GoName, "); err != nil { ", retErr, " }")
				g.P(inner, doneField)
			}
		}
		if !knownBytesWire {
			g.P(indent, "}")
		}
		return
	}

	if !isRealOneof(f) && !isOptionalPtr(f) {
		if helper := scalarIntoHelper(f.Desc.Kind()); helper != "" {
			fn := g.QualifiedGoIdent(litewirePkg.Ident(helper))
			g.P(indent, "if ok, err := ", fn, "(&b, wtyp, &m.", f.GoName, "); err != nil { ", retErr, " } else if ok { ", doneField, " }")
			return
		}
	}

	emitUnmarshalScalarOrBytes(g, indent, "b", "wtyp", f, retErr, func(val string) {
		if isRealOneof(f) {
			wrap := f.GoIdent.GoName
			g.P(indent, "\tm.", f.Oneof.GoName, " = &", wrap, "{", f.GoName, ": ", val, "}")
		} else if isOptionalPtr(f) {
			g.P(indent, "\tpv := ", val)
			g.P(indent, "\tm.", f.GoName, " = &pv")
		} else {
			g.P(indent, "\tm.", f.GoName, " = ", val)
		}
		g.P(indent, "\t", doneField)
	})
}

func emitUnmarshalScalarOrBytes(g *protogen.GeneratedFile, indent, buf, wtypVar string, f *protogen.Field, retErr string, assign func(string)) {
	wtyp := wireTypeExpr(g, f.Desc.Kind())
	g.P(indent, "if ", wtypVar, " == ", wtyp, " {")
	emitUnmarshalRawScalarInto(g, indent+"\t", buf, f, retErr, assign)
	g.P(indent, "}")
}

func emitUnmarshalRawScalarInto(g *protogen.GeneratedFile, indent, buf string, f *protogen.Field, retErr string, assign func(string)) {
	switch f.Desc.Kind() {
	case protoreflect.StringKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadString"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("v")
	case protoreflect.BytesKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadBytes"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("v")
	case protoreflect.BoolKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		decodeBool := g.QualifiedGoIdent(litewirePkg.Ident("DecodeBool"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign(decodeBool + "(v)")
	case protoreflect.EnumKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign(g.QualifiedGoIdent(f.Enum.GoIdent) + "(int32(v))")
	case protoreflect.Int32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("int32(v)")
	case protoreflect.Uint32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("uint32(v)")
	case protoreflect.Int64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("int64(v)")
	case protoreflect.Uint64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("v")
	case protoreflect.Sint32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		decodeZigZag := g.QualifiedGoIdent(litewirePkg.Ident("DecodeZigZag"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("int32(" + decodeZigZag + "(v))")
	case protoreflect.Sint64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		decodeZigZag := g.QualifiedGoIdent(litewirePkg.Ident("DecodeZigZag"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign(decodeZigZag + "(v)")
	case protoreflect.Fixed32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed32"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("v")
	case protoreflect.Sfixed32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed32"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("int32(v)")
	case protoreflect.FloatKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed32"))
		float32frombits := g.QualifiedGoIdent(mathPkg.Ident("Float32frombits"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign(float32frombits + "(v)")
	case protoreflect.Fixed64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed64"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("v")
	case protoreflect.Sfixed64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed64"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign("int64(v)")
	case protoreflect.DoubleKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed64"))
		float64frombits := g.QualifiedGoIdent(mathPkg.Ident("Float64frombits"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { ", retErr, " }")
		assign(float64frombits + "(v)")
	default:
		panic(fmt.Sprintf("unsupported raw scalar kind: %v", f.Desc.Kind()))
	}
}
