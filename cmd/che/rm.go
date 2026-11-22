package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/utils"
)

// rm deletes sources from the sources file, after confirmation. Installed ones
// are uninstalled by the next apply, the lock and state aren't touched.
func rm(app App, args []string, reqFunc RequirementFunc, flags map[string]any) error {
	if len(args) == 0 {
		return errors.New("usage: che rm <id>... [-yes]")
	}
	targets, err := resolveTargets(args, app.sources, app.state, nil, reqFunc)
	if err != nil {
		return fmt.Errorf("failed to resolve targets: %w", err)
	}

	content, err := os.ReadFile(sourcesPath)
	if err != nil {
		return err
	}
	var ids, installed []string
	fmt.Printf("The following lines will be removed from %s:\n", sourcesPath)
	for _, t := range targets {
		var removed string
		if content, removed, err = config.RemoveSource(content, t.ID); err != nil {
			return err
		}
		fmt.Println()
		for l := range strings.Lines(removed) {
			line(symRemove, "%s", strings.TrimRight(l, "\r\n"))
		}
		ids = append(ids, t.ID)
		if t.InState {
			installed = append(installed, t.ID)
		}
	}

	if yes, _ := flagAsType[bool]("yes", flags); !yes {
		ok, err := confirm("\nRemove these sources? [y/N] ")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Remove cancelled")
			return nil
		}
	}

	if err := utils.SaveToTmpFileAndRename(sourcesPath, content); err != nil {
		return err
	}
	fmt.Printf("Removed %s from %s\n", strings.Join(ids, ", "), sourcesPath)
	if len(installed) > 0 {
		fmt.Printf("Run \"che apply %s\" to uninstall\n", strings.Join(installed, " "))
	}
	return nil
}
