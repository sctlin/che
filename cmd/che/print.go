package main

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	planner "github.com/sctlin/che/internal/plan"
	"github.com/sctlin/che/internal/state"
	"github.com/sctlin/che/internal/utils"
)

const (
	csiGreen   = "\033[38;2;100;170;100m"
	csiRed     = "\033[38;2;190;80;80m"
	csiYellow  = "\033[38;2;180;140;60m"
	csiBlue    = "\033[38;2;100;140;190m"
	csiMagenta = "\033[38;2;160;110;180m"
	csiReset   = "\033[0m"
)

// useColor is false when NO_COLOR is set or stdout isn't a terminal, so piped
// output stays free of escape codes.
var useColor = func() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

type symbol struct {
	char  string
	color string
}

func (s symbol) String() string {
	if !useColor {
		return s.char
	}
	return s.color + s.char + csiReset
}

var (
	symAdd     = symbol{"+", csiGreen}
	symRemove  = symbol{"-", csiRed}
	symChange  = symbol{"~", csiYellow}
	symBlocked = symbol{"!", csiRed}
)

var kindSymbol = map[planner.Kind]symbol{
	planner.Install:   symAdd,
	planner.Upgrade:   {"^", csiBlue},
	planner.Reinstall: {"%", csiMagenta},
	planner.Modify:    symChange,
	planner.Replace:   {"*", csiYellow},
	planner.Remove:    symRemove,
}

// legendOrder lists kinds from least to most destructive.
var legendOrder = []planner.Kind{planner.Install, planner.Upgrade, planner.Reinstall, planner.Modify, planner.Replace, planner.Remove}

func printPlan(actions []planner.Action) {
	if len(actions) == 0 {
		fmt.Println("No changes, installed sources match the config and lock")
		return
	}

	fmt.Println("Actions are indicated with the following symbols:")
	for _, k := range legendOrder {
		fmt.Printf("  %s %s\n", kindSymbol[k], k)
	}
	fmt.Println()

	counts := map[planner.Kind]int{}
	for _, a := range actions {
		counts[a.Kind]++
		printAction(a)
	}

	var summary []string
	for _, k := range legendOrder {
		summary = append(summary, fmt.Sprintf("%d to %s", counts[k], k))
	}
	fmt.Printf("\nPlan: %s\n", strings.Join(summary, ", "))
}

func printAction(a planner.Action) {
	fmt.Printf("%s %s%s\n", kindSymbol[a.Kind], a.ID, header(a))

	switch a.Kind {
	case planner.Install:
		printInstall(a)
	case planner.Remove:
		printRemove(*a.Prev)
	case planner.Replace:
		for _, c := range a.Changes {
			printChange(a, c)
		}
		printRemove(*a.Prev)
		printInstall(a)
	default:
		for _, c := range a.Changes {
			printChange(a, c)
		}
	}

	for _, b := range a.Blocked {
		line(symBlocked, "blocked: %s", b)
	}
}

// header follows the ID: "@v0.75.0" for installs, "@v0.70.0 -> v0.75.0" for
// upgrades. What gets downloaded is listed as a step below it.
func header(a planner.Action) string {
	switch a.Kind {
	case planner.Install, planner.Replace:
		return "@" + a.Desired.Display()
	case planner.Upgrade:
		return fmt.Sprintf("@%s -> %s", a.Prev.Display(), a.Desired.Display())
	case planner.Reinstall, planner.Modify, planner.Remove:
		return "@" + a.Prev.Display()
	}
	return ""
}

func printInstall(a planner.Action) {
	d := a.Desired
	if repo, ok := strings.CutPrefix(d.Origin, "git:"); ok {
		line(symAdd, "clones %s", repo)
	} else if d.Asset != "" {
		line(symAdd, "downloads %s", d.Asset)
	}

	switch {
	case d.Inputs.Command != "":
		line(symAdd, "runs %s", d.Inputs.Command)
	case isPackageFormat(d.Format):
		line(symAdd, "installs as a system package (%s)", d.Format)
	default:
		line(symAdd, "installs to %s", d.InstallDir)
		if d.Inputs.Build != "" {
			line(symAdd, "builds with %s", d.Inputs.Build)
		}
		for _, l := range a.Links {
			line(symAdd, "links %s to %s", l, linkTarget(a, l))
		}
	}
}

func printRemove(e state.Entry) {
	switch {
	case e.Package != nil:
		line(symRemove, "uninstalls package %s (%s)", e.Package.Name, e.Package.Manager)
	case e.Inputs.Command != "":
		line(symBlocked, "was installed by a custom command, its files must be removed manually")
	default:
		if e.InstallDir != "" {
			line(symRemove, "deletes %s", e.InstallDir)
		}
		for _, l := range slices.Sorted(maps.Keys(e.Links)) {
			line(symRemove, "unlinks %s from %s", l, e.Links[l])
		}
	}
}

func printChange(a planner.Action, c planner.Change) {
	field, key, _ := strings.Cut(c.Field, ".") // "env.CGO_ENABLED" -> "env", "CGO_ENABLED"

	switch field {
	case "version":
		if a.Kind != planner.Upgrade { // upgrades show it in the header
			line(symChange, "changes version from %s to %s", c.From, c.To)
		}
	case "link":
		if c.From != "" {
			line(symRemove, "unlinks %s from %s", c.From, a.Prev.Links[c.From])
		} else {
			line(symAdd, "links %s to %s", c.To, linkTarget(a, c.To))
		}
	case "install_dir":
		line(symChange, "moves %s to %s", c.From, c.To)
	case "asset":
		line(symChange, "downloads %s", c.To)
	case "origin":
		line(symChange, "switches source from %s to %s", c.From, c.To)
	case "format":
		line(symChange, "switches format from %s to %s", c.From, c.To)
	default:
		printSettingChange(field, key, c.From, c.To)
	}
}

// settingNames name config fields in plan output.
var settingNames = map[string]string{
	"build":           "build command",
	"command":         "install command",
	"asset_pattern":   "asset pattern",
	"format_override": "format setting",
	"rename":          "rename",
	"bin":             "binary",
	"env":             "environment variable",
}

// printSettingChange describes a change to a config value, e.g.
// "sets environment variable CGO_ENABLED to 0".
func printSettingChange(field, key, from, to string) {
	name := cmp.Or(settingNames[field], field)
	if key != "" {
		name += " " + key
	}

	switch {
	case from == "":
		line(symAdd, "sets %s to %s", name, to)
	case to == "":
		line(symRemove, "unsets %s (was %s)", name, from)
	default:
		line(symChange, "changes %s from %s to %s", name, from, to)
	}
}

// linkTarget returns where a desired link will point. Modify keeps the
// installed target, moved with the install dir, like apply does. Otherwise
// template variables are expanded when the plan resolved the release, globs
// only resolve after unpacking.
func linkTarget(a planner.Action, link string) string {
	d := a.Desired
	name := filepath.Base(link)

	if a.Kind == planner.Modify {
		for l, target := range a.Prev.Links {
			if filepath.Base(l) != name {
				continue
			}
			if rel, err := filepath.Rel(a.Prev.InstallDir, target); err == nil && filepath.IsLocal(rel) {
				return filepath.Join(d.InstallDir, rel)
			}
			return target
		}
	}

	path := d.Inputs.Bin[name]
	if a.Artifact != nil {
		path = utils.ExpandVars(path, a.Artifact.Vars)
	}
	return filepath.Join(d.InstallDir, path)
}

func isPackageFormat(f string) bool {
	return f == "deb" || f == "rpm"
}

func line(s symbol, format string, args ...any) {
	fmt.Printf("    %s %s\n", s, fmt.Sprintf(format, args...))
}
