package probe

import (
	"net"
	"net/netip"
)

// carrierASNRanges names the operator ranges the Agent fills in when the geo
// source returns no ASN for a hop.
//
// Why this exists: a line label is built from the hop's ASN first and from the
// masked address prefix only as a fallback, and the prefix table can only speak
// in /16s (IPv4) or /32s (IPv6) because hop addresses are stored masked. A hop
// the geo source leaves unresolved therefore loses its line — or takes a
// coarser name — even though the Agent still holds the unmasked address when
// the trace runs. The 9929 line is the case that surfaced: hops on it answer
// from 210.14.192.0/18, whose masked form 210.14.*.* also covers PLDT's
// 210.14.0.0/19, so the prefix table must not claim 210.14.* at all; the same
// trace next to it answered from 218.105.* and was labelled correctly, which is
// why one target's line flip-flopped between "10099 → 9929" and
// "10099 → 4837" while its neighbours stayed stable.
//
// Every entry is a prefix its ASN announces in the registry (RIPE Stat
// announced-prefixes, read 2026-09-25), so filling an unresolved hop from this
// table repeats registry data instead of guessing. The table is narrow on
// purpose and holds only the ranges where a missing ASN costs the label
// something: 中国联通's two international networks. 4837's own ranges
// (219.158.*, 2408:*) are already named by the prefix table, 电信's CN2 hops by
// 59.43.*, and foreign transit names nothing — none of them gain anything here.
var carrierASNRanges = []struct {
	prefix netip.Prefix
	asn    string
}{
	// 中国联通 9929 (CUII, the line sold as 联通 9929): its IPv4 backbone.
	{netip.MustParsePrefix("218.105.0.0/16"), "AS9929"},
	{netip.MustParsePrefix("210.51.0.0/16"), "AS9929"},
	// 210.14.0.0/16 is split: 210.14.0.0/19 is PLDT (AS9299) while
	// 210.14.192.0/18 and 210.14.249.0/24 are CUII. Only the CUII blocks are
	// listed, which the masked /16 form cannot express.
	{netip.MustParsePrefix("210.14.192.0/18"), "AS9929"},
	{netip.MustParsePrefix("210.14.249.0/24"), "AS9929"},
	// The line's IPv6 space. The registry lists AS9929's own blocks inside
	// 2408:8120::/32 (2408:8120:1::/48, 2408:8120:2::/48,
	// 2408:8120:7000::/36) and neither AS4837 nor AS10099 announces anything
	// in it.
	{netip.MustParsePrefix("2408:8120::/32"), "AS9929"},
	// 中国联通国际 10099 (CUG): the committed blocks that carry the 联通
	// international entry hops of a trace (118.26.144.0/22 covers 144–147, the
	// rest are announced separately), plus the 162.219.*, 202.77.*, 203.160.*
	// and 2401:8a00::/32 blocks that appear in the traces.
	{netip.MustParsePrefix("118.26.144.0/22"), "AS10099"},
	{netip.MustParsePrefix("118.26.148.0/24"), "AS10099"},
	{netip.MustParsePrefix("118.26.149.0/24"), "AS10099"},
	{netip.MustParsePrefix("118.26.153.0/24"), "AS10099"},
	{netip.MustParsePrefix("118.26.155.0/24"), "AS10099"},
	{netip.MustParsePrefix("118.26.157.0/24"), "AS10099"},
	{netip.MustParsePrefix("118.26.158.0/24"), "AS10099"},
	{netip.MustParsePrefix("162.219.32.0/21"), "AS10099"},
	{netip.MustParsePrefix("202.77.22.0/24"), "AS10099"},
	{netip.MustParsePrefix("203.160.64.0/19"), "AS10099"},
	{netip.MustParsePrefix("103.239.176.0/24"), "AS10099"},
	{netip.MustParsePrefix("2401:8a00::/32"), "AS10099"},
}

// carrierASN names the carrier that owns a hop address the geo source could not
// resolve. It must be given the real address: this runs before the address is
// masked for storage, and a masked address is exactly what makes the ranges
// above unusable as a prefix-table entry.
func carrierASN(ip net.IP) string {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return ""
	}
	addr = addr.Unmap()
	for _, entry := range carrierASNRanges {
		if entry.prefix.Contains(addr) {
			return entry.asn
		}
	}
	return ""
}

// asnOrCarrierRange keeps the measured ASN and fills in the carrier range when
// the geo source had no answer, so an unresolved hop still names its line. It
// takes the hop's real address (not the masked form stored for the panel):
// matching a masked address is what the ranges above cannot do.
func asnOrCarrierRange(measured, address string) string {
	if measured != "" || address == "" {
		return measured
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return ""
	}
	return carrierASN(ip)
}
