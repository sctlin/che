package log

import "fmt"

var verbose = false

func SetVerbose(v bool) { verbose = v }

func PrintVerbose(format string, args ...any) {
	if verbose {
		fmt.Printf(format, args...)
	}
}
