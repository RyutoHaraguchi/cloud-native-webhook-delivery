package httpsender

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

var _ core.WebhookSender = (*Sender)(nil)

// lookupFunc は network ごとに決めた結果を返す名前解決を作る。呼ばれた回数を calls に数える。
func lookupFunc(calls *atomic.Int32, results map[string][]netip.Addr, errs map[string]error) func(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return func(_ context.Context, network, _ string) ([]netip.Addr, error) {
		if calls != nil {
			calls.Add(1)
		}
		if err := errs[network]; err != nil {
			return nil, err
		}
		return results[network], nil
	}
}

// unreachableServer は呼ばれたらテストを失敗させる受信側を作る。
func unreachableServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler must not be called")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func serverPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	return u.Port()
}

func TestSender_AllowedHostSendsHTTP(t *testing.T) {
	var (
		gotMethod  string
		gotBody    string
		gotHeaders http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBody = string(b)
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	s := New(Config{AllowedHosts: []string{"127.0.0.1"}})
	headers := map[string]string{
		"Content-Type":      "application/json",
		"webhook-id":        "msg_1",
		"webhook-timestamp": "1700000000",
		"webhook-signature": "v1,abc",
		"X-Custom":          "value",
	}
	resp, err := s.Send(context.Background(), core.WebhookRequest{
		URL:     srv.URL + "/hook",
		Headers: headers,
		Body:    []byte(`{"a":1}`),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("StatusCode = %d, want 201", resp.StatusCode)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody != `{"a":1}` {
		t.Errorf("body = %q", gotBody)
	}
	for k, v := range headers {
		if got := gotHeaders.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

func TestSender_ReturnsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	s := New(Config{AllowedHosts: []string{"127.0.0.1"}})
	resp, err := s.Send(context.Background(), core.WebhookRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := core.WebhookResponse{StatusCode: http.StatusServiceUnavailable, RetryAfter: "120"}
	if resp != want {
		t.Errorf("resp = %+v, want %+v", resp, want)
	}
}

func TestSender_DoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/other", http.StatusFound)
	})
	mux.HandleFunc("/other", func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s := New(Config{AllowedHosts: []string{"127.0.0.1"}})
	resp, err := s.Send(context.Background(), core.WebhookRequest{URL: srv.URL + "/hook"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Errorf("StatusCode = %d, want 302", resp.StatusCode)
	}
	if redirected.Load() {
		t.Error("redirect was followed")
	}
}

func TestSender_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Close が待たされないよう、切断でも抜ける
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	s := New(Config{AllowedHosts: []string{"127.0.0.1"}, Timeout: 50 * time.Millisecond})
	_, err := s.Send(context.Background(), core.WebhookRequest{URL: srv.URL})
	if err == nil {
		t.Fatal("Send: want error, got nil")
	}
	if errors.Is(err, core.ErrSSRFBlocked) {
		t.Errorf("err = %v, must not be ErrSSRFBlocked", err)
	}
}

func TestSender_BlocksPrivateResolution(t *testing.T) {
	srv := unreachableServer(t)
	s := New(Config{
		AllowedPorts: []int{443, mustAtoi(t, serverPort(t, srv))},
		LookupIP: lookupFunc(nil, map[string][]netip.Addr{
			"ip4": {netip.MustParseAddr("127.0.0.1")},
		}, nil),
	})

	for _, rawURL := range []string{
		"https://hooks.example.test/",
		// 解決した 127.0.0.1 に届く URL でも、接続しない
		"https://hooks.example.test:" + serverPort(t, srv) + "/",
	} {
		_, err := s.Send(context.Background(), core.WebhookRequest{URL: rawURL})
		if !errors.Is(err, core.ErrSSRFBlocked) {
			t.Errorf("Send(%s) err = %v, want ErrSSRFBlocked", rawURL, err)
		}
	}
}

func TestSender_BlocksMixedResolution(t *testing.T) {
	s := New(Config{
		LookupIP: lookupFunc(nil, map[string][]netip.Addr{
			"ip4": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")},
		}, nil),
	})
	_, err := s.Send(context.Background(), core.WebhookRequest{URL: "https://hooks.example.test/"})
	if !errors.Is(err, core.ErrSSRFBlocked) {
		t.Errorf("err = %v, want ErrSSRFBlocked", err)
	}
}

func TestSender_BlocksMappedAAAA(t *testing.T) {
	s := New(Config{
		LookupIP: lookupFunc(nil, map[string][]netip.Addr{
			"ip6": {netip.MustParseAddr("::ffff:8.8.8.8")},
		}, nil),
	})
	_, err := s.Send(context.Background(), core.WebhookRequest{URL: "https://hooks.example.test/"})
	if !errors.Is(err, core.ErrSSRFBlocked) {
		t.Errorf("err = %v, want ErrSSRFBlocked", err)
	}
}

func TestSender_BlocksIPLiteral(t *testing.T) {
	var calls atomic.Int32
	s := New(Config{LookupIP: lookupFunc(&calls, nil, nil)})
	for _, rawURL := range []string{
		"https://127.0.0.1/",
		"https://[::1]/",
		"https://[::ffff:8.8.8.8]/",
	} {
		_, err := s.Send(context.Background(), core.WebhookRequest{URL: rawURL})
		if !errors.Is(err, core.ErrSSRFBlocked) {
			t.Errorf("Send(%s) err = %v, want ErrSSRFBlocked", rawURL, err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("LookupIP called %d times for IP literals, want 0", n)
	}
}

func TestSender_LookupUnmapsIPv4(t *testing.T) {
	mapped := netip.AddrFrom16(netip.MustParseAddr("::ffff:8.8.8.8").As16())

	t.Run("ip4 is unmapped", func(t *testing.T) {
		s := New(Config{LookupIP: lookupFunc(nil, map[string][]netip.Addr{"ip4": {mapped}}, nil)})
		got, err := s.resolve(context.Background(), "hooks.example.test")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(got) != 1 || !got[0].Is4() || got[0] != netip.MustParseAddr("8.8.8.8") {
			t.Fatalf("resolve = %v, want [8.8.8.8] as Is4", got)
		}
		if core.IsBlockedIP(got[0]) {
			t.Error("IsBlockedIP(8.8.8.8) = true, want false")
		}
	})

	t.Run("ip6 stays mapped", func(t *testing.T) {
		s := New(Config{LookupIP: lookupFunc(nil, map[string][]netip.Addr{"ip6": {mapped}}, nil)})
		got, err := s.resolve(context.Background(), "hooks.example.test")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(got) != 1 || !got[0].Is4In6() {
			t.Fatalf("resolve = %v, want [::ffff:8.8.8.8]", got)
		}
		if !core.IsBlockedIP(got[0]) {
			t.Error("IsBlockedIP(::ffff:8.8.8.8) = false, want true")
		}
	})
}

func TestSender_ResolveFamilies(t *testing.T) {
	v4 := netip.MustParseAddr("8.8.8.8")
	v6 := netip.MustParseAddr("2606:4700::1111")
	lookupErr := errors.New("lookup failed")

	tests := []struct {
		name    string
		results map[string][]netip.Addr
		errs    map[string]error
		want    []netip.Addr
		wantErr bool
	}{
		{name: "both", results: map[string][]netip.Addr{"ip4": {v4}, "ip6": {v6}}, want: []netip.Addr{v4, v6}},
		{name: "ip6 error is ignored", results: map[string][]netip.Addr{"ip4": {v4}}, errs: map[string]error{"ip6": lookupErr}, want: []netip.Addr{v4}},
		{name: "ip4 error is ignored", results: map[string][]netip.Addr{"ip6": {v6}}, errs: map[string]error{"ip4": lookupErr}, want: []netip.Addr{v6}},
		{name: "both empty", wantErr: true},
		{name: "one error other empty", errs: map[string]error{"ip4": lookupErr}, wantErr: true},
		{name: "both error", errs: map[string]error{"ip4": lookupErr, "ip6": lookupErr}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(Config{LookupIP: lookupFunc(nil, tt.results, tt.errs)})
			got, err := s.resolve(context.Background(), "hooks.example.test")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolve = %v, want error", got)
				}
				if errors.Is(err, core.ErrSSRFBlocked) {
					t.Errorf("err = %v, must not be ErrSSRFBlocked", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("resolve = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("resolve[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSender_RejectsSchemeAndPort(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		wantBlocked bool
		wantLookup  bool
	}{
		{name: "http", url: "http://hooks.example.test/", wantBlocked: true},
		{name: "port 8443", url: "https://hooks.example.test:8443/", wantBlocked: true},
		{name: "port 22", url: "https://hooks.example.test:22/", wantBlocked: true},
		{name: "unparsable", url: "://bad", wantBlocked: true},
		{name: "ftp", url: "ftp://hooks.example.test/", wantBlocked: true},
		{name: "empty host", url: "https:///path", wantBlocked: true},
		// 名前解決で止める。443 の明示は scheme とポートの検査を通る
		{name: "explicit 443", url: "https://hooks.example.test:443/", wantBlocked: true, wantLookup: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			s := New(Config{LookupIP: lookupFunc(&calls, map[string][]netip.Addr{
				"ip4": {netip.MustParseAddr("10.0.0.1")},
			}, nil)})
			_, err := s.Send(context.Background(), core.WebhookRequest{URL: tt.url})
			if got := errors.Is(err, core.ErrSSRFBlocked); got != tt.wantBlocked {
				t.Errorf("err = %v, blocked = %v, want %v", err, got, tt.wantBlocked)
			}
			if got := calls.Load() > 0; got != tt.wantLookup {
				t.Errorf("LookupIP called = %v, want %v", got, tt.wantLookup)
			}
		})
	}
}

func TestSender_AllowedHostMatchesHostnameOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	port := serverPort(t, srv)

	t.Run("hostname matches", func(t *testing.T) {
		s := New(Config{AllowedHosts: []string{"127.0.0.1"}})
		resp, err := s.Send(context.Background(), core.WebhookRequest{URL: "http://127.0.0.1:" + port + "/"})
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("StatusCode = %d, want 204", resp.StatusCode)
		}
	})

	t.Run("host with port does not match", func(t *testing.T) {
		s := New(Config{AllowedHosts: []string{"127.0.0.1:" + port}})
		_, err := s.Send(context.Background(), core.WebhookRequest{URL: "http://127.0.0.1:" + port + "/"})
		if !errors.Is(err, core.ErrSSRFBlocked) {
			t.Errorf("err = %v, want ErrSSRFBlocked", err)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		s := New(Config{
			AllowedHosts: []string{"Dummy-Receiver"},
			LookupIP: lookupFunc(nil, map[string][]netip.Addr{
				"ip4": {netip.MustParseAddr("127.0.0.1")},
			}, nil),
		})
		resp, err := s.Send(context.Background(), core.WebhookRequest{URL: "http://dummy-receiver:" + port + "/"})
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("StatusCode = %d, want 204", resp.StatusCode)
		}
	})
}

func TestSender_DNSFailureIsTransient(t *testing.T) {
	s := New(Config{LookupIP: lookupFunc(nil, nil, map[string]error{
		"ip4": errors.New("no such host"),
		"ip6": errors.New("no such host"),
	})})
	_, err := s.Send(context.Background(), core.WebhookRequest{URL: "https://hooks.example.test/"})
	if err == nil {
		t.Fatal("Send: want error, got nil")
	}
	if errors.Is(err, core.ErrSSRFBlocked) {
		t.Errorf("err = %v, must not be ErrSSRFBlocked", err)
	}
}

func TestSender_ErrorOmitsQuery(t *testing.T) {
	const secret = "token=whsec_sensitive"
	s := New(Config{LookupIP: lookupFunc(nil, map[string][]netip.Addr{
		"ip4": {netip.MustParseAddr("10.0.0.1")},
	}, nil)})
	for _, rawURL := range []string{
		"http://hooks.example.test/?" + secret,
		"https://hooks.example.test/?" + secret,
		"https://hooks.example.test:bad/?" + secret,
	} {
		_, err := s.Send(context.Background(), core.WebhookRequest{URL: rawURL})
		if err == nil {
			t.Fatalf("Send(%s): want error", rawURL)
		}
		if strings.Contains(err.Error(), "whsec_sensitive") {
			t.Errorf("err = %q contains query", err)
		}
	}
}

func TestSender_DialControlBlocksIP(t *testing.T) {
	srv := unreachableServer(t)
	port := serverPort(t, srv)

	// 事前の検査をすり抜けた場合でも、Control が接続直前に止める
	dial := dialAddrs(&net.Dialer{Control: controlBlockedIP}, []netip.Addr{netip.MustParseAddr("127.0.0.1")}, port)
	conn, err := dial(context.Background(), "tcp", "hooks.example.test:"+port)
	if err == nil {
		_ = conn.Close()
		t.Fatal("dial: want error, got nil")
	}
	if !errors.Is(err, core.ErrSSRFBlocked) {
		t.Errorf("err = %v, want ErrSSRFBlocked", err)
	}

	if err := controlBlockedIP("tcp4", "8.8.8.8:443", nil); err != nil {
		t.Errorf("controlBlockedIP(8.8.8.8) = %v, want nil", err)
	}
	if err := controlBlockedIP("tcp6", "[::ffff:8.8.8.8]:443", nil); !errors.Is(err, core.ErrSSRFBlocked) {
		t.Errorf("controlBlockedIP(::ffff:8.8.8.8) = %v, want ErrSSRFBlocked", err)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("atoi %q: %v", s, err)
	}
	return n
}
