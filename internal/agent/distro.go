package agent

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode"
)

// osReleasePath is where every distribution the Agent supports describes
// itself; it is a variable so tests can point at a fixture.
var osReleasePath = "/etc/os-release"

// distro reads the release file once per process: it cannot change while the
// Agent runs, and Collect asks for it on every report.
var distro = sync.OnceValue(detectDistro)

// Distro names the distribution, e.g. "Debian 13" or "Alpine 3.21". Plain
// runtime.GOOS is "linux" on all of them, so the panel has no other way to
// tell a Debian host from an Alpine one.
func Distro() string { return distro() }

func detectDistro() string {
	raw, err := os.ReadFile(osReleasePath)
	if err != nil {
		return runtime.GOOS
	}
	label := distroLabel(string(raw))
	if label == "" {
		return runtime.GOOS
	}
	return label
}

// distroLabel turns the os-release(5) body into "<name> <version-id>", or ""
// when the file names no distribution at all.
func distroLabel(content string) string {
	fields := osReleaseFields(content)
	name := shortDistroName(fields["name"])
	if name == "" {
		name = title(fields["id"])
	}
	if name == "" {
		return ""
	}
	version := fields["version_id"]
	if version == "" {
		// VERSION carries the codename too ("13 (trixie)"); the release
		// number in front of it is the part worth showing next to the name.
		if cut, _, found := strings.Cut(fields["version"], " ("); found {
			version = cut
		} else {
			version = fields["version"]
		}
	}
	return cleanLabel(strings.TrimSpace(name + " " + version))
}

// osReleaseFields reads the shell-style KEY=VALUE lines. Later definitions of
// the same key (systemd's os-release merge) do not override the first one.
func osReleaseFields(content string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := fields[key]; !exists && value != "" {
			fields[key] = value
		}
	}
	return fields
}

// shortDistroName drops the generic words every NAME carries ("Debian
// GNU/Linux", "Alpine Linux", "Oracle Linux Server"): the release number next
// to it is what the tag has room for.
func shortDistroName(name string) string {
	name = strings.Join(strings.Fields(strings.ReplaceAll(name, "GNU/Linux", "")), " ")
	for {
		trimmed := name
		for _, suffix := range []string{"Linux", "OS", "Server", "GNU"} {
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, suffix))
		}
		if trimmed == name || trimmed == "" {
			return name
		}
		name = trimmed
	}
}

// title capitalises a lower-case release id ("gentoo" -> "Gentoo") for the
// rare distribution whose NAME is missing.
func title(id string) string {
	runes := []rune(id)
	if len(runes) == 0 {
		return ""
	}
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

// cleanLabel keeps the value printable and within what the metrics schema
// accepts, so one odd release file cannot make every report fail validation.
func cleanLabel(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r == ' ' || (r < unicode.MaxASCII && unicode.IsPrint(r)):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
		if b.Len() >= 60 {
			break
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
