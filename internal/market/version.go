package market

import (
	"regexp"
	"strconv"
	"strings"
)

var versionCore = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// semver reads the MAJOR.MINOR.PATCH core of v (a leading v and any
// pre-release / build suffix are ignored).
func semver(v string) ([3]int, bool) {
	m := versionCore.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func compare(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ProbeVersion is the version reported to CC: the MAJOR.MINOR.PATCH core of
// the build version ("v1.2.0" and "1.2.0-dev+abc" both report "1.2.0"), so
// CC compares plain release numbers. A version without that shape is sent as is.
func ProbeVersion(v string) string {
	if core, ok := semver(v); ok {
		return strconv.Itoa(core[0]) + "." + strconv.Itoa(core[1]) + "." + strconv.Itoa(core[2])
	}
	return strings.TrimSpace(v)
}

// Newer reports whether release a is newer than b.
func Newer(a, b string) bool {
	x, ok1 := semver(a)
	y, ok2 := semver(b)
	if !ok1 {
		return false
	}
	if !ok2 {
		return true
	}
	return compare(x, y) > 0
}

// Compatible reports whether probe satisfies a release's version range, such
// as ">=1.1.0" or ">=1.1.0 <2.0.0" (comparisons joined by spaces or commas,
// all must hold; a bare version means "="). An empty range fits every
// version; a range this Server cannot read fits none. A probe version without
// MAJOR.MINOR.PATCH (a hand-built binary) is treated as compatible.
func Compatible(rangeExpr, probe string) bool {
	rangeExpr = strings.TrimSpace(rangeExpr)
	if rangeExpr == "" {
		return true
	}
	have, ok := semver(probe)
	if !ok {
		return true
	}
	fields := strings.FieldsFunc(rangeExpr, func(r rune) bool { return r == ' ' || r == ',' })
	for i := 0; i < len(fields); i++ {
		tok := fields[i]
		op := strings.TrimRight(tok, "0123456789.v")
		ver := tok[len(op):]
		if ver == "" && i+1 < len(fields) {
			i++
			ver = fields[i]
		}
		want, ok := semver(ver)
		if !ok || len(versionCore.FindString(ver)) != len(ver) {
			return false
		}
		c := compare(have, want)
		switch op {
		case ">=":
			ok = c >= 0
		case ">":
			ok = c > 0
		case "<=":
			ok = c <= 0
		case "<":
			ok = c < 0
		case "", "=", "==":
			ok = c == 0
		default:
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}
