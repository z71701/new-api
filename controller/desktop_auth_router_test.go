package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDesktopAuthTestEngine builds a real gin.Engine that mirrors the production
// /api/desktop/auth route chain (DesktopAuthRateLimit + DisableCache +
// AnonymousRequestBodyLimit) and returns the server. The caller must close it.
func newDesktopAuthTestEngine(t *testing.T) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api/desktop/auth")
	group.Use(middleware.DesktopAuthRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit())
	group.POST("/login", DesktopLogin)
	return httptest.NewServer(engine)
}

// newTurnstileMockServer stands in for Cloudflare's siteverify endpoint. The
// caller controls the response status and body; the handler never inspects the
// submitted token.
func newTurnstileMockServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/turnstile/v0/siteverify", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// turnstileMockRoundTripper redirects the Turnstile siteverify POST at
// https://challenges.cloudflare.com to an in-process mock server and passes
// every other request through the original transport.
type turnstileMockRoundTripper struct {
	next     http.RoundTripper
	mockBase string
}

func (r *turnstileMockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "challenges.cloudflare.com" {
		next := r.next
		if next == nil {
			next = http.DefaultTransport
		}
		return next.RoundTrip(req)
	}
	forward := req.Clone(req.Context())
	forward.URL.Scheme = "http"
	forward.URL.Host = strings.TrimPrefix(r.mockBase, "http://")
	forward.Host = ""
	return http.DefaultTransport.RoundTrip(forward)
}

// swapTurnstileTransport points the production Turnstile HTTP client at the
// in-process mock siteverify server and restores the original transport on
// cleanup. It patches whichever client middleware.ValidateTurnstileToken
// actually dials (service.GetHttpClient(), falling back to http.DefaultClient),
// mirroring the production selection. It mutates a process-global transport, so
// callers must not run these tests in parallel.
func swapTurnstileTransport(t *testing.T, mockBase string) {
	t.Helper()
	client := service.GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	original := client.Transport
	client.Transport = &turnstileMockRoundTripper{next: original, mockBase: mockBase}
	t.Cleanup(func() { client.Transport = original })
}

// TestDesktopAcceptanceRouterCaptchaInvalid exercises the real HTTP path: a
// rejected Turnstile token (siteverify returns success:false) must surface as
// 400 AUTH_CAPTCHA_INVALID.
func TestDesktopAcceptanceRouterCaptchaInvalid(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.CriticalRateLimitEnable = false
	common.TurnstileCheckEnabled = true

	server := newTurnstileMockServer(t, http.StatusOK, `{"success":false,"error-codes":["invalid-input-response"]}`)
	swapTurnstileTransport(t, server.URL)

	httpServer := newDesktopAuthTestEngine(t)
	defer httpServer.Close()

	body := desktopAcceptanceLoginBody(t, map[string]any{"captcha_token": "provided-token"})
	resp, err := http.Post(httpServer.URL+"/api/desktop/auth/login", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	recorder := httptest.NewRecorder()
	recorder.Code = resp.StatusCode
	_, _ = recorder.Body.ReadFrom(resp.Body)
	parsed := decodeDesktopAcceptanceResponse(t, recorder)
	assert.False(t, parsed.Success)
	assert.Equal(t, "AUTH_CAPTCHA_INVALID", parsed.Code)
}

// TestDesktopAcceptanceRouterCaptchaUnavailable exercises the real HTTP path: a
// siteverify 5xx response must surface as 503 AUTH_CAPTCHA_UNAVAILABLE.
func TestDesktopAcceptanceRouterCaptchaUnavailable(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.CriticalRateLimitEnable = false
	common.TurnstileCheckEnabled = true

	server := newTurnstileMockServer(t, http.StatusInternalServerError, `{"success":false}`)
	swapTurnstileTransport(t, server.URL)

	httpServer := newDesktopAuthTestEngine(t)
	defer httpServer.Close()

	body := desktopAcceptanceLoginBody(t, map[string]any{"captcha_token": "provided-token"})
	resp, err := http.Post(httpServer.URL+"/api/desktop/auth/login", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	recorder := httptest.NewRecorder()
	recorder.Code = resp.StatusCode
	_, _ = recorder.Body.ReadFrom(resp.Body)
	parsed := decodeDesktopAcceptanceResponse(t, recorder)
	assert.False(t, parsed.Success)
	assert.Equal(t, "AUTH_CAPTCHA_UNAVAILABLE", parsed.Code)
}

// TestDesktopAcceptanceRouterCaptchaPassesThenLoginSucceeds exercises the real
// HTTP path: when siteverify returns success:true, the captcha check passes and
// the login proceeds to issue a 200 OK bundle.
func TestDesktopAcceptanceRouterCaptchaPassesThenLoginSucceeds(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.CriticalRateLimitEnable = false
	common.TurnstileCheckEnabled = true

	server := newTurnstileMockServer(t, http.StatusOK, `{"success":true}`)
	swapTurnstileTransport(t, server.URL)

	httpServer := newDesktopAuthTestEngine(t)
	defer httpServer.Close()

	body := desktopAcceptanceLoginBody(t, map[string]any{"captcha_token": "valid-token"})
	resp, err := http.Post(httpServer.URL+"/api/desktop/auth/login", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	recorder := httptest.NewRecorder()
	recorder.Code = resp.StatusCode
	_, _ = recorder.Body.ReadFrom(resp.Body)
	parsed := decodeDesktopAcceptanceResponse(t, recorder)
	assert.True(t, parsed.Success)
	assert.Equal(t, "OK", parsed.Code)
	bundle := decodeDesktopAcceptanceBundle(t, recorder)
	assert.NotEmpty(t, bundle.AccessToken)
	assert.NotEmpty(t, bundle.RefreshToken)
	assert.Equal(t, "desktop", bundle.Session.ClientType)
}
