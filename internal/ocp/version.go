// Package ocp models OpenShift release versions and derives the release
// image pullspecs used to extract openshift-install. It adapts dynamically
// to all OCP version families:
//
//   - GA releases:            4.18.6                 -> quay ocp-release:4.18.6-x86_64
//   - pre-GA channels:        5.0.0-ec.6, 4.22-rc.1  -> quay ocp-release:<ver>-x86_64
//   - CI nightlies:           5.0.0-0.nightly-...    -> release-<major>.<minor> imagestream
//
// Nightlies do NOT live in the plain "release" imagestream: they are
// tagged in per-minor streams ocp/release-4.14 / ocp/release-5.0 (etc.),
// so the major.minor stream MUST be derived from the version string.
package ocp

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// QuayRelease is the public quay path for GA / pre-GA releases.
	QuayRelease = "quay.io/openshift-release-dev/ocp-release"
	// CINightlyBase is the in-cluster CI registry hosting nightly imagestreams.
	CINightlyBase = "registry.ci.openshift.org/ocp"
)

// Version is a parsed OCP release version.
type Version struct {
	Raw     string
	Major   int
	Minor   int
	Patch   int
	Channel Channel // GA, EC, FC, RC, Nightly
	// ChannelN is the channel index for -ec.N / -fc.N / -rc.N (0 for others).
	ChannelN int
	// NightlyStamp is the stamp for nightly versions (0.nightly-<date>-<time>).
	NightlyStamp string
}

// Channel enumerates the release channels.
type Channel string

const (
	ChannelGA      Channel = "ga"
	ChannelEC      Channel = "ec"
	ChannelFC      Channel = "fc"
	ChannelRC      Channel = "rc"
	ChannelNightly Channel = "nightly"
)

var (
	// reRelease matches X.Y.Z optionally followed by -ec.N / -fc.N / -rc.N.
	// The patch component is mandatory (mirrors the legacy
	// idrac_sushy.py _OCP_VERSION_RE): a bare "X.Y" would derive a
	// non-existent quay tag, so it is rejected.
	reRelease = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-(ec|fc|rc)\.(\d+))?$`)
	// reNightly matches 4.14.0-0.nightly-2026-08-21-033959 style strings.
	reNightly = regexp.MustCompile(`^(\d+)\.(\d+)\.0-0\.nightly-(.+)$`)
)

// Parse parses a version string such as "5.0.0-ec.6", "4.18.6" or
// "5.0.0-0.nightly-2026-08-21-033959".
func Parse(raw string) (Version, error) {
	raw = strings.TrimSpace(raw)
	if m := reNightly.FindStringSubmatch(raw); m != nil {
		maj, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		return Version{
			Raw:          raw,
			Major:        maj,
			Minor:        min,
			Patch:        0,
			Channel:      ChannelNightly,
			NightlyStamp: m[3],
		}, nil
	}
	if m := reRelease.FindStringSubmatch(raw); m != nil {
		v := Version{
			Raw:     raw,
			Major:   atoiDefault(m[1]),
			Minor:   atoiDefault(m[2]),
			Channel: ChannelGA,
		}
		v.Patch = atoiDefault(m[3])
		if m[4] != "" {
			v.Channel = Channel(strings.ToLower(m[4]))
			v.ChannelN = atoiDefault(m[5])
		}
		return v, nil
	}
	return Version{}, fmt.Errorf(
		"invalid OCP version %q: expected X.Y.Z with an optional -ec.N/-fc.N/-rc.N suffix (e.g. 5.0.0-ec.6, 4.18.6) or a nightly X.Y.0-0.nightly-<stamp>",
		raw,
	)
}

// atoiDefault parses s, returning 0 when the field is empty.
func atoiDefault(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// (Version).IsNightly reports whether this is a CI nightly build.
func (v Version) IsNightly() bool { return v.Channel == ChannelNightly }

// (Version).IsPreGA reports ec/fc/rc channels.
func (v Version) IsPreGA() bool {
	return v.Channel == ChannelEC || v.Channel == ChannelFC || v.Channel == ChannelRC
}

// (Version).Normalized returns the canonical X.Y.Z form (nightly: X.Y.0).
func (v Version) Normalized() string {
	if v.IsNightly() {
		return fmt.Sprintf("%d.%d.0", v.Major, v.Minor)
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// (Version).MinorKey returns "major.minor" e.g. "5.0".
func (v Version) MinorKey() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// (Version).QuayPullSpec returns the public quay pullspec for GA / pre-GA
// releases (these are signed and available on quay.io).
func (v Version) QuayPullSpec() string {
	return fmt.Sprintf("%s:%s-x86_64", QuayRelease, v.Raw)
}

// (Version).NightlyPullSpec returns the CI registry pullspec for nightly
// builds, using the release-<major>.<minor> imagestream. Nightly tags are
// only fresh for a few days; prefer the by-digest form for durability.
func (v Version) NightlyPullSpec() string {
	return fmt.Sprintf("%s/release-%s:%s", CINightlyBase, v.MinorKey(), v.Raw)
}

// (Version).DefaultPullSpec returns the best first-choice pullspec for this
// version family.
func (v Version) DefaultPullSpec() string {
	if v.IsNightly() {
		return v.NightlyPullSpec()
	}
	return v.QuayPullSpec()
}

// (Version).Matches reports whether the version matches a pattern from an
// automation definition. Supported patterns:
//
//   - "5"      -> major 5
//   - "5.0"    -> major.minor 5.0
//   - "5.0.*"  -> same as "5.0"
//   - "5.*"    -> same as "5"
//   - "4.18.6" -> exact
//   - "nightly"       -> any nightly
//   - "5.0.0-0.nightly" -> nightly on 5.0 (prefix match allowed)
func (v Version) Matches(pattern string) bool {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return true
	}
	if p == "nightly" {
		return v.IsNightly()
	}
	if idx := strings.Index(p, ".nightly"); idx != -1 {
		head := p[:idx]
		if !v.IsNightly() {
			return false
		}
		nv, err := Parse(head)
		if err != nil {
			return false
		}
		return nv.Major == v.Major && nv.Minor == v.Minor
	}

	// Strip wildcard suffixes: "5.0.*" -> "5.0", "5.*" -> "5".
	if strings.HasSuffix(p, ".*") {
		p = strings.TrimSuffix(p, ".*")
	}

	parts := strings.Split(p, ".")
	switch len(parts) {
	case 1:
		return v.Major == atoiDefault(parts[0])
	case 2:
		return v.Major == atoiDefault(parts[0]) && v.Minor == atoiDefault(parts[1])
	case 3:
		return v.Major == atoiDefault(parts[0]) &&
			v.Minor == atoiDefault(parts[1]) &&
			v.Patch == atoiDefault(parts[2])
	default:
		return false
	}
}

// Compare returns -1, 0 or 1 ordering versions (major, minor, patch).
// Channel ordering for equal numeric parts: nightly < rc < ec+fc < ga
// (nightlies are the least stable, GA the most).
func (a Version) Compare(b Version) int {
	cmp3 := func(x, y int) int {
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		default:
			return 0
		}
	}
	if c := cmp3(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmp3(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmp3(a.Patch, b.Patch); c != 0 {
		return c
	}
	rank := func(v Version) int {
		switch v.Channel {
		case ChannelNightly:
			return 0
		case ChannelRC:
			return 1
		case ChannelEC, ChannelFC:
			return 2
		default:
			return 3
		}
	}
	return cmp3(rank(a), rank(b))
}
