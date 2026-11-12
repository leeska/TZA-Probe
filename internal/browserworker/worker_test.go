package browserworker

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
)

func TestIsolatedAcceptsFallbackTunnelDevices(t *testing.T) {
	if runtime.GOOS != "linux" {
		if Isolated([]Iface{{Name: "lo", Up: true, Addresses: 1}}) {
			t.Fatal("isolation is only provable on Linux")
		}
		return
	}
	// What a fresh network namespace looks like when ip_gre, ipip, sit… are loaded.
	fresh := []Iface{{Name: "lo", Up: true, Addresses: 2}, {Name: "tunl0"}, {Name: "gre0"}, {Name: "gretap0"}, {Name: "erspan0"}, {Name: "sit0"}, {Name: "ip6tnl0"}}
	if !Isolated(fresh) {
		t.Fatal("down, address-less fallback devices must not count as network access")
	}
	for _, leak := range []Iface{{Name: "eth0", Up: true, Addresses: 1}, {Name: "tunl0", Up: true}, {Name: "veth1", Addresses: 1}} {
		if Isolated(append([]Iface{{Name: "lo", Up: true, Addresses: 1}}, leak)) {
			t.Errorf("%+v accepted as isolated", leak)
		}
	}
	if Isolated([]Iface{{Name: "tunl0"}}) {
		t.Error("no loopback accepted")
	}
}

func writeProc(t *testing.T, root string, pid int, parent int, cmdline, extra string) {
	t.Helper()
	dir := filepath.Join(root, itoa(pid))
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\tchrome\nPPid:\t"+itoa(parent)+"\n"+extra), 0644)
	os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.ReplaceAll(cmdline, " ", "\x00")), 0644)
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestSandboxEvidence(t *testing.T) {
	build := func(renderer string) string {
		root := t.TempDir()
		writeProc(t, root, 1, 0, "init", "")
		writeProc(t, root, 2, 1, "chrome --headless", "Seccomp:\t0\nNSpid:\t2\n")     // browser: not sandboxed itself
		writeProc(t, root, 3, 2, "chrome --type=zygote", "Seccomp:\t0\nNSpid:\t3\n")  // zygote
		writeProc(t, root, 4, 3, "chrome --type=renderer --lang=en", renderer)        // renderer
		writeProc(t, root, 5, 1, "other --type=renderer", "Seccomp:\t0\nNSpid:\t5\n") // someone else's process
		return root
	}
	if ok, detail := SandboxEvidence(build("Seccomp:\t2\nNSpid:\t4\t1\n"), 2); !ok {
		t.Fatalf("confined renderer rejected: %s", detail)
	}
	for name, status := range map[string]string{
		"no seccomp":       "Seccomp:\t0\nNSpid:\t4\t1\n",
		"no pid namespace": "Seccomp:\t2\nNSpid:\t4\n",
	} {
		if ok, _ := SandboxEvidence(build(status), 2); ok {
			t.Errorf("%s accepted", name)
		}
	}
	empty := t.TempDir()
	writeProc(t, empty, 2, 1, "chrome", "")
	if ok, _ := SandboxEvidence(empty, 2); ok {
		t.Error("no renderer accepted")
	}
}

func TestLaunchNeverDisablesTheSandbox(t *testing.T) {
	args := strings.Join(Launch{Bin: "x", Profile: "/p", ProxyAddr: "127.0.0.1:9", Locale: "en-US"}.Args(), " ")
	for _, banned := range []string{"no-sandbox", "disable-setuid-sandbox", "single-process", "no-zygote", "disable-popup-blocking", "disable-site-isolation"} {
		if strings.Contains(args, banned) {
			t.Errorf("args contain %s", banned)
		}
	}
	for _, required := range []string{"--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=<-loopback>", "--disable-quic", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--host-resolver-rules=MAP * ~NOTFOUND , EXCLUDE 127.0.0.1"} {
		if !strings.Contains(args, required) {
			t.Errorf("args lack %s", required)
		}
	}
	if _, err := Start(context.Background(), Launch{Bin: "/bin/true", Profile: t.TempDir(), Locale: "--no-sandbox"}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("a sandbox-disabling flag was not refused: %v", err)
	}
}

func TestPermitted(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://www.google.com/":                true,
		"https://www.google.com:443/search?q=x":  true,
		"https://api64.ipify.org":                true,
		"https://scontent-hkg4-1.xx.fbcdn.net/x": false, // google job
		"http://www.google.com/":                 false,
		"https://www.google.com:8443/":           false,
		"https://user@www.google.com/":           false,
		"https://evil.example/":                  false,
		"https://www.google.com.evil.example/":   false,
	} {
		if got := Permitted("google", raw); got != want {
			t.Errorf("google %s = %v", raw, got)
		}
	}
	if !Permitted("meta", "https://scontent-hkg4-1.xx.fbcdn.net/x") || Permitted("meta", "https://www.google.com/") {
		t.Error("meta allowlist")
	}
}

// fakeServer is the Server's worker socket: it records CONNECTs and answers
// result posts, enough to drive the tunnel and the delivery loop.
type fakeServer struct {
	mu       sync.Mutex
	connects []string
	results  map[string][]byte
	status   int
}

func (f *fakeServer) serve(t *testing.T) string {
	t.Helper()
	// Short path: macOS limits Unix socket paths to 104 bytes.
	dir, err := os.MkdirTemp("", "bw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "w.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				request, err := http.ReadRequest(reader)
				if err != nil {
					return
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				switch {
				case request.Method == http.MethodConnect:
					f.connects = append(f.connects, request.URL.Path+" lease="+request.Header.Get("X-Job-Lease")+" dest="+request.Header.Get("X-Destination"))
					io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
					io.WriteString(conn, "tunnelled")
				case strings.HasSuffix(request.URL.Path, "/result"):
					body, _ := io.ReadAll(request.Body)
					f.results[request.URL.Path] = body
					status := f.status
					if status == 0 {
						status = 204
					}
					io.WriteString(conn, "HTTP/1.1 "+strconv.Itoa(status)+" X\r\nContent-Length: 0\r\n\r\n")
				}
			}()
		}
	}()
	return socket
}

var testJob = ipguard.Job{ID: strings.Repeat("a", 48), Lease: strings.Repeat("b", 48), Service: "google", Policy: ipguard.Policy{TimeoutSeconds: 60}}

func TestTunnelOnlyForwardsApprovedDestinations(t *testing.T) {
	server := &fakeServer{results: map[string][]byte{}}
	socket := server.serve(t)
	tunnel, err := OpenTunnel(socket, testJob)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	connect := func(target string) string {
		conn, err := net.Dial("tcp", tunnel.Addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
		raw, _ := io.ReadAll(conn)
		return string(raw)
	}
	if got := connect("www.google.com:443"); !strings.HasPrefix(got, "HTTP/1.1 200") || !strings.HasSuffix(got, "tunnelled") {
		t.Fatalf("approved destination: %q", got)
	}
	for _, target := range []string{"evil.example:443", "www.google.com:80", "127.0.0.1:22", "169.254.169.254:443"} {
		if got := connect(target); !strings.HasPrefix(got, "HTTP/1.1 403") {
			t.Errorf("%s: %q", target, got)
		}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.connects) != 1 || server.connects[0] != "/connect/"+testJob.ID+" lease="+testJob.Lease+" dest=www.google.com" {
		t.Fatalf("server saw %v", server.connects)
	}
}

func TestDeliverRetriesStoredResultsAndRecoversCrashedJobs(t *testing.T) {
	server := &fakeServer{results: map[string][]byte{}}
	socket := server.serve(t)
	state := t.TempDir()
	w := New(socket, state, "/nonexistent")
	done := filepath.Join(state, "jobs", testJob.ID)
	os.MkdirAll(done, 0700)
	writeJSON(filepath.Join(done, "job.json"), testJob)
	writeJSON(filepath.Join(done, "result.json"), ipguard.Result{Execution: "completed", Assessment: "unknown", DeniedOrigins: []string{}})
	// A job that crashed before writing a result.
	crashed := testJob
	crashed.ID = strings.Repeat("c", 48)
	crashDir := filepath.Join(state, "jobs", crashed.ID)
	os.MkdirAll(crashDir, 0700)
	writeJSON(filepath.Join(crashDir, "job.json"), crashed)

	if err := w.deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.deliver(context.Background()); err != nil { // the crashed job's failure goes out on the next pass
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	var first, second ipguard.Result
	json.Unmarshal(server.results["/jobs/"+testJob.ID+"/result"], &first)
	json.Unmarshal(server.results["/jobs/"+crashed.ID+"/result"], &second)
	if first.Execution != "completed" || second.Execution != "failed" || second.Reason != "browser_failed" {
		t.Fatalf("delivered %+v and %+v", first, second)
	}
	if entries, _ := os.ReadDir(filepath.Join(state, "jobs")); len(entries) != 0 {
		t.Fatalf("%d job directories left after delivery", len(entries))
	}
}

func TestDeliverDropsResultsTheServerRefuses(t *testing.T) {
	server := &fakeServer{results: map[string][]byte{}, status: 409}
	socket := server.serve(t)
	state := t.TempDir()
	dir := filepath.Join(state, "jobs", testJob.ID)
	os.MkdirAll(dir, 0700)
	writeJSON(filepath.Join(dir, "job.json"), testJob)
	writeJSON(filepath.Join(dir, "result.json"), ipguard.Result{Execution: "completed"})
	if err := New(socket, state, "").deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a result for an expired job was kept")
	}
}

func TestResultFilesArePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	if err := writeJSON(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func zipOf(t *testing.T, add func(*zip.Writer)) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	add(w)
	w.Close()
	return buffer.Bytes()
}

func TestUnzipRefusesEscapesAndLinks(t *testing.T) {
	escape := zipOf(t, func(w *zip.Writer) { f, _ := w.Create("../outside"); f.Write([]byte("x")) })
	if err := unzip(escape, t.TempDir()); err == nil {
		t.Error("path traversal extracted")
	}
	link := zipOf(t, func(w *zip.Writer) {
		header := &zip.FileHeader{Name: "link"}
		header.SetMode(os.ModeSymlink | 0777)
		f, _ := w.CreateHeader(header)
		f.Write([]byte("/etc/passwd"))
	})
	if err := unzip(link, t.TempDir()); err == nil {
		t.Error("symlink extracted")
	}
	dest := t.TempDir()
	good := zipOf(t, func(w *zip.Writer) {
		header := &zip.FileHeader{Name: "dir/chrome"}
		header.SetMode(04755) // setuid in the archive is dropped
		f, _ := w.CreateHeader(header)
		f.Write([]byte("bin"))
	})
	if err := unzip(good, dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, "dir", "chrome"))
	if err != nil || info.Mode()&os.ModeSetuid != 0 || info.Mode().Perm() != 0755 {
		t.Fatalf("extracted %v %v", info, err)
	}
}
