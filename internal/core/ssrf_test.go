package core

import (
	"net/netip"
	"testing"
)

func TestIsBlockedIPv4(t *testing.T) {
	// 各範囲の先頭と末尾が拒否され、直前と直後は拒否されないことを確かめる。
	// 隣にも拒否範囲があるときは、直前や直後を省く
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"0.0.0.0/8 の先頭", "0.0.0.0", true},
		{"0.0.0.0/8 の末尾", "0.255.255.255", true},
		{"0.0.0.0/8 の直後", "1.0.0.0", false},
		{"10.0.0.0/8 の直前", "9.255.255.255", false},
		{"10.0.0.0/8 の先頭", "10.0.0.0", true},
		{"10.0.0.0/8 の末尾", "10.255.255.255", true},
		{"10.0.0.0/8 の直後", "11.0.0.0", false},
		{"100.64.0.0/10 の直前", "100.63.255.255", false},
		{"100.64.0.0/10 の先頭", "100.64.0.0", true},
		{"100.64.0.0/10 の末尾", "100.127.255.255", true},
		{"100.64.0.0/10 の直後", "100.128.0.0", false},
		{"127.0.0.0/8 の直前", "126.255.255.255", false},
		{"127.0.0.0/8 の先頭", "127.0.0.0", true},
		{"127.0.0.0/8 の末尾", "127.255.255.255", true},
		{"127.0.0.0/8 の直後", "128.0.0.0", false},
		{"169.254.0.0/16 の直前", "169.253.255.255", false},
		{"169.254.0.0/16 の先頭", "169.254.0.0", true},
		{"クラウドのメタデータ", "169.254.169.254", true},
		{"169.254.0.0/16 の末尾", "169.254.255.255", true},
		{"169.254.0.0/16 の直後", "169.255.0.0", false},
		{"172.16.0.0/12 の直前", "172.15.255.255", false},
		{"172.16.0.0/12 の先頭", "172.16.0.0", true},
		{"172.16.0.0/12 の末尾", "172.31.255.255", true},
		{"172.16.0.0/12 の直後", "172.32.0.0", false},
		{"192.0.0.0/24 の直前", "191.255.255.255", false},
		{"192.0.0.0/24 の先頭", "192.0.0.0", true},
		{"192.0.0.0/24 の末尾", "192.0.0.255", true},
		{"192.0.0.0/24 の直後", "192.0.1.0", false},
		{"192.0.2.0/24 の直前", "192.0.1.255", false},
		{"192.0.2.0/24 の先頭", "192.0.2.0", true},
		{"192.0.2.0/24 の末尾", "192.0.2.255", true},
		{"192.0.2.0/24 の直後", "192.0.3.0", false},
		{"192.88.99.0/24 の直前", "192.88.98.255", false},
		{"192.88.99.0/24 の先頭", "192.88.99.0", true},
		{"192.88.99.0/24 の末尾", "192.88.99.255", true},
		{"192.88.99.0/24 の直後", "192.88.100.0", false},
		{"192.168.0.0/16 の直前", "192.167.255.255", false},
		{"192.168.0.0/16 の先頭", "192.168.0.0", true},
		{"192.168.0.0/16 の末尾", "192.168.255.255", true},
		{"192.168.0.0/16 の直後", "192.169.0.0", false},
		{"198.18.0.0/15 の直前", "198.17.255.255", false},
		{"198.18.0.0/15 の先頭", "198.18.0.0", true},
		{"198.18.0.0/15 の末尾", "198.19.255.255", true},
		{"198.18.0.0/15 の直後", "198.20.0.0", false},
		{"198.51.100.0/24 の直前", "198.51.99.255", false},
		{"198.51.100.0/24 の先頭", "198.51.100.0", true},
		{"198.51.100.0/24 の末尾", "198.51.100.255", true},
		{"198.51.100.0/24 の直後", "198.51.101.0", false},
		{"203.0.113.0/24 の直前", "203.0.112.255", false},
		{"203.0.113.0/24 の先頭", "203.0.113.0", true},
		{"203.0.113.0/24 の末尾", "203.0.113.255", true},
		{"203.0.113.0/24 の直後", "203.0.114.0", false},
		{"224.0.0.0/4 の直前", "223.255.255.255", false},
		{"224.0.0.0/4 の先頭", "224.0.0.0", true},
		{"224.0.0.0/4 の末尾", "239.255.255.255", true},
		{"240.0.0.0/4 の先頭", "240.0.0.0", true},
		{"ブロードキャスト", "255.255.255.255", true},
		{"公開の IPv4", "8.8.8.8", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsBlockedIP(netip.MustParseAddr(test.addr)); got != test.want {
				t.Errorf("IsBlockedIP(%s) = %v, want %v", test.addr, got, test.want)
			}
		})
	}
}

func TestIsBlockedIPv6(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"2000::/3 の直前", "1fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"2000::/3 の先頭", "2000::", false},
		{"2000::/3 の末尾", "3fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"2000::/3 の直後", "4000::", true},
		{"公開の IPv6", "2606:4700:4700::1111", false},

		{"2001::/23 の直前", "2000:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"2001::/23 の先頭", "2001::", true},
		{"2001::/23 の末尾", "2001:1ff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"2001::/23 の直後", "2001:200::", false},
		{"2001:db8::/32 の直前", "2001:db7:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"2001:db8::/32 の先頭", "2001:db8::", true},
		{"2001:db8::/32 の末尾", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"2001:db8::/32 の直後", "2001:db9::", false},
		{"2002::/16 の直前", "2001:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"2002::/16 の先頭", "2002::", true},
		{"2002::/16 の末尾", "2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"2002::/16 の直後", "2003::", false},
		{"3fff::/20 の直前", "3ffe:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"3fff::/20 の先頭", "3fff::", true},
		{"3fff::/20 の末尾", "3fff:fff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"3fff::/20 の直後", "3fff:1000::", false},

		{"未指定アドレス", "::", true},
		{"ループバック", "::1", true},
		{"IPv4 互換のループバック", "::127.0.0.1", true},
		{"IPv4 射影の内部アドレス", "::ffff:127.0.0.1", true},
		{"IPv4 射影の公開アドレス", "::ffff:8.8.8.8", true},
		{"IPv4 射影の末尾", "::ffff:255.255.255.255", true},
		{"IPv4 変換形式の内部アドレス", "::ffff:0:7f00:1", true},
		{"NAT64 の内部アドレス", "64:ff9b::a9fe:a9fe", true},
		{"ローカル用 NAT64", "64:ff9b:1::", true},
		{"破棄用", "100::", true},
		{"ダミー用", "100:0:0:1::1", true},
		{"SRv6", "5f00::1", true},
		{"ユニークローカルの先頭", "fc00::", true},
		{"ユニークローカルの末尾", "fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"リンクローカルの先頭", "fe80::", true},
		{"リンクローカルの末尾", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"サイトローカルの先頭", "fec0::", true},
		{"サイトローカルの末尾", "feff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"マルチキャストの先頭", "ff00::", true},
		{"マルチキャストの末尾", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsBlockedIP(netip.MustParseAddr(test.addr)); got != test.want {
				t.Errorf("IsBlockedIP(%s) = %v, want %v", test.addr, got, test.want)
			}
		})
	}
}

func TestIsBlockedIPZone(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"リンクローカル", "fe80::1%eth0", true},
		{"ループバック", "::1%lo0", true},
		{"IPv4 射影", "::ffff:127.0.0.1%eth0", true},
		{"文書用", "2001:db8::1%eth0", true},
		{"公開アドレス", "2606:4700:4700::1111%eth0", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsBlockedIP(netip.MustParseAddr(test.addr)); got != test.want {
				t.Errorf("IsBlockedIP(%s) = %v, want %v", test.addr, got, test.want)
			}
		})
	}
}

func TestIsBlockedIPInvalid(t *testing.T) {
	if !IsBlockedIP(netip.Addr{}) {
		t.Error("IsBlockedIP(zero value) = false, want true")
	}
}
