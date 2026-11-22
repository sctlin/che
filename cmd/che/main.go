package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/host"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
)

var sourcesPath string
var lockPath string
var statePath string

func init() {
	if path, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok {
		sourcesPath = fmt.Sprintf("%s/che/sources.yaml", path)
		lockPath = fmt.Sprintf("%s/che/sources.lock", path)
		statePath = fmt.Sprintf("%s/che/sources.state.yaml", path)
	} else {
		sourcesPath = fmt.Sprintf("%s/.config/che/sources.yaml", os.Getenv("HOME"))
		lockPath = fmt.Sprintf("%s/.config/che/sources.lock", os.Getenv("HOME"))
		statePath = fmt.Sprintf("%s/.config/che/sources.state.yaml", os.Getenv("HOME"))
	}
}

func defineGlobalFlags() func() {
	var sourcesPathFlag = flag.String("sources", "", "Path to sources file, if not provided, defaults to $XDG_CONFIG_HOME/che/sources.yaml")
	return func() {
		if sourcesPathFlag != nil && *sourcesPathFlag != "" {
			sourcesPath = *sourcesPathFlag
			// if sources path is set, derive the state path from it
			lockPath = strings.TrimSuffix(sourcesPath, ".yaml") + ".lock"
			statePath = strings.TrimSuffix(sourcesPath, ".yaml") + ".state.yaml"
		}
	}
}

func errorWithUsageExit(err error) {
	if err != nil {
		errorExit(fmt.Errorf("Usage: che <command> [options]\n %w", err))
	} else {
		errorExit(errors.New("Usage: che <command> [options]\n"))
	}
	os.Exit(1)
}

func errorExit(err error) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	os.Exit(1)
}

type App struct {
	sources map[string]*source.Source
	lock    *lock.Lock
	state   *state.State
	host    host.Host
	env     fetch.Env
}

func noopCmdFlags() func() map[string]any {
	return func() map[string]any { return map[string]any{} }
}
func main() {
	if len(os.Args) < 2 {
		errorWithUsageExit(nil)
	}
	command := os.Args[1]
	os.Args = os.Args[1:] // shift args to parse flags correctly
	// global flags
	handleGlobalFlags := defineGlobalFlags()
	cmd, err := getCmd(command)
	if err != nil {
		errorWithUsageExit(err)
	}
	handleCmdFlags := noopCmdFlags()
	if cmd.RegisterFlags != nil {
		handleCmdFlags = cmd.RegisterFlags()
	}
	// flag.CommandLine exits on errors itself
	args, _ := parseArgs(flag.CommandLine, os.Args[1:])
	handleGlobalFlags()

	rs, err := config.Load(sourcesPath)
	if errors.Is(err, fs.ErrNotExist) && cmd.SourcesOptional {
		rs, err = map[string]config.ResolvedSource{}, nil
	}
	if err != nil {
		errorExit(err)
	}

	// No overall timeout, downloads can take longer than API calls. The server
	// must still start responding within 30s.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	client := &http.Client{Transport: transport}
	h := host.Detect()
	env := fetch.Env{HTTP: client, Host: h}
	sources, err := source.NewSources(rs, env)
	if err != nil {
		errorExit(err)
	}

	stateFile, err := state.Load(statePath)
	if err != nil {
		errorExit(err)
	}
	lockFile, err := lock.Load(lockPath)
	if err != nil {
		errorExit(err)
	}

	if err := cmd.RunFunc(App{sources: sources, lock: lockFile, state: stateFile, host: h, env: env}, args, cmd.RequirementFunc, handleCmdFlags()); err != nil {
		errorExit(err)
	}
}
