// Command recoverproto extracts FileDescriptorProto records from a Go binary.
//
// protodump assumes that an invalid wire value after a descriptor is a fatal
// error. Large binaries can place unrelated read-only data immediately after a
// valid descriptor, so this fallback retains the longest valid descriptor
// prefix instead. It never infers fields: every emitted definition comes from
// descriptor bytes embedded in the input binary.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/arkadiyt/protodump/pkg/protodump"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

type recovered struct {
	descriptor *descriptorpb.FileDescriptorProto
	definition *protodump.ProtoDefinition
	rawLength  int
}

var protoPathPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./+\-]*\.proto$`)

func main() {
	input := flag.String("file", "", "binary containing embedded Go protobuf descriptors")
	output := flag.String("output", "proto", "directory for recovered .proto files")
	prefix := flag.String("prefix", "", "only emit proto paths with this prefix")
	descriptorSet := flag.String("descriptor-set", "", "optional path for recovered binary FileDescriptorSet")
	flag.Parse()

	if *input == "" {
		flag.Usage()
		os.Exit(2)
	}

	data, err := os.ReadFile(*input)
	if err != nil {
		fatalf("read %s: %v", *input, err)
	}

	found := make(map[string]recovered)
	for start := 0; start < len(data); start++ {
		if data[start] != 0x0a { // FileDescriptorProto.name, field 1, bytes.
			continue
		}

		nameBytes, n := protowire.ConsumeBytes(data[start+1:])
		if n < 0 || !bytes.HasSuffix(nameBytes, []byte(".proto")) {
			continue
		}
		name := string(nameBytes)
		if len(name) > 512 || !protoPathPattern.MatchString(name) || strings.Contains(name, "..") {
			continue
		}
		if *prefix != "" && !strings.HasPrefix(name, *prefix) {
			continue
		}

		candidate, ok := recoverAt(data, start, name)
		if !ok {
			continue
		}
		previous, exists := found[name]
		if !exists || candidate.rawLength > previous.rawLength {
			found[name] = candidate
		}
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(*output, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fatalf("create directory for %s: %v", name, err)
		}
		content := []byte(found[name].definition.String())
		if err := os.WriteFile(path, content, 0o644); err != nil {
			fatalf("write %s: %v", path, err)
		}
		fmt.Printf("recovered %s (%d descriptor bytes)\n", name, found[name].rawLength)
	}
	if *descriptorSet != "" {
		set := &descriptorpb.FileDescriptorSet{File: make([]*descriptorpb.FileDescriptorProto, 0, len(names))}
		for _, name := range names {
			set.File = append(set.File, found[name].descriptor)
		}
		payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(set)
		if err != nil {
			fatalf("marshal descriptor set: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(*descriptorSet), 0o755); err != nil {
			fatalf("create descriptor-set directory: %v", err)
		}
		if err := os.WriteFile(*descriptorSet, payload, 0o644); err != nil {
			fatalf("write descriptor set: %v", err)
		}
		fmt.Printf("wrote descriptor set %s (%d files)\n", *descriptorSet, len(names))
	}

	if len(names) == 0 {
		fatalf("no descriptors recovered")
	}
}

func recoverAt(data []byte, start int, expectedName string) (recovered, bool) {
	allowedWire := map[protowire.Number]map[protowire.Type]bool{
		1:  {protowire.BytesType: true},
		2:  {protowire.BytesType: true},
		3:  {protowire.BytesType: true},
		4:  {protowire.BytesType: true},
		5:  {protowire.BytesType: true},
		6:  {protowire.BytesType: true},
		7:  {protowire.BytesType: true},
		8:  {protowire.BytesType: true},
		9:  {protowire.BytesType: true},
		10: {protowire.VarintType: true, protowire.BytesType: true},
		11: {protowire.VarintType: true, protowire.BytesType: true},
		12: {protowire.BytesType: true},
		14: {protowire.VarintType: true},
		15: {protowire.BytesType: true},
	}

	position := start
	seenName := false
	best := recovered{}
	for position < len(data) {
		number, wireType, length := protowire.ConsumeField(data[position:])
		if length <= 0 || !allowedWire[number][wireType] {
			break
		}
		if number == 1 {
			if seenName {
				break // Adjacent FileDescriptorProto.
			}
			seenName = true
		}
		position += length

		var descriptor descriptorpb.FileDescriptorProto
		if err := proto.Unmarshal(data[start:position], &descriptor); err != nil || descriptor.GetName() != expectedName {
			continue
		}
		definition, err := protodump.NewFromDescriptor(&descriptor)
		if err != nil {
			continue
		}
		best = recovered{
			descriptor: proto.Clone(&descriptor).(*descriptorpb.FileDescriptorProto),
			definition: definition,
			rawLength:  position - start,
		}
	}
	return best, best.definition != nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "recoverproto: "+format+"\n", args...)
	os.Exit(1)
}
