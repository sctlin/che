package lock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/sctlin/che/internal/utils"
)

type Lock struct {
	Version int              `yaml:"version"`
	Sources map[string]Entry `yaml:"sources"`
}

// Prune removes the entries of sources not in sources and returns how many.
func (l *Lock) Prune(sources map[string]struct{}) int {
	n := 0
	for s := range l.Sources {
		if _, ok := sources[s]; !ok {
			delete(l.Sources, s)
			n++
		}
	}
	return n
}

func (l *Lock) Save(filepath string) error {
	content, err := yaml.Marshal(l)
	if err == nil {
		err = utils.SaveToTmpFileAndRename(filepath, content)
	}
	if err != nil {
		return fmt.Errorf("error while saving lock file: %w", err)
	}
	return nil
}

type Entry struct {
	Origin    string            `yaml:"origin"`
	Tag       string            `yaml:"tag,omitempty"`
	Ref       string            `yaml:"ref,omitempty"`
	Commit    string            `yaml:"commit,omitempty"`
	Checksums map[string]string `yaml:"checksums,omitempty"`
}

func (e Entry) Display() string {
	if e.Tag != "" {
		return e.Tag
	} else if e.Commit != "" {
		return e.Commit
	}
	return "none"
}

func Load(filepath string) (*Lock, error) {
	lockContent, err := os.ReadFile(filepath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Lock{Version: 1, Sources: map[string]Entry{}}, nil
		}
		return nil, fmt.Errorf("error while reading lock file: %w", err)
	}

	lock := &Lock{}
	if err := yaml.Unmarshal(lockContent, lock); err != nil {
		return nil, fmt.Errorf("error while unmarshalling lock file: %w", err)
	}

	if lock.Sources == nil {
		lock.Sources = map[string]Entry{}
	}

	return lock, nil
}
