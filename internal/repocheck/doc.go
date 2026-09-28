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
// The rest are about a contract that exists and is not what it says it is, and
// they are in `contracts.go` and `modules.go`. The one worth reading twice is the
// pair that checks a contract's annotations reached the framework: a subsystem
// with a contract and nothing embedding its source, or a method declaring no
// effect, serves operations that no policy can classify, and an unclassified
// operation is *refused* — so the deployment looks correct while an operation
// nobody can call sits in it. `subsystems/skillgit` had all seven of its methods
// unclassified for that reason, and a `read_only` misdeclaration underneath it had
// never been exercised, because the classification never arrived.
package repocheck
