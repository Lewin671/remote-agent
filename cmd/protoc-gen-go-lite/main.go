// Command protoc-gen-go-lite generates minimal, reflection-free Go protobuf structs and
// direction-aware litewire Marshal/Unmarshal/Size/Clone methods covering the subset of proto3 used in this repo.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"
)

var (
	litewirePkg = protogen.GoImportPath("github.com/Lewin671/remote-agent/clientcore/gen/litewire")
	mathPkg     = protogen.GoImportPath("math")
	slicesPkg   = protogen.GoImportPath("slices")
)

type msgFlags struct {
	marshal   bool
	unmarshal bool
	clone     bool
}

func (a msgFlags) or(b msgFlags) msgFlags {
	return msgFlags{
		marshal:   a.marshal || b.marshal,
		unmarshal: a.unmarshal || b.unmarshal,
		clone:     a.clone || b.clone,
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
		for _, f := range plugin.Files {
			if !f.Generate || !fileHasReachable(f, reach) {
				continue
			}
			generateFile(plugin, f, reach)
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

func generateFile(plugin *protogen.Plugin, file *protogen.File, reach *reachSet) {
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
	for _, msg := range file.Messages {
		genMessage(g, msg, reach)
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

func genMessage(g *protogen.GeneratedFile, msg *protogen.Message, reach *reachSet) {
	if msg.Desc.IsMapEntry() {
		return
	}
	for _, enum := range msg.Enums {
		if reach == nil || reach.enums[enum] {
			genEnum(g, enum)
		}
	}
	for _, nested := range msg.Messages {
		genMessage(g, nested, reach)
	}
	flags := msgFlags{marshal: true, unmarshal: true, clone: true}
	if reach != nil {
		var ok bool
		flags, ok = reach.msgs[msg]
		if !ok {
			return
		}
	}

	name := msg.GoIdent.GoName
	g.P("type ", name, " struct {")
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
	g.P("}")
	g.P()

	for _, o := range msg.Oneofs {
		if o.Desc.IsSynthetic() {
			continue
		}
		iface := "is" + name + "_" + o.GoName
		g.P("type ", iface, " interface { ", iface, "() }")
		for _, f := range o.Fields {
			wrap := f.GoIdent.GoName
			g.P("type ", wrap, " struct { ", f.GoName, " ", fieldGoType(g, f), " }")
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
	if flags.marshal {
		genSize(g, msg)
		genMarshal(g, msg)
	}
	if flags.unmarshal {
		genUnmarshal(g, msg)
	}
}

func genClone(g *protogen.GeneratedFile, msg *protogen.Message) {
	name := msg.GoIdent.GoName
	g.P("func (m *", name, ") Clone() *", name, " {")
	g.P("\tif m == nil { return nil }")
	g.P("\tcp := *m")
	cloneSlices := func() string { return g.QualifiedGoIdent(slicesPkg.Ident("Clone")) }
	emittedOneofs := make(map[string]bool)
	for _, f := range msg.Fields {
		if isRealOneof(f) {
			if emittedOneofs[f.Oneof.GoName] {
				continue
			}
			emittedOneofs[f.Oneof.GoName] = true
			g.P("\tswitch v := m.", f.Oneof.GoName, ".(type) {")
			for _, of := range f.Oneof.Fields {
				wrap := of.GoIdent.GoName
				g.P("\tcase *", wrap, ":")
				g.P("\t\tif v != nil {")
				switch of.Desc.Kind() {
				case protoreflect.MessageKind:
					g.P("\t\t\tcp.", f.Oneof.GoName, " = &", wrap, "{", of.GoName, ": v.", of.GoName, ".Clone()}")
				case protoreflect.BytesKind:
					g.P("\t\t\tcp.", f.Oneof.GoName, " = &", wrap, "{", of.GoName, ": ", cloneSlices(), "(v.", of.GoName, ")}")
				default:
					g.P("\t\t\tw := *v; cp.", f.Oneof.GoName, " = &w")
				}
				g.P("\t\t}")
			}
			g.P("\t}")
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
}

// genSize emits Size for callers that want the encoded length; encoding itself fills
// lengths in after the fact (litewire.BeginMessage/EndMessage) and never calls it.
func genSize(g *protogen.GeneratedFile, msg *protogen.Message) {
	name := msg.GoIdent.GoName
	g.P("func (m *", name, ") Size() int { return len(m.AppendMarshal(nil)) }")
	g.P()
}

func genMarshal(g *protogen.GeneratedFile, msg *protogen.Message) {
	name := msg.GoIdent.GoName
	appendMsgPrefix := g.QualifiedGoIdent(litewirePkg.Ident("AppendMessagePrefix"))
	appendMessage := g.QualifiedGoIdent(litewirePkg.Ident("AppendMessage"))
	beginMessage := g.QualifiedGoIdent(litewirePkg.Ident("BeginMessage"))
	endMessage := g.QualifiedGoIdent(litewirePkg.Ident("EndMessage"))

	g.P("func (m *", name, ") Marshal() ([]byte, error) {")
	g.P("\treturn m.AppendMarshal(nil), nil")
	g.P("}")
	g.P()

	g.P("func (m *", name, ") AppendMarshal(b []byte) []byte {")
	g.P("\tif m == nil { return b }")
	emittedOneofs := make(map[string]bool)
	for _, f := range msg.Fields {
		if isRealOneof(f) {
			if emittedOneofs[f.Oneof.GoName] {
				continue
			}
			emittedOneofs[f.Oneof.GoName] = true
			g.P("\tswitch x := m.", f.Oneof.GoName, ".(type) {")
			for _, of := range f.Oneof.Fields {
				wrap := of.GoIdent.GoName
				g.P("\tcase *", wrap, ":")
				g.P("\t\tif x != nil {")
				emitMarshalField(g, "\t\t\t", "b", of, "x."+of.GoName, true)
				g.P("\t\t}")
			}
			g.P("\t}")
			continue
		}
		num := f.Desc.Number()
		switch {
		case f.Desc.IsMap():
			kf := f.Message.Fields[0]
			vf := f.Message.Fields[1]
			g.P("\tfor k, v := range m.", f.GoName, " {")
			g.P("\t\tvar start int")
			g.P("\t\tb, start = ", beginMessage, "(b, ", num, ")")
			emitMarshalField(g, "\t\t", "b", kf, "k", true)
			if vf.Desc.Kind() == protoreflect.MessageKind {
				// A nil value is still written, as an empty message.
				g.P("\t\tb = ", appendMessage, "(b, 2, v)")
			} else {
				emitMarshalField(g, "\t\t", "b", vf, "v", true)
			}
			g.P("\t\tb = ", endMessage, "(b, start)")
			g.P("\t}")
		case f.Desc.IsList():
			if isPackable(f) {
				g.P("\tif len(m.", f.GoName, ") > 0 {")
				elemSz, fixed := fixedPackedElemSize(f.Desc.Kind())
				switch {
				case fixed && elemSz == 1:
					g.P("\t\tb = ", appendMsgPrefix, "(b, ", num, ", len(m.", f.GoName, "))")
				case fixed:
					g.P("\t\tb = ", appendMsgPrefix, "(b, ", num, ", len(m.", f.GoName, ")*", elemSz, ")")
				default:
					g.P("\t\tvar start int")
					g.P("\t\tb, start = ", beginMessage, "(b, ", num, ")")
				}
				g.P("\t\tfor _, v := range m.", f.GoName, " {")
				emitMarshalPackedElem(g, "\t\t\t", "b", f, "v")
				g.P("\t\t}")
				if !fixed {
					g.P("\t\tb = ", endMessage, "(b, start)")
				}
				g.P("\t}")
			} else {
				g.P("\tfor _, v := range m.", f.GoName, " {")
				emitMarshalField(g, "\t\t", "b", f, "v", true)
				g.P("\t}")
			}
		case isOptionalPtr(f):
			g.P("\tif m.", f.GoName, " != nil {")
			emitMarshalField(g, "\t\t", "b", f, "*m."+f.GoName, true)
			g.P("\t}")
		default:
			emitMarshalField(g, "\t", "b", f, "m."+f.GoName, false)
		}
	}
	g.P("\treturn b")
	g.P("}")
	g.P()
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

func genUnmarshal(g *protogen.GeneratedFile, msg *protogen.Message) {
	name := msg.GoIdent.GoName
	readTag := g.QualifiedGoIdent(litewirePkg.Ident("ReadTag"))
	skip := g.QualifiedGoIdent(litewirePkg.Ident("Skip"))

	g.P("func (m *", name, ") Unmarshal(b []byte) error {")
	g.P("\tfor len(b) > 0 {")
	g.P("\t\tnum, wtyp, err := ", readTag, "(&b)")
	g.P("\t\tif err != nil { return err }")
	if len(msg.Fields) > 0 {
		g.P("\t\tswitch num {")
		for _, f := range msg.Fields {
			g.P("\t\tcase ", f.Desc.Number(), ":")
			emitUnmarshalField(g, f)
		}
		g.P("\t\t}")
	}
	g.P("\t\tif err := ", skip, "(&b, num, wtyp); err != nil { return err }")
	g.P("\t}")
	g.P("\treturn nil")
	g.P("}")
	g.P()
}

func emitUnmarshalField(g *protogen.GeneratedFile, f *protogen.Field) {
	bytesType := g.QualifiedGoIdent(litewirePkg.Ident("BytesType"))
	readBytesRaw := g.QualifiedGoIdent(litewirePkg.Ident("ReadBytesRaw"))
	readMessage := g.QualifiedGoIdent(litewirePkg.Ident("ReadMessage"))
	readTag := g.QualifiedGoIdent(litewirePkg.Ident("ReadTag"))
	skip := g.QualifiedGoIdent(litewirePkg.Ident("Skip"))

	if f.Desc.IsMap() {
		kf := f.Message.Fields[0]
		vf := f.Message.Fields[1]
		g.P("\t\t\tif wtyp == ", bytesType, " {")
		g.P("\t\t\t\traw, err := ", readBytesRaw, "(&b)")
		g.P("\t\t\t\tif err != nil { return err }")
		g.P("\t\t\t\tvar mk ", scalarOrMsgGoType(g, kf))
		g.P("\t\t\t\tvar mv ", scalarOrMsgGoType(g, vf))
		if vf.Desc.Kind() == protoreflect.MessageKind {
			g.P("\t\t\t\tmv = &", g.QualifiedGoIdent(vf.Message.GoIdent), "{}")
		}
		g.P("\t\t\t\tfor len(raw) > 0 {")
		g.P("\t\t\t\t\tmnum, mwtyp, err := ", readTag, "(&raw)")
		g.P("\t\t\t\t\tif err != nil { return err }")
		g.P("\t\t\t\t\tswitch mnum {")
		g.P("\t\t\t\t\tcase 1:")
		emitUnmarshalScalarOrBytes(g, "\t\t\t\t\t\t", "raw", "mwtyp", kf, func(val string) {
			g.P("\t\t\t\t\t\tmk = ", val)
			g.P("\t\t\t\t\t\tcontinue")
		})
		g.P("\t\t\t\t\tcase 2:")
		if vf.Desc.Kind() == protoreflect.MessageKind {
			g.P("\t\t\t\t\t\tif mwtyp == ", bytesType, " {")
			g.P("\t\t\t\t\t\t\tif err := ", readMessage, "(&raw, mv); err != nil { return err }")
			g.P("\t\t\t\t\t\t\tcontinue")
			g.P("\t\t\t\t\t\t}")
		} else {
			emitUnmarshalScalarOrBytes(g, "\t\t\t\t\t\t", "raw", "mwtyp", vf, func(val string) {
				g.P("\t\t\t\t\t\tmv = ", val)
				g.P("\t\t\t\t\t\tcontinue")
			})
		}
		g.P("\t\t\t\t\t}")
		g.P("\t\t\t\t\tif err := ", skip, "(&raw, mnum, mwtyp); err != nil { return err }")
		g.P("\t\t\t\t}")
		g.P("\t\t\t\tif m.", f.GoName, " == nil { m.", f.GoName, " = make(", fieldGoType(g, f), ") }")
		g.P("\t\t\t\tm.", f.GoName, "[mk] = mv")
		g.P("\t\t\t\tcontinue")
		g.P("\t\t\t}")
		return
	}

	if f.Desc.IsList() {
		if isPackable(f) {
			g.P("\t\t\tif wtyp == ", bytesType, " {")
			g.P("\t\t\t\traw, err := ", readBytesRaw, "(&b)")
			g.P("\t\t\t\tif err != nil { return err }")
			g.P("\t\t\t\tfor len(raw) > 0 {")
			emitUnmarshalRawScalarInto(g, "\t\t\t\t\t", "raw", f, func(val string) {
				g.P("\t\t\t\t\tm.", f.GoName, " = append(m.", f.GoName, ", ", val, ")")
			})
			g.P("\t\t\t\t}")
			g.P("\t\t\t\tcontinue")
			g.P("\t\t\t}")
		}
		if f.Desc.Kind() == protoreflect.MessageKind {
			g.P("\t\t\tif wtyp == ", bytesType, " {")
			g.P("\t\t\t\telem := &", g.QualifiedGoIdent(f.Message.GoIdent), "{}")
			g.P("\t\t\t\tif err := ", readMessage, "(&b, elem); err != nil { return err }")
			g.P("\t\t\t\tm.", f.GoName, " = append(m.", f.GoName, ", elem)")
			g.P("\t\t\t\tcontinue")
			g.P("\t\t\t}")
			return
		}
		emitUnmarshalScalarOrBytes(g, "\t\t\t", "b", "wtyp", f, func(val string) {
			g.P("\t\t\t\tm.", f.GoName, " = append(m.", f.GoName, ", ", val, ")")
			g.P("\t\t\t\tcontinue")
		})
		return
	}

	if f.Desc.Kind() == protoreflect.MessageKind {
		g.P("\t\t\tif wtyp == ", bytesType, " {")
		if isRealOneof(f) {
			wrap := f.GoIdent.GoName
			g.P("\t\t\t\tvar sub *", g.QualifiedGoIdent(f.Message.GoIdent))
			g.P("\t\t\t\tif cur, ok := m.", f.Oneof.GoName, ".(*", wrap, "); ok && cur != nil { sub = cur.", f.GoName, " }")
			g.P("\t\t\t\tif sub == nil { sub = &", g.QualifiedGoIdent(f.Message.GoIdent), "{} }")
			g.P("\t\t\t\tif err := ", readMessage, "(&b, sub); err != nil { return err }")
			g.P("\t\t\t\tm.", f.Oneof.GoName, " = &", wrap, "{", f.GoName, ": sub}")
		} else {
			g.P("\t\t\t\tif m.", f.GoName, " == nil { m.", f.GoName, " = &", g.QualifiedGoIdent(f.Message.GoIdent), "{} }")
			g.P("\t\t\t\tif err := ", readMessage, "(&b, m.", f.GoName, "); err != nil { return err }")
		}
		g.P("\t\t\t\tcontinue")
		g.P("\t\t\t}")
		return
	}

	emitUnmarshalScalarOrBytes(g, "\t\t\t", "b", "wtyp", f, func(val string) {
		if isRealOneof(f) {
			wrap := f.GoIdent.GoName
			g.P("\t\t\t\tm.", f.Oneof.GoName, " = &", wrap, "{", f.GoName, ": ", val, "}")
		} else if isOptionalPtr(f) {
			g.P("\t\t\t\tpv := ", val)
			g.P("\t\t\t\tm.", f.GoName, " = &pv")
		} else {
			g.P("\t\t\t\tm.", f.GoName, " = ", val)
		}
		g.P("\t\t\t\tcontinue")
	})
}

func emitUnmarshalScalarOrBytes(g *protogen.GeneratedFile, indent, buf, wtypVar string, f *protogen.Field, assign func(string)) {
	wtyp := wireTypeExpr(g, f.Desc.Kind())
	g.P(indent, "if ", wtypVar, " == ", wtyp, " {")
	emitUnmarshalRawScalarInto(g, indent+"\t", buf, f, assign)
	g.P(indent, "}")
}

func emitUnmarshalRawScalarInto(g *protogen.GeneratedFile, indent, buf string, f *protogen.Field, assign func(string)) {
	switch f.Desc.Kind() {
	case protoreflect.StringKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadString"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("v")
	case protoreflect.BytesKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadBytes"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("v")
	case protoreflect.BoolKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		decodeBool := g.QualifiedGoIdent(litewirePkg.Ident("DecodeBool"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign(decodeBool + "(v)")
	case protoreflect.EnumKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign(g.QualifiedGoIdent(f.Enum.GoIdent) + "(int32(v))")
	case protoreflect.Int32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("int32(v)")
	case protoreflect.Uint32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("uint32(v)")
	case protoreflect.Int64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("int64(v)")
	case protoreflect.Uint64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("v")
	case protoreflect.Sint32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		decodeZigZag := g.QualifiedGoIdent(litewirePkg.Ident("DecodeZigZag"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("int32(" + decodeZigZag + "(v))")
	case protoreflect.Sint64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadVarint"))
		decodeZigZag := g.QualifiedGoIdent(litewirePkg.Ident("DecodeZigZag"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign(decodeZigZag + "(v)")
	case protoreflect.Fixed32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed32"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("v")
	case protoreflect.Sfixed32Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed32"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("int32(v)")
	case protoreflect.FloatKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed32"))
		float32frombits := g.QualifiedGoIdent(mathPkg.Ident("Float32frombits"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign(float32frombits + "(v)")
	case protoreflect.Fixed64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed64"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("v")
	case protoreflect.Sfixed64Kind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed64"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign("int64(v)")
	case protoreflect.DoubleKind:
		fn := g.QualifiedGoIdent(litewirePkg.Ident("ReadFixed64"))
		float64frombits := g.QualifiedGoIdent(mathPkg.Ident("Float64frombits"))
		g.P(indent, "v, err := ", fn, "(&", buf, ")")
		g.P(indent, "if err != nil { return err }")
		assign(float64frombits + "(v)")
	default:
		panic(fmt.Sprintf("unsupported raw scalar kind: %v", f.Desc.Kind()))
	}
}
