package model

import (
	"net"
	"strconv"
	"strings"
)

// backboneNames names the concrete backbone an observed ASN operates. CN2
// (AS4809) is resolved separately because GIA and GT are different products
// sold on the same ASN. 中国移动 is not listed: AS9808 carries its domestic
// backbone and its international entry alike, so that ASN alone cannot name
// 中移国际 — see prefixBackbones.
var backboneNames = map[string]string{
	"4134": "163", "23764": "CTG", "4837": "4837", "9929": "9929",
	"10099": "10099", "58807": "CMIN2", "58453": "CMI",
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
// the operator backbone allocations taken from the earlier classifier tables,
// minus the entries the registry attributes to another carrier: 210.14.*
// resolves to PLDT (AS9299) and a Beijing ISP rather than 联通 9929, and
// 103.214.* to a Nepali and a Hong Kong/Orange block rather than 联通国际 10099.
// Naming the wrong operator is worse than staying unknown, and the ASN path
// still names the backbones when the tool resolves them.
//
// No 中移国际 entry fits here. The registry lists 221.183.*, 111.24.*,
// 111.13.* and 2409:8080:* under CHINAMOBILE-CN (AS9808, China Mobile
// Communications Group), which is the domestic backbone the international
// 58453/58807 feed into — not CMI. 223.120.* is split as well: its lower /17
// plus 223.120.0.0/22 belong to AS58453 (CMI) while 223.120.128.0/17 is
// AS58807 (CMIN2), and the mask cannot tell the two halves apart. CMIN2 has no
// entry for the same reason on IPv6: 58807 sits inside 2402:4f00:f000::/36
// while the mask keeps two hextets, so "2402:4f00:****" would label plain CMI
// hops as CMIN2. 202.96.* stays even though 202.96.0.0/21 is 联通: the rest of
// that /16 is 电信 (4134 and 4812), and the mask cannot split a /16.
var prefixBackbones = []struct {
	prefix string
	name   string
}{
	{"202.97.", "163"}, {"202.96.", "163"}, {"219.141.", "163"}, {"219.142.", "163"},
	{"106.37.", "163"}, {"240e:", "163"},
	{"219.158.", "4837"}, {"2408:", "4837"},
	{"210.51.", "9929"}, {"210.78.", "9929"}, {"218.105.", "9929"},
	{"223.119.", "CMI"},
	{"203.22.178.", "CTG"}, {"203.22.179.", "CTG"}, {"203.22.182.", "CTG"},
	{"203.128.224.", "CTG"}, {"69.194.", "CTG"}, {"2400:9380:", "CTG"},
	{"103.239.176.", "10099"}, {"118.26.151.", "10099"},
	{"202.77.23.", "10099"}, {"2401:8a00:", "10099"},
}

// RouteLabel names the concrete backbones seen in a trace, in hop order, for
// example "CN2GT" or "163 → 4837". Every hop is read by ASN first and by
// address prefix second, so the backbone evidence survives a trace whose CN2
// hops came back without an ASN. China Telecom is one product per trace, so its
// hops share the name ctLabel resolves; 中国联通's 169 backbone is dropped once
// the trace shows the 9929 line it lands on, so a 联通 回程 reads
// "10099 → 9929" rather than "10099 → 9929 → 4837". Unmatched traces read
// "未知".
func RouteLabel(hops []Hop) string {
	firstCN2 := -1
	for i, h := range hops {
		if hopBackbone(h) == "CN2" {
			firstCN2 = i
			break
		}
	}
	ct := ctLabel(hops, firstCN2)
	cm := cmLabel(hops)
	cu9929 := cuOn9929Line(hops)
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
		case "4837":
			// 4837 is 中国联通's own 169 backbone (AS4837, 219.158.*/2408:*),
			// and the network the 9929 international line feeds on the China
			// side. A trace that crossed 9929 has already named the line that
			// carried it, so 4837 here is that line's landing side rather than a
			// second line — the rule the 163 hops around a CN2 run get. Without
			// 9929 the trace crossed the 169 backbone itself, which is a
			// different 联通 line and keeps its name (10099 → 4837).
			if cu9929 {
				continue
			}
		case "CMI", "CMIN2":
			// 移动同理：CMI 与 CMIN2 都是中移国际的网络，一条回程只给一个产品名。
			name = cm
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

// cmLabel names the single 移动 product a trace carries. CMI (AS58453) and
// CMIN2 (AS58807) are both 中移国际 networks that feed the same domestic
// backbone, so a trace crossing both is one product, not two: CMIN2 is the
// newer network and the one adjacent to the China-side hand-off, which is the
// rule cn2Label already applies to a CN2 run surrounded by 163 hops. A trace
// that only shows 中国移动's own hops (AS9808 and friends) reports nothing —
// that ASN is the domestic backbone, not an international product.
func cmLabel(hops []Hop) string {
	cmin2, cmi := false, false
	for _, h := range hops {
		switch hopBackbone(h) {
		case "CMIN2":
			cmin2 = true
		case "CMI":
			cmi = true
		}
	}
	switch {
	case cmin2:
		return "CMIN2"
	case cmi:
		return "CMI"
	}
	return ""
}

// cuOn9929Line reports whether the trace crossed 中国联通's 9929 international
// line (AS9929; 210.51.*, 210.78.*, 218.105.*). That line is 联通国际's own
// network, and the 169 hops behind it (AS4837) are where it lands on the China
// side, so RouteLabel names one 联通 line instead of a chain. 10099 (联通国际)
// stays in front of it: it is the international entry the trace crossed, and a
// trace that never shows 9929 keeps 4837 as its 联通 name.
func cuOn9929Line(hops []Hop) bool {
	for _, h := range hops {
		if hopBackbone(h) == "9929" {
			return true
		}
	}
	return false
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
