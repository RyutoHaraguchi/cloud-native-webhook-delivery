package core

import "net/netip"

// globalUnicastIPv6 は IPv6 で許可する範囲を表す。外側にはループバック、リンクローカル、
// IPv4 射影、NAT64 などがあり、今後割り当てられる範囲も含めて拒否する。
var globalUnicastIPv6 = netip.MustParsePrefix("2000::/3")

// blockedPrefixes は送信先として拒否するアドレスの範囲を表す。
var blockedPrefixes = []netip.Prefix{
	// IPv4
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),

	// 2000::/3 の中で公開されていない IPv6。Teredo と 6to4 は内部の IPv4 へ届く恐れがある
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

// IsBlockedIP は addr へ送信してはいけないときに true を返す。
// 判定できない不正な値も拒否する。
//
// IPv4 射影アドレスは公開アドレスでも拒否するので、net.IP を netip.AddrFromSlice で
// 変換して渡さない。16 バイト形式の IPv4 は射影アドレスになる。
func IsBlockedIP(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}

	// ゾーン付きのアドレスは Prefix.Contains で一致しないので、外してから比べる
	addr = addr.WithZone("")
	if addr.Is6() && !globalUnicastIPv6.Contains(addr) {
		return true
	}

	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}
