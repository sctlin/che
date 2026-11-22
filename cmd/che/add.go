package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/lock"
	planner "github.com/sctlin/che/internal/plan"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/utils"
)

// mapFlag is a repeatable key=value flag.
type mapFlag struct {
	m map[string]string
	// path allows a value without "=", keyed by its file name, like bin.
	path bool
}

func (f *mapFlag) String() string {
	if f == nil {
		return ""
	}
	var kvs []string
	for _, k := range slices.Sorted(maps.Keys(f.m)) {
		kvs = append(kvs, k+"="+f.m[k])
	}
	return strings.Join(kvs, ",")
}

func (f *mapFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		if !f.path {
			return fmt.Errorf("expected key=value, got %q", v)
		}
		k, val = filepath.Base(v), v
	}
	if k == "" || val == "" {
		return fmt.Errorf("expected key=value, got %q", v)
	}
	if _, dup := f.m[k]; dup {
		return fmt.Errorf("%s is set twice", k)
	}

	if f.m == nil {
		f.m = map[string]string{}
	}
	f.m[k] = val
	return nil
}

// formatFlag validates the format like the config does.
type formatFlag struct{ f *config.Format }

func (f formatFlag) String() string {
	if f.f == nil {
		return ""
	}
	return string(*f.f)
}

func (f formatFlag) Set(v string) error { return f.f.UnmarshalText([]byte(v)) }

func registerAddFlags() func() map[string]any {
	var c config.SourceConfig
	flag.StringVar(&c.Asset, "asset", "", "asset file name, may use ${os}, ${arch}, ${tag} and ${version}")
	flag.Var(formatFlag{&c.Format}, "format", "asset format, detected from the asset name if not set")
	flag.StringVar(&c.Version, "version", "", "version constraint")
	flag.StringVar(&c.Ref, "ref", "", "git ref to track")
	flag.StringVar(&c.Build, "build", "", "command building a git source")
	flag.StringVar(&c.Command, "command", "", "command installing the source")
	flag.StringVar(&c.InstallDir, "install-dir", "", "install dir, defaults to the group's")
	flag.StringVar(&c.BinDir, "bin-dir", "", "bin dir, defaults to the group's")
	flag.StringVar(&c.Jq, "jq", "", "jq filter for url sources")

	bin := &mapFlag{path: true}
	rename := &mapFlag{}
	env := &mapFlag{}
	flag.Var(bin, "bin", "name=path of a binary to link, or a path linked by its file name, repeatable")
	flag.Var(rename, "rename", "os or arch name the assets use, e.g. amd64=x86_64, repeatable")
	flag.Var(env, "env", "KEY=value for build and command, repeatable")

	noCheck := flag.Bool("no-check", false, "skip checking the source against upstream")

	return func() map[string]any {
		c.Bin, c.Rename, c.Env = bin.m, rename.m, env.m
		return map[string]any{"conf": c, "no-check": *noCheck}
	}
}

// add writes a new source to the sources file. Unless -no-check is set, the
// source is planned against upstream first, so a source whose asset can't be
// found isn't written. The lock and state aren't touched.
func add(app App, args []string, _ RequirementFunc, flags map[string]any) error {
	if len(args) != 2 {
		return errors.New("usage: che add <id> <origin> [flags]")
	}
	id := args[0]

	conf, _ := flagAsType[config.SourceConfig]("conf", flags)
	origin, err := config.ParseOrigin(args[1])
	if err != nil {
		return err
	}
	conf.Github, conf.Gitlab, conf.Git, conf.URL = origin.Github, origin.Gitlab, origin.Git, origin.URL

	content, err := os.ReadFile(sourcesPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := config.AddSource(content, id, conf)
	if err != nil {
		return err
	}

	noCheck, _ := flagAsType[bool]("no-check", flags)
	var actions []planner.Action
	if !noCheck {
		if actions, err = checkAdded(context.Background(), app, out, id); err != nil {
			return fmt.Errorf("%s failed the upstream check, nothing was written (-no-check skips it):\n%w", id, err)
		}
	}

	if err := utils.SaveToTmpFileAndRename(sourcesPath, out); err != nil {
		return err
	}

	fmt.Printf("Added %s to %s:\n\n", id, sourcesPath)
	for l := range strings.Lines(config.RenderSource(id, conf)) {
		fmt.Println("  " + strings.TrimRight(l, "\n"))
	}
	fmt.Println()

	switch {
	case noCheck:
		fmt.Printf("Run \"che plan %s\" to check it\n", id)
	case len(actions) == 0:
		fmt.Println("It matches what's installed, nothing to apply")
	default:
		for _, a := range actions {
			printAction(a)
		}
		verb := "apply it"
		if actions[0].Kind == planner.Install {
			verb = "install it"
		}
		fmt.Printf("\nRun \"che apply %s\" to %s\n", id, verb)
	}
	return nil
}

// checkAdded plans the added source against upstream, on a copy of the lock,
// so nothing is saved. A var, so tests can avoid the network.
var checkAdded = func(c context.Context, app App, content []byte, id string) ([]planner.Action, error) {
	rs, err := config.Parse(content)
	if err != nil {
		return nil, err
	}
	src, err := source.NewSource(rs[id], app.env)
	if err != nil {
		return nil, err
	}

	app.sources = maps.Clone(app.sources)
	app.sources[id] = src
	app.lock = &lock.Lock{Version: app.lock.Version, Sources: maps.Clone(app.lock.Sources)}

	if _, err := lockMissing(c, []string{id}, app); err != nil {
		return nil, err
	}
	return planner.Build(c, []string{id}, planner.Input{
		Sources: app.sources,
		Lock:    app.lock,
		State:   app.state,
		Host:    app.host,
		Resolve: resolveArtifact(app.lock),
	})
}
