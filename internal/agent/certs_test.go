package agent

import "testing"

func TestCertDir(t *testing.T) {
	for _, ok := range []string{"/etc/tza-certs/example.com", "/etc/nginx/ssl", "/opt/app/certs", "/var/lib/app/tls"} {
		if err := certDir(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "relative/dir", "/", "/etc", "/usr", "/etc/../root", "/etc/tza-certs/", "/proc/self", "/sys/x", "/dev/shm/x", "/a\nb"} {
		if err := certDir(bad); err == nil {
			t.Errorf("%q 要拒绝", bad)
		}
	}
}
