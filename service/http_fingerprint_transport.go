package service

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// chromeFingerprintRoundTripper sends HTTPS requests with a Chrome TLS
// fingerprint (utls HelloChrome_Auto), for upstreams that reject the default
// Go ClientHello. The HTTP version follows ALPN. Each request uses a
// dedicated connection closed with the response body, matching the
// CLIProxyAPI codex transport behavior.
type chromeFingerprintRoundTripper struct {
	dialer proxy.Dialer
}

// closeConnectionBody closes the dedicated TLS connection together with the
// response body so no fingerprinted connection is reused for another request.
type closeConnectionBody struct {
	io.ReadCloser
	closeConnection func() error
	once            sync.Once
	err             error
}

func (b *closeConnectionBody) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		var errConn error
		if b.closeConnection != nil {
			errConn = b.closeConnection()
		}
		var errBody error
		if b.ReadCloser != nil {
			errBody = b.ReadCloser.Close()
		}
		b.err = errors.Join(errBody, errConn)
	})
	return b.err
}

// chromeFingerprintEnvProxyURL resolves the environment proxy with the same
// semantics as the standard transport (HTTP_PROXY/HTTPS_PROXY/NO_PROXY, via
// x/net/http/httpproxy). net/http.ProxyFromEnvironment caches the environment
// in a package sync.Once, and proxy.FromURL's env variant only reads
// ALL_PROXY — neither matches what the standard relay transport does. The
// probe URL is fixed because the fingerprint transport only serves
// https://chatgpt.com.
var chromeFingerprintEnvProxyURL = func() *url.URL {
	proxyFunc := httpproxy.FromEnvironment().ProxyFunc()
	proxyURL, err := proxyFunc(&url.URL{Scheme: "https", Host: "chatgpt.com"})
	if err != nil || proxyURL == nil {
		return nil
	}
	return proxyURL
}

// chromeFingerprintDialer resolves the dialer for the fingerprint transport:
// channel proxy > environment proxy (HTTP_PROXY/HTTPS_PROXY/NO_PROXY, which
// the standard transport honors and a bare proxy.Direct dial would silently
// drop) > direct. The result is always wrapped with a dial deadline so a
// blackholed SYN cannot stall the relay. Invalid URLs fall back to direct.
func newChromeFingerprintDialer(rawProxyURL string) proxy.Dialer {
	var dialer proxy.Dialer = proxy.Direct
	trimmed := strings.TrimSpace(rawProxyURL)
	if trimmed == "" {
		if envURL := chromeFingerprintEnvProxyURL(); envURL != nil {
			dialer = dialerFromProxyURL(envURL)
		}
	} else if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		dialer = dialerFromProxyURL(parsed)
	}
	return boundedDialer{dialer: dialer}
}

func dialerFromProxyURL(parsed *url.URL) proxy.Dialer {
	switch parsed.Scheme {
	case "http", "https":
		return &httpConnectProxyDialer{proxyURL: parsed}
	case "socks5", "socks5h":
		if dialer, err := proxy.FromURL(parsed, proxy.Direct); err == nil {
			return dialer
		}
	}
	return proxy.Direct
}

// httpConnectProxyDialer tunnels TCP through an http/https proxy via CONNECT,
// so the Chrome ClientHello still originates from this process. The TLS
// handshake to an https proxy uses the standard Go stack; only the payload
// connection carries the fingerprint.
type httpConnectProxyDialer struct {
	proxyURL *url.URL
}

func (d *httpConnectProxyDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *httpConnectProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	proxyAddr := d.proxyURL.Host
	if _, _, err := net.SplitHostPort(proxyAddr); err != nil {
		if d.proxyURL.Scheme == "https" {
			proxyAddr = net.JoinHostPort(proxyAddr, "443")
		} else {
			proxyAddr = net.JoinHostPort(proxyAddr, "80")
		}
	}

	var conn net.Conn
	if d.proxyURL.Scheme == "https" {
		tlsConn, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, network, proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("chrome tls: dial https proxy: %w", err)
		}
		proxied := tls.Client(tlsConn, &tls.Config{ServerName: d.proxyURL.Hostname(), NextProtos: []string{"http/1.1"}})
		if err := proxied.HandshakeContext(ctx); err != nil {
			_ = tlsConn.Close()
			return nil, fmt.Errorf("chrome tls: https proxy TLS handshake: %w", err)
		}
		conn = proxied
	} else {
		var err error
		conn, err = (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, network, proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("chrome tls: dial http proxy: %w", err)
		}
	}

	connectReq := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	if user := d.proxyURL.User; user != nil {
		password, _ := user.Password()
		connectReq.Header.Set("Proxy-Authorization", "Basic "+basicProxyAuth(user.Username(), password))
	}
	if err := connectReq.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("chrome tls: proxy CONNECT: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), connectReq)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("chrome tls: proxy CONNECT: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("chrome tls: proxy CONNECT status: %s", resp.Status)
	}
	resp.Body = http.NoBody
	return conn, nil
}

func basicProxyAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

// chromeDialTimeout bounds the TCP dial for fingerprinted connections. A
// blackholed SYN otherwise hangs for the kernel SYN-retry duration (~130s on
// Linux), stalling the relay attempt far beyond any useful retry window.
const chromeDialTimeout = 15 * time.Second

// chromeHandshakeTimeout bounds the TLS handshake after the TCP connect.
const chromeHandshakeTimeout = 15 * time.Second

// boundedDialer wraps a proxy.Dialer with a dial deadline. proxy.Direct has
// none, and env-provided dialers may be unbounded as well.
type boundedDialer struct {
	dialer proxy.Dialer
}

func (b boundedDialer) Dial(network, addr string) (net.Conn, error) {
	return b.DialContext(context.Background(), network, addr)
}

func (b boundedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, chromeDialTimeout)
	defer cancel()
	// Every dialer this package constructs (proxy.Direct, FromEnvironment's
	// PerHost, socks5, httpConnectProxyDialer) implements ContextDialer; the
	// plain-Dial call only covers exotic external dialers.
	if contextDialer, ok := b.dialer.(proxy.ContextDialer); ok {
		return contextDialer.DialContext(ctx, network, addr)
	}
	return b.dialer.Dial(network, addr)
}

func (t *chromeFingerprintRoundTripper) createConnection(ctx context.Context, host, addr string, alpn []string) (*utls.UConn, error) {
	// t.dialer is already deadline-bounded by newChromeFingerprintDialer;
	// this also covers round trippers built directly in tests.
	dialer := t.dialer
	if _, bounded := dialer.(boundedDialer); !bounded {
		dialer = boundedDialer{dialer: dialer}
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("chrome tls: dialer does not support context cancellation")
	}
	conn, errDial := contextDialer.DialContext(ctx, "tcp", addr)
	if errDial != nil {
		return nil, fmt.Errorf("chrome tls: dial upstream: %w", errDial)
	}

	tlsConfig := &utls.Config{ServerName: host}
	if len(alpn) > 0 {
		tlsConfig.NextProtos = alpn
	}
	tlsConn := utls.UClient(conn, tlsConfig, utls.HelloChrome_Auto)
	// Bound the handshake too: a connected-but-blackholed peer parks the
	// handshake forever without its own deadline. The conn deadline is
	// cleared after a successful handshake; stream reads/writes afterwards
	// follow the request context, not this timeout.
	_ = conn.SetDeadline(time.Now().Add(chromeHandshakeTimeout))
	if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("chrome tls: TLS handshake: %w; close connection: %v", errHandshake, errClose)
		}
		return nil, fmt.Errorf("chrome tls: TLS handshake: %w", errHandshake)
	}
	_ = conn.SetDeadline(time.Time{})
	return tlsConn, nil
}

func (t *chromeFingerprintRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	tlsConn, err := t.createConnection(req.Context(), hostname, net.JoinHostPort(hostname, port), nil)
	if err != nil {
		return nil, err
	}
	return roundTripChromeConnection(req, tlsConn)
}

// roundTripChromeConnection selects the HTTP protocol per ALPN: "h2" rides a
// dedicated HTTP/2 connection; empty ALPN (a TLS-inspecting proxy omitted it)
// or "http/1.1" reuses the handshaken connection via a non-pooling transport.
func roundTripChromeConnection(req *http.Request, tlsConn *utls.UConn) (*http.Response, error) {
	closeConnection := func() error {
		if errClose := tlsConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			return errClose
		}
		return nil
	}
	var resp *http.Response
	var err error
	switch protocol := tlsConn.ConnectionState().NegotiatedProtocol; protocol {
	case "h2":
		h2Conn, errClientConn := (&http2.Transport{}).NewClientConn(tlsConn)
		if errClientConn != nil {
			err = fmt.Errorf("chrome tls: initialize HTTP/2 connection: %w", errClientConn)
			break
		}
		closeConnection = h2Conn.Close
		resp, err = h2Conn.RoundTrip(req)
	case "", "http/1.1":
		transport := &http.Transport{
			DisableKeepAlives: true,
			DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
				return tlsConn, nil
			},
		}
		resp, err = transport.RoundTrip(req)
		transport.CloseIdleConnections()
	default:
		err = fmt.Errorf("chrome tls: unsupported negotiated protocol %q", protocol)
	}
	if err != nil {
		_ = closeConnection()
		return nil, err
	}
	if resp == nil {
		_ = closeConnection()
		return nil, errors.New("chrome tls: upstream returned an empty response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = &closeConnectionBody{
		ReadCloser:      resp.Body,
		closeConnection: closeConnection,
	}
	return resp, nil
}

// fingerprintFallbackRoundTripper routes HTTPS requests to chatgpt.com
// through the Chrome fingerprint transport and everything else through the
// standard relay transport.
type fingerprintFallbackRoundTripper struct {
	chrome   http.RoundTripper
	fallback http.RoundTripper
}

// UsesChromeTLSFingerprint reports whether the URL should use the Chrome TLS
// fingerprint transport: HTTPS to chatgpt.com exactly (no subdomains), the
// host the Codex chatgpt.com backend serves.
func UsesChromeTLSFingerprint(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "chatgpt.com")
}

func (f *fingerprintFallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if UsesChromeTLSFingerprint(req.URL) {
		return f.chrome.RoundTrip(req)
	}
	return f.fallback.RoundTrip(req)
}

var chromeFingerprintClients sync.Map // proxy cache key -> *http.Client

// GetChromeFingerprintHTTPClient returns a cached HTTP client that applies
// the Chrome TLS fingerprint to chatgpt.com HTTPS requests and uses the
// standard relay transport for other hosts.
func GetChromeFingerprintHTTPClient(rawProxyURL string) *http.Client {
	cacheKey := strings.TrimSpace(rawProxyURL)
	if cached, ok := chromeFingerprintClients.Load(cacheKey); ok {
		return cached.(*http.Client)
	}
	client := &http.Client{
		Transport: &fingerprintFallbackRoundTripper{
			chrome:   &chromeFingerprintRoundTripper{dialer: newChromeFingerprintDialer(cacheKey)},
			fallback: newRelayHTTPTransport(),
		},
	}
	actual, _ := chromeFingerprintClients.LoadOrStore(cacheKey, client)
	return actual.(*http.Client)
}

// DialChromeFingerprintTLS dials addr with the Chrome TLS fingerprint and an
// http/1.1-only ALPN, returning the handshaken connection for the websocket
// upgrade against chatgpt.com.
func DialChromeFingerprintTLS(ctx context.Context, rawProxyURL, addr string) (net.Conn, error) {
	hostname, _, errSplit := net.SplitHostPort(addr)
	if errSplit != nil {
		return nil, fmt.Errorf("chrome tls: split address: %w", errSplit)
	}
	dialer := newChromeFingerprintDialer(rawProxyURL)
	rt := &chromeFingerprintRoundTripper{dialer: dialer}
	conn, err := rt.createConnection(ctx, hostname, addr, []string{"http/1.1"})
	if err != nil {
		return nil, err
	}
	if negotiated := conn.ConnectionState().NegotiatedProtocol; negotiated != "http/1.1" && negotiated != "" {
		_ = conn.Close()
		return nil, fmt.Errorf("chrome tls: websocket requires http/1.1, negotiated %q", negotiated)
	}
	return conn, nil
}
