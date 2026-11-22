package main

import (
	"flag"
	"io"
	"slices"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantPos  []string
		wantYes  bool
		wantBins string
	}{
		{name: "flags first", args: []string{"-yes", "a", "b"}, wantPos: []string{"a", "b"}, wantYes: true},
		{name: "flags last", args: []string{"a", "b", "-yes"}, wantPos: []string{"a", "b"}, wantYes: true},
		{name: "interleaved", args: []string{"a", "-bin", "x=y", "b", "-yes"}, wantPos: []string{"a", "b"}, wantYes: true, wantBins: "x=y"},
		{name: "no flags", args: []string{"a"}, wantPos: []string{"a"}},
		{name: "dash is positional", args: []string{"-", "-yes"}, wantPos: []string{"-"}, wantYes: true},
		{name: "after double dash", args: []string{"a", "--", "-yes", "b"}, wantPos: []string{"a", "-yes", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			yes := fs.Bool("yes", false, "")
			bin := fs.String("bin", "", "")

			got, err := parseArgs(fs, tt.args)
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(got, tt.wantPos) {
				t.Errorf("positional: got %q, want %q", got, tt.wantPos)
			}
			if *yes != tt.wantYes {
				t.Errorf("yes: got %t, want %t", *yes, tt.wantYes)
			}
			if *bin != tt.wantBins {
				t.Errorf("bin: got %q, want %q", *bin, tt.wantBins)
			}
		})
	}
}

func TestParseArgsUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if _, err := parseArgs(fs, []string{"a", "-nope"}); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}
