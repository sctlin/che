package utils

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

func SaveToTmpFileAndRename(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "che.tmp*")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var templateVar = regexp.MustCompile(`\$\{([^}]*)\}`)

var (
	OSTemplateVar      string = "os"
	ArchTemplateVar    string = "arch"
	VersionTemplateVar string = "version"
	TagTemplateVar     string = "tag"
)
var availableTemplateVars = map[string]struct{}{
	"os":      {},
	"arch":    {},
	"version": {},
	"tag":     {},
}

func SetTemplateVar(vars map[string]string, key string, value string) bool {
	if _, ok := availableTemplateVars[key]; !ok {
		return false
	}
	vars[key] = value
	return true
}

func TemplateVars(s string) []string {
	return templateVar.FindAllString(s, -1)
}

func UnknownVars(s string, known ...string) []string {
	var unknown []string
	for _, m := range templateVar.FindAllStringSubmatch(s, -1) {
		if !slices.Contains(known, m[1]) {
			unknown = append(unknown, m[1])
		}
	}
	return unknown
}
func ExpandVars(s string, vars map[string]string) string {
	return templateVar.ReplaceAllStringFunc(s, func(m string) string {
		return vars[m[2:len(m)-1]]
	})
}
