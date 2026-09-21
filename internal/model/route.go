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
// 59.43.* is the CN2 backbone, 202.97.* is the core of the 163 backbone. Other
// China Telecom ranges (218.30.* for its international entry, 110.184.* to
// 110.199.* for provincial access, and the rest of the 163-family list below)
// are announced under AS4134 as well, which is why that ASN alone cannot name
// the 163 backbone — see cn2Label.
const (
	cn2Prefix   = "59.43."
	ct163Prefix = "202.97."
)

// prefixBackbones is the fallback used when the trace tool reports no ASN for a
// hop. Addresses are stored masked, so an IPv4 entry matches on /16 and an IPv6
// entry on the first two hextets. The list is deliberately narrow: an address
// that is not listed stays unknown instead of being guessed. Prefixes follow
// the operator backbone allocations taken from the earlier classifier tables.
var prefixBackbones = []struct {
	prefix string
	name   string
}{
	{"202.97.", "163"}, {"202.96.", "163"}, {"219.141.", "163"}, {"219.142.", "163"},
	{"106.37.", "163"}, {"240e:", "163"},
	{"219.158.", "4837"}, {"2408:", "4837"},
	{"210.14.", "9929"}, {"210.51.", "9929"}, {"210.78.", "9929"}, {"218.105.", "9929"},
	{"223.120.", "CMI"}, {"223.119.", "CMI"}, {"221.183.", "CMI"}, {"111.24.", "CMI"},
	{"111.13.", "CMI"}, {"2409:8080:", "CMI"},
	{"203.22.178.", "CTG"}, {"203.22.179.", "CTG"}, {"203.22.182.", "CTG"},
	{"203.128.224.", "CTG"}, {"69.194.", "CTG"}, {"2400:9380:", "CTG"},
	{"103.214.", "10099"}, {"103.239.176.", "10099"}, {"118.26.151.", "10099"},
	{"202.77.23.", "10099"}, {"2401:8a00:", "10099"},
	{"2402:4f00:", "CMIN2"},
}

// RouteLabel names the concrete backbones seen in a trace, in hop order, for
// example "CN2GT" or "4837 → 9929". Every hop is read by ASN first and by
// address prefix second, so the backbone evidence survives a trace whose CN2
// hops came back without an ASN. China Telecom is one product per trace, so its
// hops share the name ctLabel resolves; unmatched traces read "未知".
func RouteLabel(hops []Hop) string {
	firstCN2 := -1
	for i, h := range hops {
		if hopBackbone(h) == "CN2" {
			firstCN2 = i
			break
		}
	}
	ct := ctLabel(hops, firstCN2)
	out := []string{}
	seen := map[string]bool{}
	for _, h := range hops {
		name := hopBackbone(h)
		switch name {
		case "163", "CN2", "CTG":
			// hopBackbone names the evidence a single hop carries, ctLabel names
			// the product the 电信 path belongs to. The 163 hops around a CN2 run
			// are that product's access side, and a GIA or CTG trace crosses
			// China Telecom's own AS4134 ranges on both sides of it, so neither
			// may be reported as a second backbone.
			name = ct
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

// ctLabel names the single 电信 product a trace carries. CTG wins over CN2: a
// trace that shows CTGNet hops is a CTG line and its 59.43.* hops are that
// line's transport, which is how the neighbouring route classifiers resolve it
// too. Next comes the CN2 split; a trace with neither falls back to the 163 line
// its AS4134 hops prove. Other carriers keep their own names.
func ctLabel(hops []Hop, firstCN2 int) string {
	ctg, line163 := false, false
	for _, h := range hops {
		switch hopBackbone(h) {
		case "CTG":
			ctg = true
		case "163":
			line163 = true
		}
	}
	switch {
	case ctg:
		return "CTG"
	case firstCN2 >= 0:
		return cn2Label(hops, firstCN2)
	case line163:
		return "163"
	}
	return ""
}

// hopBackbone names the backbone a single hop proves. The ASN is authoritative;
// the address prefix only fills in for hops the tool could not resolve. AS4134
// names the 163 line because that ASN is China Telecom's own network: a hop it
// owns is 电信 traffic even when the trace never showed the 202.97.* core.
func hopBackbone(h Hop) string {
	asn := asnDigits(h.ASN)
	if asn == "4809" {
		return "CN2"
	}
	if name := backboneNames[asn]; name != "" {
		return name
	}
	if strings.HasPrefix(h.Address, cn2Prefix) {
		return "CN2"
	}
	for _, entry := range prefixBackbones {
		if strings.HasPrefix(h.Address, entry.prefix) {
			return entry.name
		}
	}
	return ""
}

// cn2Label splits CN2 into GIA and GT. A trace runs from the probe towards the
// Chinese target, so the China-side hand-off is what comes after the CN2 hops:
// a GT line leaves 59.43.* for the 163 backbone there, while a GIA line stays on
// 59.43.* down to the target's province. Looking at the hops in front of the
// first CN2 hop cannot decide this — AS4134 covers China Telecom's international
// entry and the target's provincial access network as well as 163, so a GIA
// trace carries AS4134 hops on both sides of 59.43.* without ever touching the
// 163 backbone. Only 202.97.* addresses prove the 163 backbone, and the split
// needs 59.43.* evidence at all, so a trace that reports AS4809 alone stays
// plain "CN2".
func cn2Label(hops []Hop, firstCN2 int) string {
	if !hasAddressPrefix(hops, cn2Prefix) {
		return "CN2"
	}
	for _, h := range hops[firstCN2+1:] {
		if strings.HasPrefix(h.Address, ct163Prefix) {
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
