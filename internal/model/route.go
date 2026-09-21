package model

import (
	"net"
	"strconv"
	"strings"
)

// backboneNames names the concrete backbone an observed ASN operates. CN2
// (AS4809) is resolved separately because GIA and GT are different products
// sold on the same ASN.
var backboneNames = map[string]string{
	"4134": "163", "23764": "CTG", "4837": "4837", "9929": "9929",
	"10099": "10099", "58807": "CMIN2", "58453": "CMI", "9808": "CMI",
}

// CN2 hop prefixes. 59.43.* is the CN2 backbone and 202.97.* is the 163
// backbone. GIA stays on 59.43.* end to end; GT also crosses 202.97.*. The
// split is prefix evidence from the trace, not a carrier product guarantee,
// so a trace that never shows a 59.43.* hop stays plain "CN2".
const (
	cn2Prefix   = "59.43."
	ct163Prefix = "202.97."
)

// RouteLabel names the concrete backbones seen in a trace, in hop order, for
// example "163 → CN2GT" or "4837 → 9929". It never invents a product that the
// observed hops do not support; unmatched traces read "未知".
func RouteLabel(hops []Hop) string {
	gt := hasAddressPrefix(hops, ct163Prefix)
	out := []string{}
	seen := map[string]bool{}
	for _, h := range hops {
		asn := strings.TrimPrefix(NormalizeASN(h.ASN), "AS")
		name := backboneNames[asn]
		if asn == "4809" {
			name = cn2Label(hops, gt)
		}
		if name != "" && !seen[name] {
			out = append(out, name)
			seen[name] = true
		}
	}
	if len(out) == 0 {
		return "未知"
	}
	return strings.Join(out, " → ")
}

func cn2Label(hops []Hop, gt bool) string {
	if !hasAddressPrefix(hops, cn2Prefix) {
		return "CN2"
	}
	if gt {
		return "CN2GT"
	}
	return "CN2GIA"
}

func hasAddressPrefix(hops []Hop, prefix string) bool {
	for _, h := range hops {
		if strings.HasPrefix(h.Address, prefix) {
			return true
		}
	}
	return false
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
