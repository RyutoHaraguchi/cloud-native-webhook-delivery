package httpsender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

const (
	defaultTimeout = 30 * time.Second
	// maxDrainBytes は接続を閉じる前に読み捨てる応答本文の上限
	maxDrainBytes = 64 << 10
)

// Config は Sender の設定を表す。
type Config struct {
	AllowedHosts []string      // SSRF_ALLOWED_HOSTS。ホスト名の完全一致（小文字で比べる）
	AllowedPorts []int         // nil なら []int{443}
	Timeout      time.Duration // 0 なら 30 秒
	// LookupIP は名前解決。nil なら net.DefaultResolver を使う。テストで差し替える
	LookupIP func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Sender は SSRF 検査を通して Webhook を送る。
type Sender struct {
	allowedHosts map[string]bool
	allowedPorts []int
	timeout      time.Duration
	lookupIP     func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

var _ core.WebhookSender = (*Sender)(nil)

// New は cfg の既定値を埋めて Sender を作る。
func New(cfg Config) *Sender {
	hosts := make(map[string]bool, len(cfg.AllowedHosts))
	for _, h := range cfg.AllowedHosts {
		hosts[strings.ToLower(h)] = true
	}
	ports := cfg.AllowedPorts
	if ports == nil {
		ports = []int{443}
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	lookup := cfg.LookupIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	return &Sender{
		allowedHosts: hosts,
		allowedPorts: slices.Clone(ports),
		timeout:      timeout,
		lookupIP:     lookup,
	}
}

// Send は req.URL を検査し、検査済みの IP にだけ接続して POST する。
// 応答があればステータスに関係なく nil エラーで返す。
// Timeout は名前解決、接続、応答の受信を合わせた Send 全体の上限になる。
func (s *Sender) Send(ctx context.Context, req core.WebhookRequest) (core.WebhookResponse, error) {
	// 呼び出し側の ctx に期限がなくても、名前解決が止まったまま戻らないことがないようにする
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	u, err := url.Parse(req.URL)
	if err != nil {
		// url.Error は入力の URL 全体（クエリを含む）を持つので、元のエラーは捨てる
		return core.WebhookResponse{}, fmt.Errorf("%w: invalid url", core.ErrSSRFBlocked)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return core.WebhookResponse{}, fmt.Errorf("%w: missing host", core.ErrSSRFBlocked)
	}
	allowed := s.allowedHosts[host]

	port, err := s.checkSchemeAndPort(u, allowed)
	if err != nil {
		return core.WebhookResponse{}, err
	}

	addrs, err := s.lookup(ctx, host)
	if err != nil {
		return core.WebhookResponse{}, err
	}
	if !allowed {
		for _, addr := range addrs {
			if core.IsBlockedIP(addr) {
				return core.WebhookResponse{}, fmt.Errorf("%w: host %s resolves to blocked address %s", core.ErrSSRFBlocked, host, addr)
			}
		}
	}

	return s.do(ctx, u, req, addrs, port, allowed)
}

// checkSchemeAndPort は scheme とポートを検査し、接続するポートを返す。
// 例外のホストは http と https の任意のポートを許す。
func (s *Sender) checkSchemeAndPort(u *url.URL, allowed bool) (string, error) {
	var defaultPort string
	switch u.Scheme {
	case "https":
		defaultPort = "443"
	case "http":
		if !allowed {
			return "", fmt.Errorf("%w: scheme must be https", core.ErrSSRFBlocked)
		}
		defaultPort = "80"
	default:
		return "", fmt.Errorf("%w: unsupported scheme", core.ErrSSRFBlocked)
	}

	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%w: invalid port", core.ErrSSRFBlocked)
	}
	if !allowed && !slices.Contains(s.allowedPorts, n) {
		return "", fmt.Errorf("%w: port %d is not allowed", core.ErrSSRFBlocked, n)
	}
	return port, nil
}

// lookup は接続先の IP を返す。ホストが IP リテラルなら解決せずにその IP を返す。
func (s *Sender) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	// IPv4 射影のリテラルも IsBlockedIP で拒否させるため、Unmap しない
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	return s.resolve(ctx, host)
}

// resolve は host を IPv4 と IPv6 で解決する。片方の失敗は AAAA がないなど通常のことなので、
// もう片方の結果を使う。両方とも 0 件なら一時的なエラー（ErrSSRFBlocked ではない）を返す。
func (s *Sender) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	v4, err4 := s.lookupIP(ctx, "ip4", host)
	v6, err6 := s.lookupIP(ctx, "ip6", host)

	addrs := make([]netip.Addr, 0, len(v4)+len(v6))
	if err4 == nil {
		// ip4 の結果は 16 バイト形式で返りうる。射影のままだと公開アドレスも拒否されるので戻す
		for _, a := range v4 {
			addrs = append(addrs, a.Unmap())
		}
	}
	if err6 == nil {
		// AAAA の射影アドレスは IsBlockedIP で拒否させるため、そのまま残す
		addrs = append(addrs, v6...)
	}
	if len(addrs) == 0 {
		if err := errors.Join(err4, err6); err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	return addrs, nil
}

// do は addrs にだけ接続する Transport を作って 1 回送る。
// 接続先を検査済みの IP に固定するため、Transport はリクエストごとに作り、接続を使い回さない。
func (s *Sender) do(ctx context.Context, u *url.URL, req core.WebhookRequest, addrs []netip.Addr, port string, allowed bool) (core.WebhookResponse, error) {
	dialer := &net.Dialer{}
	if !allowed {
		// 多層防御として、接続直前のアドレスも検査する
		dialer.Control = controlBlockedIP
	}
	transport := &http.Transport{
		Proxy:             nil,
		DialContext:       dialAddrs(dialer, addrs, port),
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{
		// 期限は Send で ctx に付けている
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(req.Body))
	if err != nil {
		// エラーは URL 全体を含みうるので、元のエラーは捨てる
		return core.WebhookResponse{}, errors.New("build webhook request")
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		// url.Error は URL 全体（クエリを含む）を持つので、中のエラーだけを残す
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return core.WebhookResponse{}, fmt.Errorf("send webhook: %w", err)
	}
	defer func() {
		// 応答は受け取り済みで、閉じる失敗は結果に影響しない
		_ = resp.Body.Close()
	}()
	// 本文は使わない。上限まで読み捨てる。読めなくても応答の結果は変わらないので無視する
	_, _ = io.CopyN(io.Discard, resp.Body, maxDrainBytes)

	return core.WebhookResponse{
		StatusCode: resp.StatusCode,
		RetryAfter: resp.Header.Get("Retry-After"),
	}, nil
}

// dialAddrs は、要求されたアドレスに関係なく addrs の port にだけ順に接続する DialContext を返す。
func dialAddrs(dialer *net.Dialer, addrs []netip.Addr, port string) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var errs []error
		for _, addr := range addrs {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
			if err == nil {
				return conn, nil
			}
			errs = append(errs, err)
		}
		return nil, fmt.Errorf("dial: %w", errors.Join(errs...))
	}
}

// controlBlockedIP は接続直前のアドレスが拒否範囲なら接続を止める。
func controlBlockedIP(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparsable dial address", core.ErrSSRFBlocked)
	}
	if core.IsBlockedIP(ap.Addr()) {
		return fmt.Errorf("%w: dial to blocked address %s", core.ErrSSRFBlocked, ap.Addr())
	}
	return nil
}
