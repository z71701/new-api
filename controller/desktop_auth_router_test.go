package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
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

// TestDesktopAcceptanceRouterCaptchaInvalid exercises the real HTTP path: a
// rejected Turnstile token must surface as 400 AUTH_CAPTCHA_INVALID.
func TestDesktopAcceptanceRouterCaptchaInvalid(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.CriticalRateLimitEnable = false
	common.TurnstileCheckEnabled = true

	restore := middleware.SetTurnstileVerifierForTest(func(response, remoteIP string) error {
		return middleware.ErrTurnstileRejected
	})
	t.Cleanup(restore)

	server := newDesktopAuthTestEngine(t)
	defer server.Close()

	body := desktopAcceptanceLoginBody(t, map[string]any{"captcha_token": "provided-token"})
	resp, err := http.Post(server.URL+"/api/desktop/auth/login", "application/json", strings.NewReader(body))
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
// Turnstile network/transport failure must surface as 503 AUTH_CAPTCHA_UNAVAILABLE.
func TestDesktopAcceptanceRouterCaptchaUnavailable(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.CriticalRateLimitEnable = false
	common.TurnstileCheckEnabled = true

	networkErr := errors.New("dial tcp challenges.cloudflare.com: connect: connection refused")
	restore := middleware.SetTurnstileVerifierForTest(func(response, remoteIP string) error {
		return networkErr
	})
	t.Cleanup(restore)

	server := newDesktopAuthTestEngine(t)
	defer server.Close()

	body := desktopAcceptanceLoginBody(t, map[string]any{"captcha_token": "provided-token"})
	resp, err := http.Post(server.URL+"/api/desktop/auth/login", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	recorder := httptest.NewRecorder()
	recorder.Code = resp.StatusCode
	recorder.Body.ReadFrom(resp.Body)
	parsed := decodeDesktopAcceptanceResponse(t, recorder)
	assert.False(t, parsed.Success)
	assert.Equal(t, "AUTH_CAPTCHA_UNAVAILABLE", parsed.Code)
}

// TestDesktopAcceptanceRouterCaptchaPassesThenLoginSucceeds exercises the real
// HTTP path: when the Turnstile verifier returns nil, the login proceeds and
// returns a 200 OK bundle.
func TestDesktopAcceptanceRouterCaptchaPassesThenLoginSucceeds(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.CriticalRateLimitEnable = false
	common.TurnstileCheckEnabled = true

	restore := middleware.SetTurnstileVerifierForTest(func(response, remoteIP string) error {
		return nil
	})
	t.Cleanup(restore)

	server := newDesktopAuthTestEngine(t)
	defer server.Close()

	body := desktopAcceptanceLoginBody(t, map[string]any{"captcha_token": "valid-token"})
	resp, err := http.Post(server.URL+"/api/desktop/auth/login", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	recorder := httptest.NewRecorder()
	recorder.Code = resp.StatusCode
	recorder.Body.ReadFrom(resp.Body)
	parsed := decodeDesktopAcceptanceResponse(t, recorder)
	assert.True(t, parsed.Success)
	assert.Equal(t, "OK", parsed.Code)
	bundle := decodeDesktopAcceptanceBundle(t, recorder)
	assert.NotEmpty(t, bundle.AccessToken)
	assert.NotEmpty(t, bundle.RefreshToken)
	assert.Equal(t, "desktop", bundle.Session.ClientType)
}
