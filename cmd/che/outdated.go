package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
)

// $ che outdated
// ID         INSTALLED   LOCKED    WANTED    LATEST
// fzf        v0.67.0     v0.67.0   v0.75.0   v0.75.0
// node       22.11.0     22.11.0   22.14.0   24.1.0    constraint ^22
// lsp.zls    -           0.17.0    0.17.0    0.17.0
//
// ┌───────────┬─────────────────────────┬─────────────────────────────────────────────────┐
// │  Column   │         Source          │                     Meaning                     │
// ├───────────┼─────────────────────────┼─────────────────────────────────────────────────┤
// │ INSTALLED │ state                   │ what's on this machine (- if nothing)           │
// ├───────────┼─────────────────────────┼─────────────────────────────────────────────────┤
// │ LOCKED    │ lock                    │ what apply would install                        │
// ├───────────┼─────────────────────────┼─────────────────────────────────────────────────┤
// │ WANTED    │ upstream, constrained   │ what update would lock                          │
// ├───────────┼─────────────────────────┼─────────────────────────────────────────────────┤
// │ LATEST    │ upstream, unconstrained │ whether a constraint is holding the source back │
// └───────────┴─────────────────────────┴─────────────────────────────────────────────────┘
//
// Sources whose four columns agree are left out. Nothing is written.
func outdated(app App, args []string, reqFunc RequirementFunc, flags map[string]any) error {
	targets, err := resolveTargets(args, app.sources, app.state, slices.Sorted(maps.Keys(app.sources)), reqFunc)
	if err != nil {
		return fmt.Errorf("failed to resolve targets: %w", err)
	}
	ids := make([]string, len(targets))
	for i, t := range targets {
		ids[i] = t.ID
	}

	rows, err := outdatedRows(context.Background(), app, ids)
	printOutdated(os.Stdout, rows)
	if err != nil {
		return fmt.Errorf("failed to check some sources:\n%w", err)
	}
	return nil
}

type outdatedRow struct {
	id         string
	installed  string
	locked     string
	wanted     string
	latest     string
	constraint string
}

func (r outdatedRow) current() bool {
	return r.installed == r.locked && r.locked == r.wanted && r.wanted == r.latest
}

// outdatedRows returns the rows of the sources that aren't current. Errors are
// collected per ID, so the rows that could be checked are still returned.
func outdatedRows(c context.Context, app App, ids []string) ([]outdatedRow, error) {
	var rows []outdatedRow
	var errs []error
	for _, id := range ids {
		row, err := outdatedRowFor(c, app, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		if !row.current() {
			rows = append(rows, row)
		}
	}
	return rows, errors.Join(errs...)
}

func outdatedRowFor(c context.Context, app App, id string) (outdatedRow, error) {
	src := app.sources[id]
	row := outdatedRow{id: id, installed: "-", locked: "-", constraint: src.Config.Version}
	if e, ok := app.state.Sources[id]; ok {
		row.installed = displayVersion(e.Tag, e.Commit)
	}
	if e, ok := app.lock.Sources[id]; ok {
		row.locked = displayVersion(e.Tag, e.Commit)
	}

	wanted, err := src.Latest(c, src.Config.Version)
	if err != nil {
		return outdatedRow{}, err
	}
	row.wanted = displayVersion(wanted.Tag, wanted.Commit)

	row.latest = row.wanted
	if src.Config.Version != "" {
		latest, err := src.Latest(c, "")
		if err != nil {
			return outdatedRow{}, err
		}
		row.latest = displayVersion(latest.Tag, latest.Commit)
	}
	return row, nil
}

// displayVersion returns the tag, or a short commit when there is no tag.
// State and lock both use it, so their columns compare equal.
func displayVersion(tag, commit string) string {
	switch {
	case tag != "":
		return tag
	case len(commit) > 12:
		return commit[:12]
	case commit != "":
		return commit
	}
	return "-"
}

func printOutdated(w io.Writer, rows []outdatedRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "All sources are up to date")
		return
	}

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tINSTALLED\tLOCKED\tWANTED\tLATEST\t")
	for _, r := range rows {
		note := ""
		if r.constraint != "" {
			note = "constraint " + r.constraint
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.id, r.installed, r.locked, r.wanted, r.latest, note)
	}
	tw.Flush()

	// rows without a note end in padding
	for line := range strings.Lines(buf.String()) {
		fmt.Fprintln(w, strings.TrimRight(line, " \n"))
	}
}
