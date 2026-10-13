package service

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

// captureClientHello listens on a local port and returns the digest of the
// first TLS ClientHello record, plus the listener address.
func captureClientHello(t *testing.T) (string, chan string) {
	t.Helper()
	digests := make(chan string, 4)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				record := make([]byte, 1024)
				n, err := conn.Read(record)
				if err != nil || n == 0 {
					return
				}
				digest := sha256.Sum256(record[:n])
				digests <- hex.EncodeToString(digest[:8])
			}(conn)
		}
	}()
	return listener.Addr().String(), digests
}

// The Chrome fingerprint transport must send a utls Chrome ClientHello, which
// differs from the ClientHello the standard Go transport emits.
func TestChromeFingerprintClientHelloDiffersFromGoDefault(t *testing.T) {
	addr, digests := captureClientHello(t)

	rt := &chromeFingerprintRoundTripper{dialer: newChromeFingerprintDialer("")}
	host, _, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	_, err = rt.createConnection(context.Background(), host, addr, nil)
	// The capture server never completes the handshake; the ClientHello has
	// already been written and observed by then.
	digestChrome := <-digests
	if err == nil {
		t.Fatal("expected handshake failure against the capture server")
	}
	require.NotEmpty(t, digestChrome)

	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+addr+"/", nil)
		if err == nil {
			_, _ = (&http.Transport{}).RoundTrip(req)
		}
	}()
	digestGo := <-digests
	require.NotEmpty(t, digestGo)
	assert.NotEqual(t, digestChrome, digestGo, "Chrome fingerprint ClientHello must differ from the Go default")
}

// The Chrome fingerprint only applies to https chatgpt.com exactly.
func TestUsesChromeTLSFingerprint(t *testing.T) {
	chatgpt, err := url.Parse("https://chatgpt.com/backend-api/codex/responses")
	require.NoError(t, err)
	assert.True(t, UsesChromeTLSFingerprint(chatgpt))

	sub, err := url.Parse("https://api.chatgpt.com/x")
	require.NoError(t, err)
	assert.False(t, UsesChromeTLSFingerprint(sub))

	other, err := url.Parse("https://api.openai.com/v1/responses")
	require.NoError(t, err)
	assert.False(t, UsesChromeTLSFingerprint(other))

	httpURL, err := url.Parse("http://chatgpt.com/x")
	require.NoError(t, err)
	assert.False(t, UsesChromeTLSFingerprint(httpURL))

	assert.False(t, UsesChromeTLSFingerprint(nil))

	_ = utls.HelloChrome_Auto
}

// A blackholed dial must fail within the bounded timeout instead of hanging
// for the kernel SYN-retry duration (~130s), which previously turned one
// relay attempt into three 135s stalls.
func TestChromeFingerprintDialTimeoutBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the dial timeout")
	}
	// TEST-NET-3 (240.0.0.0/12) is never routed; the SYN gets no answer.
	rt := &chromeFingerprintRoundTripper{dialer: boundedDialer{dialer: newChromeFingerprintDialer("")}}
	start := time.Now()
	_, err := rt.createConnection(context.Background(), "chatgpt.com", "240.0.0.1:443", nil)
	require.Error(t, err)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 60*time.Second, "dial must fail within the bounded timeout")
	assert.ErrorContains(t, err, "dial upstream")
}

// startConnectProbe listens once and reports whether an HTTP CONNECT arrives.
func startConnectProbe(t *testing.T) (proxyURL string, gotConnect chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	gotConnect = make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		gotConnect <- request.Method + " " + request.URL.Host
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		time.Sleep(50 * time.Millisecond)
	}()
	return "http://" + listener.Addr().String(), gotConnect
}

// The fingerprint dialer must honor environment proxies when the channel has
// none configured: the standard transport honors HTTP_PROXY/HTTPS_PROXY via
// http.ProxyFromEnvironment, and silently dropping them broke deployments
// that egress through an env proxy (all codex requests stalled ~135s).
// proxy.FromEnvironment would NOT cover this: it only reads ALL_PROXY.
func TestChromeFingerprintDialerHonorsEnvProxy(t *testing.T) {
	probeURL, gotConnect := startConnectProbe(t)
	t.Setenv("HTTPS_PROXY", probeURL)
	t.Setenv("https_proxy", probeURL)
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	dialer := newChromeFingerprintDialer("")
	require.NotNil(t, dialer)

	conn, err := dialer.(proxy.ContextDialer).DialContext(context.Background(), "tcp", "chatgpt.com:443")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	select {
	case connect := <-gotConnect:
		assert.Equal(t, "CONNECT chatgpt.com:443", connect, "dial must tunnel through the env proxy")
	case <-time.After(5 * time.Second):
		t.Fatal("no CONNECT reached the env proxy; dialer bypassed it")
	}
}

// A channel proxy takes precedence over the environment proxy.
func TestChromeFingerprintDialerChannelProxyWins(t *testing.T) {
	probeURL, gotConnect := startConnectProbe(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("NO_PROXY", "")

	dialer := newChromeFingerprintDialer(probeURL)
	require.NotNil(t, dialer)
	conn, err := dialer.(proxy.ContextDialer).DialContext(context.Background(), "tcp", "chatgpt.com:443")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	select {
	case connect := <-gotConnect:
		assert.Equal(t, "CONNECT chatgpt.com:443", connect)
	case <-time.After(5 * time.Second):
		t.Fatal("no CONNECT reached the channel proxy; dialer bypassed it")
	}
}

// NO_PROXY must keep a fingerprinted host on the direct path, matching the
// standard transport semantics.
func TestChromeFingerprintDialerNoProxyBypasses(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the dial timeout")
	}
	probeURL, gotConnect := startConnectProbe(t)
	t.Setenv("HTTPS_PROXY", probeURL)
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("NO_PROXY", "chatgpt.com")

	dialer := newChromeFingerprintDialer("")
	// A proxied dial would hit the local probe instantly; a direct dial to
	// unrouted TEST-NET-3 times out instead, proving NO_PROXY was honored.
	start := time.Now()
	_, err := dialer.(proxy.ContextDialer).DialContext(context.Background(), "tcp", "240.0.0.1:443")
	require.Error(t, err)
	select {
	case <-gotConnect:
		t.Fatal("dial tunneled through the proxy despite NO_PROXY")
	default:
	}
	assert.Less(t, time.Since(start), 60*time.Second, "direct dial must still be bounded")
}
