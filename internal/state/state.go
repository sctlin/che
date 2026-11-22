package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/sctlin/che/internal/utils"
)

type State struct {
	Version int              `yaml:"version"`
	Sources map[string]Entry `yaml:"sources"`
}

func (st *State) Save(filepath string) error {
	content, err := yaml.Marshal(st)
	if err == nil {
		err = utils.SaveToTmpFileAndRename(filepath, content)
	}
	if err != nil {
		return fmt.Errorf("error while saving state file: %w", err)
	}
	return nil
}

// Inputs are config values copied verbatim, so the plan can detect config edits
// that don't change any other field of the Spec.
type Inputs struct {
	AssetPattern string            `yaml:"asset_pattern,omitempty"`
	Format       string            `yaml:"format,omitempty"` // config override, not the detected format
	Rename       map[string]string `yaml:"rename,omitempty"`
	Bin          map[string]string `yaml:"bin,omitempty"` // as written, globs included
	Build        string            `yaml:"build,omitempty"`
	Command      string            `yaml:"command,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
}

// Spec contains everything decided before installing
type Spec struct {
	Origin     string `yaml:"origin"`
	Format     string `yaml:"format"`
	Tag        string `yaml:"tag,omitempty"`
	Commit     string `yaml:"commit,omitempty"`
	Asset      string `yaml:"asset,omitempty"`
	InstallDir string `yaml:"install_dir,omitempty"`
	Inputs     Inputs `yaml:"inputs,omitempty"`
}

// Entry is a Spec plus what only the install itself can tell.
type Entry struct {
	Spec `yaml:",inline"`

	// Links contains the resolved links created during installation
	Links map[string]string `yaml:"links,omitempty"`
	// Package contains the manager and name of the package installed
	Package     *PackageRef `yaml:"package,omitempty"`
	InstalledAt time.Time   `yaml:"installed_at"`
}

// Display returns the tag, or a short commit when there is no tag.
func (s Spec) Display() string {
	switch {
	case s.Tag != "":
		return s.Tag
	case len(s.Commit) > 12:
		return s.Commit[:12]
	case s.Commit != "":
		return s.Commit
	}
	return "none"
}

type PackageRef struct {
	Manager string `yaml:"manager"`
	Name    string `yaml:"name"`
}

func Load(filepath string) (*State, error) {
	stateContent, err := os.ReadFile(filepath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &State{Version: 1, Sources: map[string]Entry{}}, nil
		}
		return nil, fmt.Errorf("error while reading state file: %w", err)
	}

	state := &State{}
	if err := yaml.Unmarshal(stateContent, state); err != nil {
		return nil, fmt.Errorf("error while unmarshalling state file: %w", err)
	}

	if state.Sources == nil {
		state.Sources = map[string]Entry{}
	}

	return state, nil
}
