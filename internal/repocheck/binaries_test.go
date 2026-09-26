package repocheck_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Manu343726/toolbox/internal/repocheck"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every compiler's magic number, because a check that only knows ELF would let the
// same mistake through on a developer's other machine.
func TestEveryKindOfBinaryIsFound(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		contents []byte
		want     string
	}{
		{"ELF", append([]byte{0x7f, 'E', 'L', 'F'}, 0x02, 0x01, 0x01, 0x00, 0, 0, 0, 0, 0, 0, 0, 0), "ELF binary"},
		{"PE", []byte{'M', 'Z', 0x90, 0x00, 0x03, 0x00, 0x00, 0x00}, "PE binary"},
		{"Mach-O, 64-bit", []byte{0xcf, 0xfa, 0xed, 0xfe, 0x07, 0x00, 0x00, 0x03}, "Mach-O binary"},
		{"Mach-O, 32-bit", []byte{0xce, 0xfa, 0xed, 0xfe, 0x07, 0x00, 0x00, 0x03}, "Mach-O binary"},
		{"Mach-O, 64-bit the other byte order", []byte{0xfe, 0xed, 0xfa, 0xcf, 0x07, 0x00, 0x00, 0x03}, "Mach-O binary"},
		{"Mach-O, 32-bit the other byte order", []byte{0xfe, 0xed, 0xfa, 0xce, 0x07, 0x00, 0x00, 0x03}, "Mach-O binary"},
		{"a universal Mach-O", []byte{0xca, 0xfe, 0xba, 0xbe, 0x00, 0x00, 0x00, 0x02}, "Mach-O binary"},
		{"WebAssembly", []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}, "WebAssembly module"},
		{"a static library", []byte("!<arch>\n/               "), "ar archive"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "out"), testCase.contents, 0o755))

			found, err := repocheck.Binaries(root)
			require.NoError(t, err)
			require.Len(t, found, 1)
			assert.Equal(t, "out", found[0].Path)
			assert.Equal(t, testCase.want, found[0].Kind)
			assert.Contains(t, found[0].Error(), "out")
			assert.Contains(t, found[0].Error(), "build output")
		})
	}
}

// A binary committed by mistake is named after the module and sits in the module's
// own directory, which is the one place a path-based rule would have to enumerate a
// new name for every subsystem.
func TestABinaryIsFoundWhereverItIsNamed(t *testing.T) {
	root := t.TempDir()
	elf := append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 16)...)
	for _, path := range []string{"toolbox", "subsystems/skill/skill", "cmd/toolbox/toolbox", "pkg/log/anything"} {
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, elf, 0o755))
	}
	found, err := repocheck.Binaries(root)
	require.NoError(t, err)

	paths := make([]string, 0, len(found))
	for _, binary := range found {
		paths = append(paths, binary.Path)
	}
	assert.Equal(t, []string{
		"cmd/toolbox/toolbox",
		"pkg/log/anything",
		"subsystems/skill/skill",
		"toolbox",
	}, paths, "found by what a file is, not by where it is, and in a stable order")
}

// Source is not a binary however it begins, and a check that got this wrong would
// report the repository's own files on every run and then be turned off.
func TestSourceIsNotABinary(t *testing.T) {
	root := t.TempDir()
	for path, contents := range map[string]string{
		"main.go":    "package main\n",
		"README.md":  "# toolbox\n",
		"data.json":  `{"name":"toolbox"}`,
		"empty.txt":  "",
		"script.sh":  "#!/bin/sh\necho hello\n",
		"script2.sh": "#!/usr/bin/env bash\n# an ELF loader is not an ELF file\necho hi\n",
		"just_text":  "ELF is what this text file is about\n",
		"no_ext":     "plain text\n",
	} {
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(contents), 0o644))
	}
	found, err := repocheck.Binaries(root)
	require.NoError(t, err)
	assert.Empty(t, found)
}

func TestVendoredAndMetadataDirectoriesAreSkippedByTheBinaryCheck(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"vendor/x/out", ".git/objects/ab/cdef", "one/main.go"} {
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		contents := []byte("package x\n")
		if filepath.Base(path) == "out" {
			contents = append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 16)...)
		}
		require.NoError(t, os.WriteFile(full, contents, 0o644))
	}
	found, err := repocheck.Binaries(root)
	require.NoError(t, err)
	assert.Empty(t, found)
}

func TestAnAbsentTreeIsAnErrorForTheBinaryCheck(t *testing.T) {
	_, err := repocheck.Binaries(filepath.Join(t.TempDir(), "nowhere"))
	require.Error(t, err)
}

// A binary inside a directory named bin is a local build somebody asked for: every
// Makefile here writes there, and .gitignore ignores it. Reporting one would mean
// this failed on every machine that had run make build, and a check that always
// fails is a check that gets turned off.
func TestABinaryInsideABinDirectoryIsNotReported(t *testing.T) {
	root := t.TempDir()
	elf := append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 16)...)
	for _, path := range []string{"bin/toolbox", "subsystems/skill/bin/skill", "cmd/toolbox/bin/toolbox"} {
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, elf, 0o755))
	}
	found, err := repocheck.Binaries(root)
	require.NoError(t, err)
	assert.Empty(t, found)

	// The same binary beside the source rather than in bin/ is reported, which is
	// the whole distinction.
	stray := filepath.Join(root, "subsystems/skill/skill")
	require.NoError(t, os.WriteFile(stray, elf, 0o755))
	found, err = repocheck.Binaries(root)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, filepath.Join("subsystems", "skill", "skill"), found[0].Path)
}

// The assertion about this repository rather than the mechanism. One of the files
// it would have caught is 30MB of ELF at the root, which AGENTS.md says not to
// commit, so the check is also stated against the working tree: a binary beside the
// source, tracked or not, is one `git add -A` away from being committed by whoever
// runs it next.
//
// It fails on `go build ./...` run inside a module directory, which writes a binary
// for the module's main package into that directory. That is deliberate: AGENTS.md
// says to build through a Makefile target, and a check that let this pass would
// leave the rule as advice.
func TestNoBuildOutputSitsBesideTheSourceInThisRepository(t *testing.T) {
	found, err := repocheck.Binaries(repositoryRoot(t))
	require.NoError(t, err)

	if len(found) > 0 {
		reported := make([]string, 0, len(found))
		for _, binary := range found {
			reported = append(reported, binary.Error())
		}
		t.Errorf(
			"these build outputs are in the tree:%s\n\n"+
				"A build belongs in a bin/ directory, which is where every Makefile here puts it: "+
				"run 'make build', and 'make -C <module> clean' for the one left behind. "+
				"'go build ./...' inside a module directory writes a binary beside the source, "+
				"which is how one was committed at the repository root.",
			joinLines(reported))
	}
}
