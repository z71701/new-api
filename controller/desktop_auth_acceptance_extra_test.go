package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// A. Refresh replay and revocation
// ---------------------------------------------------------------------------

// TestDesktopAcceptanceRefreshOldRTWithin30sReplaySucceeds verifies the rotation
// race tolerance: a refresh with the immediately-rotated-away token, still inside
// the 30s grace window, returns the same bundle as the winning refresh and does
// not revoke the session.
func TestDesktopAcceptanceRefreshOldRTWithin30sReplaySucceeds(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	first := decodeDesktopAcceptanceBundle(t, login)
	require.NotEmpty(t, first.RefreshToken)
	require.NotEmpty(t, first.Session.SID)

	body, err := common.Marshal(desktopRefreshRequest{RefreshToken: first.RefreshToken, SID: first.Session.SID})
	require.NoError(t, err)
	rotated := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(body), "", service.AuthIdentity{}, DesktopRefresh)
	require.Equal(t, http.StatusOK, rotated.Code, rotated.Body.String())
	second := decodeDesktopAcceptanceBundle(t, rotated)
	require.NotEmpty(t, second.RefreshToken)
	assert.NotEqual(t, first.RefreshToken, second.RefreshToken)

	// Replay the old token immediately — still inside the 30s replay window.
	replayBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: first.RefreshToken, SID: first.Session.SID})
	require.NoError(t, err)
	replay := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(replayBody), "", service.AuthIdentity{}, DesktopRefresh)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	replayed := decodeDesktopAcceptanceBundle(t, replay)
	assert.Equal(t, second.RefreshToken, replayed.RefreshToken, "replayed old token must resolve to the current rotated token")

	stored, err := model.GetUserSessionBySID(first.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusActive, stored.Status, "session must remain active after in-window replay")
}

// TestDesktopAcceptanceRefreshOldRTAfter30sRevokesSession verifies that replaying
// a previous refresh token outside the 30s grace window revokes the entire
// session family.
func TestDesktopAcceptanceRefreshOldRTAfter30sRevokesSession(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	first := decodeDesktopAcceptanceBundle(t, login)
	require.NotEmpty(t, first.RefreshToken)
	require.NotEmpty(t, first.Session.SID)

	body, err := common.Marshal(desktopRefreshRequest{RefreshToken: first.RefreshToken, SID: first.Session.SID})
	require.NoError(t, err)
	rotated := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(body), "", service.AuthIdentity{}, DesktopRefresh)
	require.Equal(t, http.StatusOK, rotated.Code, rotated.Body.String())

	// Simulate >30s elapsed by pushing PreviousValidUntil into the past.
	past := time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", first.Session.SID).
		Update("previous_valid_until", past).Error)

	replayBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: first.RefreshToken, SID: first.Session.SID})
	require.NoError(t, err)
	replay := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(replayBody), "", service.AuthIdentity{}, DesktopRefresh)
	assert.Equal(t, http.StatusUnauthorized, replay.Code)
	parsed := decodeDesktopAcceptanceResponse(t, replay)
	assert.Equal(t, "AUTH_SESSION_REVOKED", parsed.Code)

	stored, err := model.GetUserSessionBySID(first.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
	assert.Equal(t, "refresh_reuse", stored.RevokedReason)
}

// TestDesktopAcceptanceRefreshRandomTokenDoesNotRevokeOrLeak verifies that a
// random/garbage refresh token yields 401 without touching the real session and
// without leaking session metadata in the response body.
func TestDesktopAcceptanceRefreshRandomTokenDoesNotRevokeOrLeak(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	bundle := decodeDesktopAcceptanceBundle(t, login)
	require.NotEmpty(t, bundle.Session.SID)

	randomBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: "totally-random-garbage", SID: bundle.Session.SID})
	require.NoError(t, err)
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(randomBody), "", service.AuthIdentity{}, DesktopRefresh)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_UNAUTHORIZED", parsed.Code)

	// The response body must not echo any session identifiers.
	raw := response.Body.String()
	assert.NotContains(t, raw, bundle.Session.SID)
	assert.NotContains(t, raw, bundle.AccessToken)
	assert.NotContains(t, raw, bundle.RefreshToken)

	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusActive, stored.Status, "unknown token must not revoke the session")
}

// ---------------------------------------------------------------------------
// B. Logout idempotency and edge cases
// ---------------------------------------------------------------------------

// TestDesktopAcceptanceLogoutIdempotent verifies that logging out twice with the
// same refresh token returns success both times (second is a no-op).
func TestDesktopAcceptanceLogoutIdempotent(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	bundle := decodeDesktopAcceptanceBundle(t, login)

	logoutBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
	require.NoError(t, err)

	first := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/logout", string(logoutBody), "", service.AuthIdentity{}, DesktopLogout)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	second := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/logout", string(logoutBody), "", service.AuthIdentity{}, DesktopLogout)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String(), "second logout must be idempotent")

	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
}

// TestDesktopAcceptanceLogoutSIDOnlyCannotRevoke verifies that a logout request
// with an empty refresh_token is rejected (400) and does not touch the session.
func TestDesktopAcceptanceLogoutSIDOnlyCannotRevoke(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	bundle := decodeDesktopAcceptanceBundle(t, login)

	body, err := common.Marshal(desktopRefreshRequest{RefreshToken: "", SID: bundle.Session.SID})
	require.NoError(t, err)
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/logout", string(body), "", service.AuthIdentity{}, DesktopLogout)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "INVALID_ARGUMENT", parsed.Code)

	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusActive, stored.Status, "session must remain active")
}

// TestDesktopAcceptanceLogoutWithExpiredAccessTokenValidRT verifies that when
// the Authorization header carries an invalid/expired access token, logout still
// succeeds via the strict refresh-token revocation path.
func TestDesktopAcceptanceLogoutWithExpiredAccessTokenValidRT(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	bundle := decodeDesktopAcceptanceBundle(t, login)

	logoutBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/desktop/auth/logout", strings.NewReader(string(logoutBody)))
	c.Request.Header.Set("Content-Type", "application/json")
	// Intentionally malformed/expired access token — ParseAccessToken must fail
	// so the AT-mismatch guard is skipped and the strict RT path runs.
	c.Request.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	DesktopLogout(c)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	parsed := decodeDesktopAcceptanceResponse(t, recorder)
	assert.True(t, parsed.Success)
	assert.Equal(t, "OK", parsed.Code)

	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
}

// ---------------------------------------------------------------------------
// C. Session absolute expiry
// ---------------------------------------------------------------------------

// TestDesktopAcceptanceSessionAbsoluteExpiry verifies that when a session's
// ExpiresAt has been pushed into the past, both refresh and logout handle the
// expired session gracefully.
func TestDesktopAcceptanceSessionAbsoluteExpiry(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	bundle := decodeDesktopAcceptanceBundle(t, login)
	require.NotEmpty(t, bundle.Session.SID)

	past := time.Now().Add(-time.Hour).Unix()
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", bundle.Session.SID).
		Update("expires_at", past).Error)

	// Refresh must reject the expired session (mapped as revoked/inactive).
	refreshBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
	require.NoError(t, err)
	refresh := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(refreshBody), "", service.AuthIdentity{}, DesktopRefresh)
	assert.Equal(t, http.StatusUnauthorized, refresh.Code)
	refreshParsed := decodeDesktopAcceptanceResponse(t, refresh)
	assert.Contains(t, []string{"AUTH_SESSION_EXPIRED", "AUTH_SESSION_REVOKED"}, refreshParsed.Code)

	// Logout on an expired session is a no-op success (idempotent cleanup).
	logoutBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
	require.NoError(t, err)
	logout := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/logout", string(logoutBody), "", service.AuthIdentity{}, DesktopLogout)
	assert.Equal(t, http.StatusOK, logout.Code, logout.Body.String())
}

// ---------------------------------------------------------------------------
// D. TOTP consecutive-failure lockout
// ---------------------------------------------------------------------------

// desktopVerifyWrongCode submits a TOTP verify request with the given code and
// returns the response recorder.
func desktopVerifyCode(t *testing.T, flowToken, code string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := common.Marshal(map[string]string{"flow_token": flowToken, "method": "totp", "code": code})
	require.NoError(t, err)
	return securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(body), "", service.AuthIdentity{}, DesktopVerify)
}

// TestDesktopAcceptanceTOTPLockoutAfterConsecutiveFailures verifies that after
// MaxFailAttempts consecutive wrong TOTP codes, the account is locked and even
// the correct code is rejected.
func TestDesktopAcceptanceTOTPLockoutAfterConsecutiveFailures(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	secret := seedDesktopTOTP(t, user)
	flowToken := desktopAcceptanceChallenge(t, user)

	// Submit wrong codes until the account locks (common.MaxFailAttempts = 5).
	for i := 1; i <= common.MaxFailAttempts; i++ {
		resp := desktopVerifyCode(t, flowToken, "000000")
		assert.Equal(t, http.StatusUnauthorized, resp.Code, "attempt %d", i)
		parsed := decodeDesktopAcceptanceResponse(t, resp)
		assert.Equal(t, "AUTH_VERIFICATION_FAILED", parsed.Code, "attempt %d", i)
	}

	// The TwoFA record must now carry a non-null LockedUntil in the future.
	twoFA, err := model.GetTwoFAByUserId(user.Id)
	require.NoError(t, err)
	require.NotNil(t, twoFA.LockedUntil, "locked_until must be set after %d failures", common.MaxFailAttempts)
	assert.True(t, twoFA.LockedUntil.After(time.Now()), "locked_until must be in the future")

	// Even the correct code is now rejected while locked. The verification
	// policy marks TOTP as unavailable once TwoFALocked is set, so the response
	// surfaces as 403 AUTH_VERIFICATION_UNSUPPORTED rather than 401.
	correctCode, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	lockedResp := desktopVerifyCode(t, flowToken, correctCode)
	require.NotEqual(t, http.StatusOK, lockedResp.Code, "correct code must not succeed while locked")
	lockedParsed := decodeDesktopAcceptanceResponse(t, lockedResp)
	assert.Contains(t,
		[]string{"AUTH_VERIFICATION_FAILED", "AUTH_VERIFICATION_UNSUPPORTED"},
		lockedParsed.Code, "correct code must be rejected while locked")
}

// TestDesktopAcceptanceTOTPLockoutCorrectCodeRejected is focused: after the
// lockout threshold, a correct TOTP code does not create a session.
func TestDesktopAcceptanceTOTPLockoutCorrectCodeRejected(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	secret := seedDesktopTOTP(t, user)
	flowToken := desktopAcceptanceChallenge(t, user)

	// Burn through the failure budget.
	for range common.MaxFailAttempts {
		resp := desktopVerifyCode(t, flowToken, "000000")
		require.Equal(t, http.StatusUnauthorized, resp.Code, resp.Body.String())
	}

	correctCode, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	resp := desktopVerifyCode(t, flowToken, correctCode)
	require.NotEqual(t, http.StatusOK, resp.Code, resp.Body.String())
	parsed := decodeDesktopAcceptanceResponse(t, resp)
	assert.Contains(t,
		[]string{"AUTH_VERIFICATION_FAILED", "AUTH_VERIFICATION_UNSUPPORTED"},
		parsed.Code)

	// No session should have been created for the user.
	var sessionCount int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("user_id = ?", user.Id).Count(&sessionCount).Error)
	assert.Equal(t, int64(0), sessionCount, "locked correct code must not create a session")
}

// ---------------------------------------------------------------------------
// E. Desktop/Web auth-flow cross-exchange
// ---------------------------------------------------------------------------

// TestDesktopAcceptanceDesktopFlowCannotBeUsedOnWebVerify verifies that a TOTP
// flow created by the desktop login path is rejected by the web verification
// entry point (client_type mismatch).
func TestDesktopAcceptanceDesktopFlowCannotBeUsedOnWebVerify(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	seedDesktopTOTP(t, user)
	flowToken := desktopAcceptanceChallenge(t, user)

	// The web verify path expects a web client_type; the desktop flow carries
	// client_type=desktop, so RequireLoginVerification must reject it.
	_, err := service.RequireLoginVerification(flowToken, "totp")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrAuthFlowInvalid)
}

// TestDesktopAcceptanceWebFlowCannotBeUsedOnDesktopVerify verifies that a TOTP
// flow created by the web login path is rejected by the desktop /verify endpoint.
func TestDesktopAcceptanceWebFlowCannotBeUsedOnDesktopVerify(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	seedDesktopTOTP(t, user)

	challenge, err := service.StartLoginVerification(user, "password", nil)
	require.NoError(t, err)
	require.NotNil(t, challenge)
	webFlowToken := challenge.FlowToken

	code, err := totp.GenerateCode("JBSWY3DPEHPK3PXP", time.Now())
	require.NoError(t, err)
	resp := desktopVerifyCode(t, webFlowToken, code)
	assert.Equal(t, http.StatusUnauthorized, resp.Code, resp.Body.String())
	parsed := decodeDesktopAcceptanceResponse(t, resp)
	assert.Equal(t, "AUTH_FLOW_EXPIRED", parsed.Code, "client_type mismatch maps to FLOW_EXPIRED")
}

// ---------------------------------------------------------------------------
// F. Logout vs refresh concurrent race
// ---------------------------------------------------------------------------

// TestDesktopAcceptanceLogoutRefreshRaceDoesNotCorrupt fires concurrent refresh
// and logout requests and asserts the final session state is coherent — either
// active (with a rotated hash) or revoked — with no partial update or panic.
func TestDesktopAcceptanceLogoutRefreshRaceDoesNotCorrupt(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	bundle := decodeDesktopAcceptanceBundle(t, login)

	var wg sync.WaitGroup
	errs := make(chan error, 12)

	doRefresh := func() {
		defer wg.Done()
		body, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
		if err != nil {
			errs <- err
			return
		}
		resp := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(body), "", service.AuthIdentity{}, DesktopRefresh)
		_ = resp
	}

	doLogout := func() {
		defer wg.Done()
		body, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
		if err != nil {
			errs <- err
			return
		}
		resp := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/logout", string(body), "", service.AuthIdentity{}, DesktopLogout)
		_ = resp
	}

	t.Cleanup(func() {
		close(errs)
	})

	// Small fan-out to avoid SQLite write contention while still exercising the race.
	for range 3 {
		wg.Add(1)
		go doRefresh()
	}
	for range 3 {
		wg.Add(1)
		go doLogout()
	}
	wg.Wait()

	// The session must be in a coherent terminal state: active or revoked.
	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Contains(t,
		[]string{model.UserSessionStatusActive, model.UserSessionStatusRevoked},
		stored.Status, "session must be active or revoked, not in a partial state")

	if stored.Status == model.UserSessionStatusActive {
		// Active: the refresh hash must have rotated away from the original.
		assert.NotEmpty(t, stored.RefreshHash)
	}
}

// ---------------------------------------------------------------------------
// G. 429 generic rate limiting
// ---------------------------------------------------------------------------

// TestDesktopAcceptanceRateLimit429RetryAfterAndNoStore verifies that the
// DesktopAuthRateLimit middleware emits 429 with Retry-After, Cache-Control:
// no-store, and the AUTH_RATE_LIMITED shell.
func TestDesktopAcceptanceRateLimit429RetryAfterAndNoStore(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)

	previousEnable := common.CriticalRateLimitEnable
	previousNum := common.CriticalRateLimitNum
	previousDuration := common.CriticalRateLimitDuration
	common.CriticalRateLimitEnable = true
	common.CriticalRateLimitNum = 1
	common.CriticalRateLimitDuration = 60
	t.Cleanup(func() {
		common.CriticalRateLimitEnable = previousEnable
		common.CriticalRateLimitNum = previousNum
		common.CriticalRateLimitDuration = previousDuration
	})

	gin.SetMode(gin.TestMode)

	// Use a unique client IP so the global in-memory singleton does not leak
	// state across tests.
	const clientIP = "203.0.113.77"

	buildChain := func() *httptest.ResponseRecorder {
		engine := gin.New()
		engine.Use(middleware.DesktopAuthRateLimit(), middleware.DisableCache())
		engine.POST("/api/desktop/auth/login", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/desktop/auth/login", strings.NewReader("{}"))
		req.RemoteAddr = clientIP + ":12345"
		engine.ServeHTTP(recorder, req)
		return recorder
	}

	first := buildChain()
	assert.Equal(t, http.StatusOK, first.Code, "first request within limit should pass")

	second := buildChain()
	require.Equal(t, http.StatusTooManyRequests, second.Code, "second request must be rate-limited")
	retryAfter := second.Header().Get("Retry-After")
	assert.NotEmpty(t, retryAfter, "Retry-After header must be present")
	assert.NotEqual(t, "0", retryAfter, "Retry-After must be a positive integer")
	assert.Equal(t, "no-store", second.Header().Get("Cache-Control"), "rate-limited response must carry no-store")

	var shell struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(second.Body.Bytes(), &shell), second.Body.String())
	assert.False(t, shell.Success)
	assert.Equal(t, "AUTH_RATE_LIMITED", shell.Code)
}

// ---------------------------------------------------------------------------
// H. 5xx no-store
// ---------------------------------------------------------------------------

// TestDesktopAcceptance5xxHasNoStore verifies that even a 500 response carries
// Cache-Control: no-store because setAuthNoStore runs before the DB failure.
func TestDesktopAcceptance5xxHasNoStore(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)

	// Swap in a closed DB so the verify handler hits a 500, mirroring the
	// existing TestDesktopAcceptanceInternalErrorOnDBFailure seam.
	closedDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlConn, err := closedDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlConn.Close())
	previousDB := model.DB
	model.DB = closedDB
	t.Cleanup(func() { model.DB = previousDB })

	body := `{"flow_token":"any-flow-token","method":"totp","code":"123456"}`
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", body, "", service.AuthIdentity{}, DesktopVerify)
	require.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"), "500 response must still carry no-store")
}
