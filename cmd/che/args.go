package main

import (
	"flag"
	"strings"
)

// parseArgs parses flags anywhere among args and returns the positional
// arguments in order. Go's flag package stops at the first positional, so
// positionals are set aside and parsing resumes after them. Everything after
// "--" is positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		i := 0
		for i < len(args) && !isFlag(args[i]) {
			i++
		}
		positional = append(positional, args[:i]...)
		if i == len(args) {
			break
		}

		rest := args[i:]
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		args = fs.Args()

		if consumed := len(rest) - len(args); consumed > 0 && rest[consumed-1] == "--" {
			positional = append(positional, args...)
			break
		}
	}
	return positional, nil
}

func isFlag(arg string) bool {
	return len(arg) > 1 && strings.HasPrefix(arg, "-")
}
