package docs

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// A generated descriptor carries no comments. protoc-gen-go writes the prose into the Go
// source as doc comments and protoc's source info is a build artefact that has to be asked for
// separately — so a subsystem that generates its descriptor set without `--include_source_info`
// has a contract with no documentation at all, and nothing fails: the help it generates simply
// says less, one field and one annotation at a time.
//
// The documentation is therefore read from the original `.proto`, which every subsystem already
// has and which is the thing the comments were written in. A subsystem embeds its `.proto` and
// registers it; documentation is then compiled from that text with source info, and the
// generated descriptor supplies only the structure.
//
// This also fixes the annotations, which are read from the same comments: an operation whose
// `@toolbox.side-effects` line is lost does not merely lose a description, it becomes
// unclassified, which is the one state a policy cannot grant by naming read or write.

// protoSources holds the original .proto text, by the path protoc would give it.
//
// The key is the descriptor's own path — "proto/logger.proto" for a subsystem, because that is
// what --proto_path=. produces — so a service found through a generated descriptor and a file
// found through the embedded source meet on the same name.
var protoSources sync.Map

// compiled caches the descriptor compiled from each registered source.
//
// Compilation is not cheap and a subsystem is asked about its contracts on every call that
// generates help, so each source is compiled once. The cache is never invalidated: the source
// is embedded in the binary, so it cannot change under a running process.
var compiled sync.Map

// compiledFailure remembers a source that would not compile.
//
// A failure is remembered because the alternative is re-parsing a broken file on every request
// to find out the same thing, and a subsystem whose .proto does not compile should say so once
// rather than on every help page.
var compiledFailure sync.Map

// RegisterProtoSource registers the original text of a .proto file, so that documentation for
// the contracts it declares is read from the source rather than from a generated descriptor.
//
// The path must be the descriptor's path — the same string FindFileByPath would be asked for —
// because that is what ties a contract found through reflection to the source that documents it.
func RegisterProtoSource(path string, source []byte) error {
	name := strings.TrimSpace(path)
	if name == "" {
		return fmt.Errorf("a proto source needs the path it was declared at")
	}
	if len(source) == 0 {
		return fmt.Errorf("the proto source at %s is empty", name)
	}
	if _, loaded := protoSources.LoadOrStore(name, append([]byte(nil), source...)); loaded {
		return fmt.Errorf("a proto source for %s is already registered", name)
	}
	return nil
}

// RegisterEmbeddedProtoSource registers a proto source from a package's init, and panics if it
// cannot.
//
// It is the form a docs_embed.go calls, and panicking is right there: a subsystem that cannot
// register its own contract is broken at build time, and a process that started anyway would
// serve help with no descriptions and no annotations, which is exactly the failure this exists
// to remove.
func RegisterEmbeddedProtoSource(path string, source []byte) {
	if err := RegisterProtoSource(path, source); err != nil {
		panic(err)
	}
}

// hasProtoSource reports whether the original text of a file is available.
func hasProtoSource(path string) bool {
	_, ok := protoSources.Load(path)
	return ok
}

// documentedFile returns a descriptor for a file that carries its original documentation.
//
// When the source is registered it is compiled with source info, so the comments and the
// annotations in it become readable. When it is not — an external service reached by
// reflection, say, whose .proto this process does not have — the descriptor is returned as it
// arrived, which still works when the caller passed a descriptor set that carried source info.
func documentedFile(file protoreflect.FileDescriptor) protoreflect.FileDescriptor {
	if file == nil {
		return nil
	}
	path := file.Path()
	if !hasProtoSource(path) {
		return file
	}
	compiledFile, err := compileSource(path)
	if err != nil {
		return file
	}
	return compiledFile
}

// CompileProtoSource parses a registered .proto into a descriptor that carries its source
// information, once, and returns the same descriptor on every later call.
//
// It is exported because a caller that needs a contract to work from — a tool generating
// commands, a test building a fixture — needs it compiled the same way the documentation is, or
// it is testing a path the framework does not take.
func CompileProtoSource(path string) (protoreflect.FileDescriptor, error) {
	return compileSource(path)
}

// compileSource parses a registered .proto into a descriptor with source info, once.
func compileSource(path string) (protoreflect.FileDescriptor, error) {
	if cached, ok := compiled.Load(path); ok {
		return cached.(protoreflect.FileDescriptor), nil
	}
	if cached, ok := compiledFailure.Load(path); ok {
		return nil, cached.(error)
	}
	if _, ok := protoSources.Load(path); !ok {
		return nil, fmt.Errorf("no proto source is registered for %s", path)
	}
	compiler := protocompile.Compiler{
		Resolver:       protoSourceResolver{},
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := compiler.Compile(context.Background(), path)
	if err != nil {
		wrapped := fmt.Errorf("the proto source at %s does not compile: %w", path, err)
		compiledFailure.Store(path, wrapped)
		return nil, wrapped
	}
	if len(files) == 0 {
		wrapped := fmt.Errorf("the proto source at %s compiled to no files", path)
		compiledFailure.Store(path, wrapped)
		return nil, wrapped
	}
	compiledFile := files[0]
	compiled.Store(path, compiledFile)
	return compiledFile, nil
}

// protoSourceResolver finds an import from the registered sources, or from the descriptors
// already linked into this process.
//
// The second case matters: a contract that imports the framework's api.proto resolves through
// the generated descriptors that api's own package linked in, so a subsystem does not have to
// embed a file it does not own.
type protoSourceResolver struct{}

// FindFileByPath implements protocompile.Resolver.
func (protoSourceResolver) FindFileByPath(path string) (protocompile.SearchResult, error) {
	if stored, ok := protoSources.Load(path); ok {
		return protocompile.SearchResult{Source: bytes.NewReader(stored.([]byte))}, nil
	}
	if linked, err := protoregistry.GlobalFiles.FindFileByPath(path); err == nil {
		return protocompile.SearchResult{Desc: linked}, nil
	}
	return protocompile.SearchResult{}, protoregistry.NotFound
}
