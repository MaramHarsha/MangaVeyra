package main

import (
	"strings"
	"testing"
)

func TestRunRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--no-such-flag"},
		{"extra-argument"},
		{"--check-delay", "-1s"},
		{"--poll-interval", "100ms"},
		{"--rate-limit", "0"},
		{"--auto-approve", "--auto-approve-as", "aiko", "--addr", "127.0.0.1:0"}, // needs --seed
	} {
		if err := run(args); err == nil {
			t.Errorf("run(%s) succeeded, want an error", strings.Join(args, " "))
		}
	}
}

func TestRunHelp(t *testing.T) {
	if err := run([]string{"-h"}); err != nil {
		t.Fatalf("help: %v", err)
	}
}
