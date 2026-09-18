package model

import (
	"net"
	"strconv"
	"strings"
)

// RouteLabel reports observed ASN evidence. It does not infer a commercial
// product (such as GIA) from a partial trace or a destination's carrier.
func RouteLabel(hops []Hop) string {
	names := map[string]string{"4134": "163", "4809": "CN2", "23764": "CTG", "4837": "4837", "9929": "9929", "10099": "10099", "58807": "CMIN2", "58453": "CMI", "9808": "CMI"}
	out := []string{}
	seen := map[string]bool{}
	for _, h := range hops {
		a := strings.TrimPrefix(strings.ToUpper(h.ASN), "AS")
		s := names[a]
		if s != "" && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	if len(out) == 0 {
		return "未知"
	}
	return strings.Join(out, " → ")
}
func NormalizeASN(s string) string {
	s = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "AS")
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return ""
	}
	return "AS" + strconv.FormatUint(n, 10)
}
func MaskAddress(s string) string {
	ip := net.ParseIP(s)
	if ip == nil {
		return "*"
	}
	if v := ip.To4(); v != nil {
		return strconv.Itoa(int(v[0])) + "." + strconv.Itoa(int(v[1])) + ".*.*"
	}
	parts := strings.Split(ip.String(), ":")
	return strings.Join(parts[:min(2, len(parts))], ":") + ":****"
}

// PublicAddress accepts only canonical masked values or an actual IP to mask.
// Arbitrary strings, even those containing an asterisk, cannot expose an address.
func PublicAddress(s string) string {
	if net.ParseIP(s) != nil {
		return MaskAddress(s)
	}
	if strings.HasSuffix(s, ".*.*") {
		parts := strings.Split(s, ".")
		if len(parts) == 4 {
			a, e1 := strconv.ParseUint(parts[0], 10, 8)
			b, e2 := strconv.ParseUint(parts[1], 10, 8)
			if e1 == nil && e2 == nil {
				return strconv.FormatUint(a, 10) + "." + strconv.FormatUint(b, 10) + ".*.*"
			}
		}
	}
	if strings.HasSuffix(s, ":****") {
		prefix := strings.TrimSuffix(s, ":****")
		parts := strings.Split(prefix, ":")
		if len(parts) == 2 {
			a, e1 := strconv.ParseUint(parts[0], 16, 16)
			b, e2 := strconv.ParseUint(parts[1], 16, 16)
			if e1 == nil && e2 == nil {
				return strconv.FormatUint(a, 16) + ":" + strconv.FormatUint(b, 16) + ":****"
			}
		}
	}
	return "*"
}
