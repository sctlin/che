// Package version implements version constraints. A constraint is empty (the
// latest release), a range ("1.x", "1.x.x", "1.2.x") over semver-compatible
// tags, or an exact tag.
package version

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/mod/semver"
)

type kind int

const (
	latest kind = iota
	exact
	rng
)

type Constraint struct {
	raw  string
	kind kind
	// prefix is what semver.Major or semver.MajorMinor must return for a
	// tag in the range, e.g. "v1" or "v1.2".
	prefix string
}

// Parse parses a constraint. Anything without an "x" segment is an exact tag.
func Parse(s string) (Constraint, error) {
	if s == "" {
		return Constraint{}, nil
	}

	segs := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if !slices.Contains(segs, "x") {
		return Constraint{raw: s, kind: exact}, nil
	}

	invalid := fmt.Errorf("invalid version range %q, use 1.x, 1.x.x or 1.2.x", s)
	major, err := number(segs[0])
	if err != nil {
		return Constraint{}, invalid
	}

	switch {
	case len(segs) == 2 && segs[1] == "x",
		len(segs) == 3 && segs[1] == "x" && segs[2] == "x":
		return Constraint{raw: s, kind: rng, prefix: "v" + major}, nil
	case len(segs) == 3 && segs[2] == "x":
		minor, err := number(segs[1])
		if err != nil {
			return Constraint{}, invalid
		}
		return Constraint{raw: s, kind: rng, prefix: "v" + major + "." + minor}, nil
	}
	return Constraint{}, invalid
}

// number returns s without leading zeros, as semver writes it.
func number(s string) (string, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || strings.ContainsAny(s, "+-") {
		return "", fmt.Errorf("not a number: %q", s)
	}
	return strconv.Itoa(n), nil
}

func (c Constraint) String() string { return c.raw }

// Exact reports whether the constraint is an exact tag.
func (c Constraint) Exact() bool { return c.kind == exact }

// Range reports whether the constraint is a range.
func (c Constraint) Range() bool { return c.kind == rng }

// Matches reports whether the tag satisfies the constraint. Exact tags match
// with or without a leading "v".
func (c Constraint) Matches(tag string) bool {
	switch c.kind {
	case exact:
		return strings.TrimPrefix(tag, "v") == strings.TrimPrefix(c.raw, "v")
	case rng:
		v, ok := Tidy(tag)
		if !ok || semver.Prerelease(v) != "" {
			return false
		}
		if strings.Count(c.prefix, ".") == 0 {
			return semver.Major(v) == c.prefix
		}
		return semver.MajorMinor(v) == c.prefix
	}
	return true
}

// Select picks a tag from tags, listed newest first: the first one without a
// constraint, the matching one for an exact tag, the highest matching version
// for a range.
func (c Constraint) Select(tags []string) (string, error) {
	if len(tags) == 0 {
		return "", fmt.Errorf("no releases")
	}

	switch c.kind {
	case latest:
		return tags[0], nil
	case exact:
		for _, t := range tags {
			if c.Matches(t) {
				return t, nil
			}
		}
		return "", fmt.Errorf("no release tagged %s", c.raw)
	}

	best, bestV, skipped := "", "", 0
	for _, t := range tags {
		v, ok := Tidy(t)
		if !ok {
			skipped++
			continue
		}
		if c.Matches(t) && (best == "" || semver.Compare(v, bestV) > 0) {
			best, bestV = t, v
		}
	}
	if best == "" {
		msg := fmt.Sprintf("none of %d releases matches %s", len(tags), c.raw)
		if skipped > 0 {
			msg += fmt.Sprintf(", %d have tags that aren't semver", skipped)
		}
		return "", errors.New(msg)
	}
	return best, nil
}

// releaseSuffixes mark a release, not a prerelease, e.g. Godot's "4.7.2-stable".
var releaseSuffixes = []string{"-stable", "-release", "-final"}

// Tidy returns the semver form of a tag, and whether it has one: the prefix
// before the first digit is dropped, and the release suffixes. Whatever else
// follows a "-" stays a prerelease. Missing minor and patch numbers are 0.
// e.g. "jq-1.7.1" is v1.7.1, "4.7.2-stable" is v4.7.2, "4.8-rc1" is
// v4.8.0-rc1.
func Tidy(tag string) (string, bool) {
	i := strings.IndexFunc(tag, unicode.IsDigit)
	if i < 0 {
		return "", false
	}
	t := tag[i:]

	for _, s := range releaseSuffixes {
		if len(t) > len(s) && strings.EqualFold(t[len(t)-len(s):], s) {
			t = t[:len(t)-len(s)]
			break
		}
	}

	// semver accepts "v4.8" but not "v4.8-rc1", so short versions are padded
	core, rest := t, ""
	if i := strings.IndexAny(t, "-+"); i >= 0 {
		core, rest = t[:i], t[i:]
	}
	for strings.Count(core, ".") < 2 {
		core += ".0"
	}

	v := "v" + core + rest
	if !semver.IsValid(v) {
		return "", false
	}
	return v, true
}
