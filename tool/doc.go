//go:build linux

// Package tool runs mature system tools behind one contract: a Tool bound to one program, built by
// a runner's constructor, which finds the program and checks its version so a missing or outdated
// tool fails at startup. An adapter written against Tool runs the same code whichever runner built
// it. The package builds on Linux only.
package tool
