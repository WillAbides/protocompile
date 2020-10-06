package parser

import (
	"bytes"
	"github.com/jhump/protocompile/internal"
	"github.com/jhump/protocompile/reporter"
	"math"
	"strings"
	"unicode"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/jhump/protocompile/ast"
)

type Result interface {
	AST() *ast.FileNode
	Proto() *descriptorpb.FileDescriptorProto
}

type result struct {
	file  *ast.FileNode
	proto *descriptorpb.FileDescriptorProto

	nodes map[proto.Message]ast.Node
}

func ResultWithoutAST(proto *descriptorpb.FileDescriptorProto) Result {
	//TODO
	return &result{proto: proto}
}

func ToFileDescriptorProto(filename string, file *ast.FileNode, validate bool, handler *reporter.Handler) (Result, error) {
	r := &result{file: file}
	r.createFileDescriptor(filename, file, handler)
	if validate {
		// TODO
	}
	return r, nil
}

func (r *result) AST() *ast.FileNode {
	return r.file
}

func (r *result) Proto() *descriptorpb.FileDescriptorProto {
	return r.proto
}

func (r *result) createFileDescriptor(filename string, file *ast.FileNode, handler *reporter.Handler) {
	fd := &descriptorpb.FileDescriptorProto{Name: proto.String(filename)}
	r.proto = fd

	r.putFileNode(fd, file)

	isProto3 := false
	if file.Syntax != nil {
		if file.Syntax.Syntax.AsString() == "proto3" {
			isProto3 = true
		} else if file.Syntax.Syntax.AsString() != "proto2" {
			if handler.handleErrorWithPos(file.Syntax.Syntax.Start(), `syntax value must be "proto2" or "proto3"`) != nil {
				return
			}
		}

		// proto2 is the default, so no need to set unless proto3
		if isProto3 {
			fd.Syntax = proto.String(file.Syntax.Syntax.AsString())
		}
	} else {
		handler.warn(file.Start(), ErrNoSyntax)
	}

	for _, decl := range file.Decls {
		if r.errs.err != nil {
			return
		}
		switch decl := decl.(type) {
		case *ast.EnumNode:
			fd.EnumType = append(fd.EnumType, r.asEnumDescriptor(decl))
		case *ast.ExtendNode:
			r.addExtensions(decl, &fd.Extension, &fd.MessageType, isProto3)
		case *ast.ImportNode:
			index := len(fd.Dependency)
			fd.Dependency = append(fd.Dependency, decl.Name.AsString())
			if decl.Public != nil {
				fd.PublicDependency = append(fd.PublicDependency, int32(index))
			} else if decl.Weak != nil {
				fd.WeakDependency = append(fd.WeakDependency, int32(index))
			}
		case *ast.MessageNode:
			fd.MessageType = append(fd.MessageType, r.asMessageDescriptor(decl, isProto3))
		case *ast.OptionNode:
			if fd.Options == nil {
				fd.Options = &descriptorpb.FileOptions{}
			}
			fd.Options.UninterpretedOption = append(fd.Options.UninterpretedOption, r.asUninterpretedOption(decl))
		case *ast.ServiceNode:
			fd.Service = append(fd.Service, r.asServiceDescriptor(decl))
		case *ast.PackageNode:
			if fd.Package != nil {
				if r.errs.handleErrorWithPos(decl.Start(), "files should have only one package declaration") != nil {
					return
				}
			}
			fd.Package = proto.String(string(decl.Name.AsIdentifier()))
		}
	}
}

func (r *result) asUninterpretedOptions(nodes []*ast.OptionNode) []*descriptorpb.UninterpretedOption {
	if len(nodes) == 0 {
		return nil
	}
	opts := make([]*descriptorpb.UninterpretedOption, len(nodes))
	for i, n := range nodes {
		opts[i] = r.asUninterpretedOption(n)
	}
	return opts
}

func (r *result) asUninterpretedOption(node *ast.OptionNode) *descriptorpb.UninterpretedOption {
	opt := &descriptorpb.UninterpretedOption{Name: r.asUninterpretedOptionName(node.Name.Parts)}
	r.putOptionNode(opt, node)

	switch val := node.Val.Value().(type) {
	case bool:
		if val {
			opt.IdentifierValue = proto.String("true")
		} else {
			opt.IdentifierValue = proto.String("false")
		}
	case int64:
		opt.NegativeIntValue = proto.Int64(val)
	case uint64:
		opt.PositiveIntValue = proto.Uint64(val)
	case float64:
		opt.DoubleValue = proto.Float64(val)
	case string:
		opt.StringValue = []byte(val)
	case ast.Identifier:
		opt.IdentifierValue = proto.String(string(val))
	case []*ast.MessageFieldNode:
		var buf bytes.Buffer
		aggToString(val, &buf)
		aggStr := buf.String()
		opt.AggregateValue = proto.String(aggStr)
		//the grammar does not allow arrays here, so no case for []ast.ValueNode
	}
	return opt
}

func (r *result) asUninterpretedOptionName(parts []*ast.FieldReferenceNode) []*descriptorpb.UninterpretedOption_NamePart {
	ret := make([]*descriptorpb.UninterpretedOption_NamePart, len(parts))
	for i, part := range parts {
		np := &descriptorpb.UninterpretedOption_NamePart{
			NamePart:    proto.String(string(part.Name.AsIdentifier())),
			IsExtension: proto.Bool(part.IsExtension()),
		}
		r.putOptionNamePartNode(np, part)
		ret[i] = np
	}
	return ret
}

func (r *result) addExtensions(ext *ast.ExtendNode, flds *[]*descriptorpb.FieldDescriptorProto, msgs *[]*descriptorpb.DescriptorProto, isProto3 bool) {
	extendee := string(ext.Extendee.AsIdentifier())
	count := 0
	for _, decl := range ext.Decls {
		switch decl := decl.(type) {
		case *ast.FieldNode:
			count++
			// use higher limit since we don't know yet whether extendee is messageset wire format
			fd := r.asFieldDescriptor(decl, internal.MaxTag, isProto3)
			fd.Extendee = proto.String(extendee)
			*flds = append(*flds, fd)
		case *ast.GroupNode:
			count++
			// ditto: use higher limit right now
			fd, md := r.asGroupDescriptors(decl, isProto3, internal.MaxTag)
			fd.Extendee = proto.String(extendee)
			*flds = append(*flds, fd)
			*msgs = append(*msgs, md)
		}
	}
	if count == 0 {
		_ = r.errs.handleErrorWithPos(ext.Start(), "extend sections must define at least one extension")
	}
}

func asLabel(lbl *ast.FieldLabel) *descriptorpb.FieldDescriptorProto_Label {
	if !lbl.IsPresent() {
		return nil
	}
	switch {
	case lbl.Repeated:
		return descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	case lbl.Required:
		return descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum()
	default:
		return descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	}
}

func (r *result) asFieldDescriptor(node *ast.FieldNode, maxTag int32, isProto3 bool) *descriptorpb.FieldDescriptorProto {
	tag := node.Tag.Val
	if err := checkTag(node.Tag.Start(), tag, maxTag); err != nil {
		_ = r.errs.handleError(err)
	}
	fd := newFieldDescriptor(node.Name.Val, string(node.FldType.AsIdentifier()), int32(tag), asLabel(&node.Label))
	r.putFieldNode(fd, node)
	if opts := node.Options.GetElements(); len(opts) > 0 {
		fd.Options = &descriptorpb.FieldOptions{UninterpretedOption: r.asUninterpretedOptions(opts)}
	}
	if isProto3 && fd.Label != nil && fd.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL {
		fd.Proto3Optional = proto.Bool(true)
	}
	return fd
}

var fieldTypes = map[string]descriptorpb.FieldDescriptorProto_Type{
	"double":   descriptorpb.FieldDescriptorProto_TYPE_DOUBLE,
	"float":    descriptorpb.FieldDescriptorProto_TYPE_FLOAT,
	"int32":    descriptorpb.FieldDescriptorProto_TYPE_INT32,
	"int64":    descriptorpb.FieldDescriptorProto_TYPE_INT64,
	"uint32":   descriptorpb.FieldDescriptorProto_TYPE_UINT32,
	"uint64":   descriptorpb.FieldDescriptorProto_TYPE_UINT64,
	"sint32":   descriptorpb.FieldDescriptorProto_TYPE_SINT32,
	"sint64":   descriptorpb.FieldDescriptorProto_TYPE_SINT64,
	"fixed32":  descriptorpb.FieldDescriptorProto_TYPE_FIXED32,
	"fixed64":  descriptorpb.FieldDescriptorProto_TYPE_FIXED64,
	"sfixed32": descriptorpb.FieldDescriptorProto_TYPE_SFIXED32,
	"sfixed64": descriptorpb.FieldDescriptorProto_TYPE_SFIXED64,
	"bool":     descriptorpb.FieldDescriptorProto_TYPE_BOOL,
	"string":   descriptorpb.FieldDescriptorProto_TYPE_STRING,
	"bytes":    descriptorpb.FieldDescriptorProto_TYPE_BYTES,
}

func newFieldDescriptor(name string, fieldType string, tag int32, lbl *descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
	fd := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		JsonName: proto.String(internal.JsonName(name)),
		Number:   proto.Int32(tag),
		Label:    lbl,
	}
	t, ok := fieldTypes[fieldType]
	if ok {
		fd.Type = t.Enum()
	} else {
		// NB: we don't have enough info to determine whether this is an enum
		// or a message type, so we'll leave Type nil and set it later
		// (during linking)
		fd.TypeName = proto.String(fieldType)
	}
	return fd
}

func (r *result) asGroupDescriptors(group *ast.GroupNode, isProto3 bool, maxTag int32) (*descriptorpb.FieldDescriptorProto, *descriptorpb.DescriptorProto) {
	tag := group.Tag.Val
	if err := checkTag(group.Tag.Start(), tag, maxTag); err != nil {
		_ = r.errs.handleError(err)
	}
	if !unicode.IsUpper(rune(group.Name.Val[0])) {
		_ = r.errs.handleErrorWithPos(group.Name.Start(), "group %s should have a name that starts with a capital letter", group.Name.Val)
	}
	fieldName := strings.ToLower(group.Name.Val)
	fd := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(fieldName),
		JsonName: proto.String(internal.JsonName(fieldName)),
		Number:   proto.Int32(int32(tag)),
		Label:    asLabel(&group.Label),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_GROUP.Enum(),
		TypeName: proto.String(group.Name.Val),
	}
	r.putFieldNode(fd, group)
	if opts := group.Options.GetElements(); len(opts) > 0 {
		fd.Options = &descriptorpb.FieldOptions{UninterpretedOption: r.asUninterpretedOptions(opts)}
	}
	md := &descriptorpb.DescriptorProto{Name: proto.String(group.Name.Val)}
	r.putMessageNode(md, group)
	r.addMessageBody(md, &group.MessageBody, isProto3)
	return fd, md
}

func (r *result) asMapDescriptors(mapField *ast.MapFieldNode, isProto3 bool, maxTag int32) (*descriptorpb.FieldDescriptorProto, *descriptorpb.DescriptorProto) {
	tag := mapField.Tag.Val
	if err := checkTag(mapField.Tag.Start(), tag, maxTag); err != nil {
		_ = r.errs.handleError(err)
	}
	var lbl *descriptorpb.FieldDescriptorProto_Label
	if !isProto3 {
		lbl = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	}
	keyFd := newFieldDescriptor("key", mapField.MapType.KeyType.Val, 1, lbl)
	r.putFieldNode(keyFd, mapField.KeyField())
	valFd := newFieldDescriptor("value", string(mapField.MapType.ValueType.AsIdentifier()), 2, lbl)
	r.putFieldNode(valFd, mapField.ValueField())
	entryName := internal.InitCap(internal.JsonName(mapField.Name.Val)) + "Entry"
	fd := newFieldDescriptor(mapField.Name.Val, entryName, int32(tag), descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum())
	if opts := mapField.Options.GetElements(); len(opts) > 0 {
		fd.Options = &descriptorpb.FieldOptions{UninterpretedOption: r.asUninterpretedOptions(opts)}
	}
	r.putFieldNode(fd, mapField)
	md := &descriptorpb.DescriptorProto{
		Name:    proto.String(entryName),
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
		Field:   []*descriptorpb.FieldDescriptorProto{keyFd, valFd},
	}
	r.putMessageNode(md, mapField)
	return fd, md
}

func (r *result) asExtensionRanges(node *ast.ExtensionRangeNode, maxTag int32) []*descriptorpb.DescriptorProto_ExtensionRange {
	opts := r.asUninterpretedOptions(node.Options.GetElements())
	ers := make([]*descriptorpb.DescriptorProto_ExtensionRange, len(node.Ranges))
	for i, rng := range node.Ranges {
		start, end := getRangeBounds(r, rng, 0, maxTag)
		er := &descriptorpb.DescriptorProto_ExtensionRange{
			Start: proto.Int32(start),
			End:   proto.Int32(end + 1),
		}
		if len(opts) > 0 {
			er.Options = &descriptorpb.ExtensionRangeOptions{UninterpretedOption: opts}
		}
		r.putExtensionRangeNode(er, rng)
		ers[i] = er
	}
	return ers
}

func (r *result) asEnumValue(ev *ast.EnumValueNode) *descriptorpb.EnumValueDescriptorProto {
	num, ok := ast.AsInt32(ev.Number, math.MinInt32, math.MaxInt32)
	if !ok {
		_ = r.errs.handleErrorWithPos(ev.Number.Start(), "value %d is out of range: should be between %d and %d", ev.Number.Value(), math.MinInt32, math.MaxInt32)
	}
	evd := &descriptorpb.EnumValueDescriptorProto{Name: proto.String(ev.Name.Val), Number: proto.Int32(num)}
	r.putEnumValueNode(evd, ev)
	if opts := ev.Options.GetElements(); len(opts) > 0 {
		evd.Options = &descriptorpb.EnumValueOptions{UninterpretedOption: r.asUninterpretedOptions(opts)}
	}
	return evd
}

func (r *result) asMethodDescriptor(node *ast.RPCNode) *descriptorpb.MethodDescriptorProto {
	md := &descriptorpb.MethodDescriptorProto{
		Name:       proto.String(node.Name.Val),
		InputType:  proto.String(string(node.Input.MessageType.AsIdentifier())),
		OutputType: proto.String(string(node.Output.MessageType.AsIdentifier())),
	}
	r.putMethodNode(md, node)
	if node.Input.Stream != nil {
		md.ClientStreaming = proto.Bool(true)
	}
	if node.Output.Stream != nil {
		md.ServerStreaming = proto.Bool(true)
	}
	// protoc always adds a MethodOptions if there are brackets
	// We do the same to match protoc as closely as possible
	// https://github.com/protocolbuffers/protobuf/blob/0c3f43a6190b77f1f68b7425d1b7e1a8257a8d0c/src/google/protobuf/compiler/parser.cc#L2152
	if node.OpenBrace != nil {
		md.Options = &descriptorpb.MethodOptions{}
		for _, decl := range node.Decls {
			switch decl := decl.(type) {
			case *ast.OptionNode:
				md.Options.UninterpretedOption = append(md.Options.UninterpretedOption, r.asUninterpretedOption(decl))
			}
		}
	}
	return md
}

func (r *result) asEnumDescriptor(en *ast.EnumNode) *descriptorpb.EnumDescriptorProto {
	ed := &descriptorpb.EnumDescriptorProto{Name: proto.String(en.Name.Val)}
	r.putEnumNode(ed, en)
	for _, decl := range en.Decls {
		switch decl := decl.(type) {
		case *ast.OptionNode:
			if ed.Options == nil {
				ed.Options = &descriptorpb.EnumOptions{}
			}
			ed.Options.UninterpretedOption = append(ed.Options.UninterpretedOption, r.asUninterpretedOption(decl))
		case *ast.EnumValueNode:
			ed.Value = append(ed.Value, r.asEnumValue(decl))
		case *ast.ReservedNode:
			for _, n := range decl.Names {
				ed.ReservedName = append(ed.ReservedName, n.AsString())
			}
			for _, rng := range decl.Ranges {
				ed.ReservedRange = append(ed.ReservedRange, r.asEnumReservedRange(rng))
			}
		}
	}
	return ed
}

func (r *result) asEnumReservedRange(rng *ast.RangeNode) *descriptorpb.EnumDescriptorProto_EnumReservedRange {
	start, end := getRangeBounds(r, rng, math.MinInt32, math.MaxInt32)
	rr := &descriptorpb.EnumDescriptorProto_EnumReservedRange{
		Start: proto.Int32(start),
		End:   proto.Int32(end),
	}
	r.putEnumReservedRangeNode(rr, rng)
	return rr
}

func (r *result) asMessageDescriptor(node *ast.MessageNode, isProto3 bool) *descriptorpb.DescriptorProto {
	msgd := &descriptorpb.DescriptorProto{Name: proto.String(node.Name.Val)}
	r.putMessageNode(msgd, node)
	r.addMessageBody(msgd, &node.MessageBody, isProto3)
	return msgd
}

func (r *result) addMessageBody(msgd *descriptorpb.DescriptorProto, body *ast.MessageBody, isProto3 bool) {
	// first process any options
	for _, decl := range body.Decls {
		if opt, ok := decl.(*ast.OptionNode); ok {
			if msgd.Options == nil {
				msgd.Options = &descriptorpb.MessageOptions{}
			}
			msgd.Options.UninterpretedOption = append(msgd.Options.UninterpretedOption, r.asUninterpretedOption(opt))
		}
	}

	// now that we have options, we can see if this uses messageset wire format, which
	// impacts how we validate tag numbers in any fields in the message
	maxTag := int32(internal.MaxNormalTag)
	if isMessageSet, err := isMessageSetWireFormat(r, "message "+msgd.GetName(), msgd); err != nil {
		return
	} else if isMessageSet {
		maxTag = internal.MaxTag // higher limit for messageset wire format
	}

	rsvdNames := map[string]int{}

	// now we can process the rest
	for _, decl := range body.Decls {
		switch decl := decl.(type) {
		case *ast.EnumNode:
			msgd.EnumType = append(msgd.EnumType, r.asEnumDescriptor(decl))
		case *ast.ExtendNode:
			r.addExtensions(decl, &msgd.Extension, &msgd.NestedType, isProto3)
		case *ast.ExtensionRangeNode:
			msgd.ExtensionRange = append(msgd.ExtensionRange, r.asExtensionRanges(decl, maxTag)...)
		case *ast.FieldNode:
			fd := r.asFieldDescriptor(decl, maxTag, isProto3)
			msgd.Field = append(msgd.Field, fd)
		case *ast.MapFieldNode:
			fd, md := r.asMapDescriptors(decl, isProto3, maxTag)
			msgd.Field = append(msgd.Field, fd)
			msgd.NestedType = append(msgd.NestedType, md)
		case *ast.GroupNode:
			fd, md := r.asGroupDescriptors(decl, isProto3, maxTag)
			msgd.Field = append(msgd.Field, fd)
			msgd.NestedType = append(msgd.NestedType, md)
		case *ast.OneOfNode:
			oodIndex := len(msgd.OneofDecl)
			ood := &descriptorpb.OneofDescriptorProto{Name: proto.String(decl.Name.Val)}
			r.putOneOfNode(ood, decl)
			msgd.OneofDecl = append(msgd.OneofDecl, ood)
			ooFields := 0
			for _, oodecl := range decl.Decls {
				switch oodecl := oodecl.(type) {
				case *ast.OptionNode:
					if ood.Options == nil {
						ood.Options = &descriptorpb.OneofOptions{}
					}
					ood.Options.UninterpretedOption = append(ood.Options.UninterpretedOption, r.asUninterpretedOption(oodecl))
				case *ast.FieldNode:
					fd := r.asFieldDescriptor(oodecl, maxTag, isProto3)
					fd.OneofIndex = proto.Int32(int32(oodIndex))
					msgd.Field = append(msgd.Field, fd)
					ooFields++
				case *ast.GroupNode:
					fd, md := r.asGroupDescriptors(oodecl, isProto3, maxTag)
					fd.OneofIndex = proto.Int32(int32(oodIndex))
					msgd.Field = append(msgd.Field, fd)
					msgd.NestedType = append(msgd.NestedType, md)
					ooFields++
				}
			}
			if ooFields == 0 {
				_ = r.errs.handleErrorWithPos(decl.Start(), "oneof must contain at least one field")
			}
		case *ast.MessageNode:
			msgd.NestedType = append(msgd.NestedType, r.asMessageDescriptor(decl, isProto3))
		case *ast.ReservedNode:
			for _, n := range decl.Names {
				count := rsvdNames[n.AsString()]
				if count == 1 { // already seen
					_ = r.errs.handleErrorWithPos(n.Start(), "name %q is reserved multiple times", n.AsString())
				}
				rsvdNames[n.AsString()] = count + 1
				msgd.ReservedName = append(msgd.ReservedName, n.AsString())
			}
			for _, rng := range decl.Ranges {
				msgd.ReservedRange = append(msgd.ReservedRange, r.asMessageReservedRange(rng, maxTag))
			}
		}
	}

	// process any proto3_optional fields
	if isProto3 {
		internal.ProcessProto3OptionalFields(msgd)
	}
}

func isMessageSetWireFormat(res *result, scope string, md *descriptorpb.DescriptorProto) (bool, error) {
	uo := md.GetOptions().GetUninterpretedOption()
	index, err := findOption(res, scope, uo, "message_set_wire_format")
	if err != nil {
		return false, err
	}
	if index == -1 {
		// no such option, so default to false
		return false, nil
	}

	opt := uo[index]
	optNode := res.getOptionNode(opt)

	switch opt.GetIdentifierValue() {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, res.errs.handleErrorWithPos(optNode.GetValue().Start(), "%s: expecting bool value for message_set_wire_format option", scope)
	}
}

func (r *result) asMessageReservedRange(rng *ast.RangeNode, maxTag int32) *descriptorpb.DescriptorProto_ReservedRange {
	start, end := getRangeBounds(r, rng, 0, maxTag)
	rr := &descriptorpb.DescriptorProto_ReservedRange{
		Start: proto.Int32(start),
		End:   proto.Int32(end + 1),
	}
	r.putMessageReservedRangeNode(rr, rng)
	return rr
}

func getRangeBounds(res *result, rng *ast.RangeNode, minVal, maxVal int32) (int32, int32) {
	checkOrder := true
	start, ok := rng.StartValueAsInt32(minVal, maxVal)
	if !ok {
		checkOrder = false
		_ = res.errs.handleErrorWithPos(rng.StartVal.Start(), "range start %d is out of range: should be between %d and %d", rng.StartValue(), minVal, maxVal)
	}

	end, ok := rng.EndValueAsInt32(minVal, maxVal)
	if !ok {
		checkOrder = false
		if rng.EndVal != nil {
			_ = res.errs.handleErrorWithPos(rng.EndVal.Start(), "range end %d is out of range: should be between %d and %d", rng.EndValue(), minVal, maxVal)
		}
	}

	if checkOrder && start > end {
		_ = res.errs.handleErrorWithPos(rng.RangeStart().Start(), "range, %d to %d, is invalid: start must be <= end", start, end)
	}

	return start, end
}

func (r *result) asServiceDescriptor(svc *ast.ServiceNode) *descriptorpb.ServiceDescriptorProto {
	sd := &descriptorpb.ServiceDescriptorProto{Name: proto.String(svc.Name.Val)}
	r.putServiceNode(sd, svc)
	for _, decl := range svc.Decls {
		switch decl := decl.(type) {
		case *ast.OptionNode:
			if sd.Options == nil {
				sd.Options = &descriptorpb.ServiceOptions{}
			}
			sd.Options.UninterpretedOption = append(sd.Options.UninterpretedOption, r.asUninterpretedOption(decl))
		case *ast.RPCNode:
			sd.Method = append(sd.Method, r.asMethodDescriptor(decl))
		}
	}
	return sd
}
