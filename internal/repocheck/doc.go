// Package repocheck holds the checks that run against the repository's own shape
// rather than against any package's behaviour.
//
// Two of them are about files, and both were found by a defect that nothing else
// reported. One is a Go file the toolchain never compiles: a directory holds one
// package, and a file in it that declares a different one is not an error —
// Go lists it under IgnoredGoFiles and carries on, so the build passes, the tests
// pass, and whatever the file was written to do has never happened. The framework
// registers its contracts and their documentation from files of exactly that
// kind, so a file silently skipped is a subsystem documenting nothing, and nobody
// finding out until a help page has no descriptions on it.
//
// The other is a compiled binary committed to the tree. "go build ." writes a
// binary named after the module into that module's own directory, which is the
// one way this repository produces a file that is definitely not source, and one
// of them was committed: 30MB of ELF at the repository root, in the repository
// whose AGENTS.md says not to commit binaries.
//
// The third is the CI matrix, in `modules.go`: a hand-written list, so a
// subsystem that is not on it is tested on its own by nothing while every job
// still reports green.
//
// Checks about a *contract* rather than a file are not here, and that is a
// constraint rather than an omission. Reading a contract's annotations means
// compiling it and interpreting what came back, which needs generated code — and
// this package deliberately imports nothing but the standard library so that it
// runs on a bare checkout, which is what lets the CI job named *formatting and
// staged files* assert that no Go file is one the toolchain skips before anything
// is built. Those checks are in `internal/contractcheck`, and the two packages
// divide on their subject: this one asks whether the files in the tree are the
// files they claim to be, and that one asks whether a contract says what it
// means.
package repocheck
