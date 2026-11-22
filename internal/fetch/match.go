package fetch

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/utils"
)

// assetMatcher picks the asset to install from a release's files, shared by
// the fetchers that list releases.
type assetMatcher struct {
	Env    Env
	Asset  string
	Format config.Format
	Rename map[string]string
}

// candidate is a release file.
type candidate struct {
	Name   string
	URL    string
	SHA256 string
}

// auxiliarySuffixes are release files that are never the installable asset.
var auxiliarySuffixes = []string{".sha256", ".sha512", ".sig", ".asc", ".minisig", ".pem", ".sbom", ".intoto.jsonl", "checksums.txt", "sha256sums"}

var packageSuffixes = []string{".deb", ".rpm", ".apk"}

func isAuxiliary(name string) bool {
	lower := strings.ToLower(name)
	return slices.ContainsFunc(auxiliarySuffixes, func(s string) bool { return strings.HasSuffix(lower, s) })
}

func (m assetMatcher) templateVars(v Version) map[string]string {
	os, arch := m.Env.systemInfo(m.Rename)
	return map[string]string{
		utils.OSTemplateVar:      os,
		utils.ArchTemplateVar:    arch,
		utils.VersionTemplateVar: strings.TrimPrefix(v.Tag, "v"),
		utils.TagTemplateVar:     v.Tag,
	}
}

// pick returns the single candidate that matches. The errors list the
// candidates, without auxiliary files, so one can be set as asset.
func (m assetMatcher) pick(v Version, cands []candidate) (Artifact, error) {
	vars := m.templateVars(v)

	var matches []Artifact
	var names []string
	for _, c := range cands {
		if !isAuxiliary(c.Name) {
			names = append(names, c.Name)
		}
		if !m.matches(c.Name, vars) {
			continue
		}
		matches = append(matches, Artifact{Version: v, Name: c.Name, URL: c.URL, SHA256: c.SHA256, Vars: vars})
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if m.Asset != "" {
			return Artifact{}, fmt.Errorf("no asset named %s in %s, available:\n  %s", utils.ExpandVars(m.Asset, vars), v.Tag, strings.Join(names, "\n  "))
		}
		return Artifact{}, fmt.Errorf("no asset matches os %s and arch %s in %s, set asset:\n  %s", vars[utils.OSTemplateVar], vars[utils.ArchTemplateVar], v.Tag, strings.Join(names, "\n  "))
	}

	var matched []string
	for _, a := range matches {
		matched = append(matched, a.Name)
	}
	return Artifact{}, fmt.Errorf("several assets match, set asset: to pick one:\n  %s", strings.Join(matched, "\n  "))
}

// matches reports whether name is the asset to install: an exact match of the
// expanded asset pattern if one is set, otherwise an asset naming both this
// host's os and arch. Packages are only picked when the format asks for one.
func (m assetMatcher) matches(name string, vars map[string]string) bool {
	if m.Asset != "" {
		return name == utils.ExpandVars(m.Asset, vars)
	}
	if isAuxiliary(name) {
		return false
	}

	lower := strings.ToLower(name)
	isPackage := slices.ContainsFunc(packageSuffixes, func(s string) bool { return strings.HasSuffix(lower, s) })
	pkgFormat := m.Format
	if pkgFormat == config.Package {
		pkgFormat = m.Env.Host.PackageFormat
	}
	wantsPackage := pkgFormat == config.Deb || pkgFormat == config.Rpm
	if isPackage != wantsPackage {
		return false
	}
	if wantsPackage && !strings.HasSuffix(lower, "."+string(pkgFormat)) {
		return false
	}

	return strings.Contains(lower, strings.ToLower(vars[utils.OSTemplateVar])) &&
		strings.Contains(lower, strings.ToLower(vars[utils.ArchTemplateVar]))
}
