package browserworker

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Iface is the part of a network interface the isolation check looks at.
type Iface struct {
	Name      string
	Up        bool
	Addresses int
}

func interfaces() []Iface {
	list, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := []Iface{}
	for _, i := range list {
		addrs, _ := i.Addrs()
		out = append(out, Iface{Name: i.Name, Up: i.Flags&net.FlagUp != 0, Addresses: len(addrs)})
	}
	return out
}

// Isolated reports whether the only usable interface is loopback. A fresh
// network namespace (systemd PrivateNetwork=yes, docker --network none) also
// holds the kernel's fallback tunnel devices — tunl0, gre0, sit0, ip6tnl0… —
// whenever those modules are loaded on the host. They are down and have no
// address, so they cannot carry traffic; requiring that every interface be
// named lo (the Python worker's check) refuses to run on such hosts.
func Isolated(list []Iface) bool {
	if runtime.GOOS != "linux" || len(list) == 0 {
		return false
	}
	loopback := false
	for _, i := range list {
		if i.Name == "lo" {
			loopback = true
			continue
		}
		if i.Up || i.Addresses > 0 {
			return false
		}
	}
	return loopback
}

// processes is pid → (parent pid, /proc/<pid>/status, cmdline).
type process struct {
	parent  int
	status  string
	cmdline string
}

func readProcesses(root string) map[int]process {
	out := map[int]process{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join(root, entry.Name(), "status"))
		if err != nil {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join(root, entry.Name(), "cmdline"))
		p := process{status: string(status), cmdline: strings.ReplaceAll(string(cmdline), "\x00", " ")}
		for _, line := range strings.Split(p.status, "\n") {
			if value, ok := strings.CutPrefix(line, "PPid:"); ok {
				p.parent, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		out[pid] = p
	}
	return out
}

func descendantsIn(all map[int]process, root int) []int {
	children := map[int][]int{}
	for pid, p := range all {
		children[p.parent] = append(children[p.parent], pid)
	}
	out := []int{}
	queue := []int{root}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, child := range children[next] {
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

func descendants(root int) []int {
	if runtime.GOOS != "linux" {
		return nil
	}
	return descendantsIn(readProcesses("/proc"), root)
}

func statusField(status, name string) string {
	for _, line := range strings.Split(status, "\n") {
		if value, ok := strings.CutPrefix(line, name+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// SandboxEvidence checks every renderer under the browser: seccomp-BPF filter
// mode (Seccomp: 2) and its own PID namespace (NSpid lists more than one pid,
// the namespace sandbox). headless-shell has no chrome://sandbox page, and
// /proc is the stronger evidence anyway: it is the kernel's view, not Chromium's.
func SandboxEvidence(root string, browser int) (bool, string) {
	all := readProcesses(root)
	renderers := 0
	for _, pid := range descendantsIn(all, browser) {
		p := all[pid]
		if !strings.Contains(p.cmdline, "--type=renderer") {
			continue
		}
		renderers++
		seccomp := statusField(p.status, "Seccomp")
		nspid := strings.Fields(statusField(p.status, "NSpid"))
		if seccomp != "2" || len(nspid) < 2 {
			return false, "renderer " + strconv.Itoa(pid) + " seccomp=" + seccomp + " nspid=" + strings.Join(nspid, ",")
		}
	}
	if renderers == 0 {
		return false, "no renderer process"
	}
	return true, strconv.Itoa(renderers) + " renderers: seccomp filter + own pid namespace"
}
