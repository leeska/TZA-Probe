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

// Address prefixes that identify a backbone on their own, without an ASN:
// 59.43.* is the CN2 backbone, 202.97.* belongs to the 163 backbone.
const (
	cn2Prefix   = "59.43."
	ct163Prefix = "202.97."
)

// RouteLabel names the concrete backbones seen in a trace, in hop order, for
// example "163 → CN2GT" or "4837 → 9929". Every hop is read by address prefix
// first and by ASN second, so the backbone evidence survives a trace whose CN2
// hops came back without an ASN. Unmatched traces read "未知".
func RouteLabel(hops []Hop) string {
	firstCN2 := -1
	for i, h := range hops {
		if isCN2Hop(h) {
			firstCN2 = i
			break
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, h := range hops {
		var name string
		switch {
		case isCN2Hop(h):
			name = cn2Label(hops, firstCN2)
		case is163Hop(h):
			name = "163"
		default:
			name = backboneNames[asnDigits(h.ASN)]
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

// isCN2Hop reports CN2 evidence: the 59.43.* backbone prefix or the AS4809
// owner. Traceroute tools regularly leave the ASN of these hops unresolved.
func isCN2Hop(h Hop) bool {
	return strings.HasPrefix(h.Address, cn2Prefix) || asnDigits(h.ASN) == "4809"
}

// is163Hop reports 163 evidence: the 202.97.* backbone prefix or AS4134, which
// is ChinaNet and therefore the same 163 backbone under a provincial prefix.
func is163Hop(h Hop) bool {
	return strings.HasPrefix(h.Address, ct163Prefix) || asnDigits(h.ASN) == "4134"
}

// cn2Label splits CN2 into GIA and GT. GIA enters the CN2 backbone directly,
// while GT first crosses the 163 backbone and only then reaches 59.43.*. The
// split needs 59.43.* evidence, so a trace that reports AS4809 alone stays
// plain "CN2".
func cn2Label(hops []Hop, firstCN2 int) string {
	if !hasAddressPrefix(hops, cn2Prefix) {
		return "CN2"
	}
	for _, h := range hops[:firstCN2] {
		if is163Hop(h) {
			return "CN2GT"
		}
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

func asnDigits(s string) string {
	return strings.TrimPrefix(NormalizeASN(s), "AS")
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
