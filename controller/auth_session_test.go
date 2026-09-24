package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuthLogoutRejectsRefreshCookieSessionMismatch(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousSecret := common.SessionSecret
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "auth-logout-mismatch-test-secret"
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.SessionSecret = previousSecret
	})

	user := &model.User{
		Username: "logout-mismatch-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	sessionA, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "agent-a")
	require.NoError(t, err)
	sessionB, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "agent-b")
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/logout", nil)
	c.Request.Header.Set("Authorization", "Bearer "+sessionA.AccessToken)
	c.Request.Header.Set("X-Auth-Session", sessionA.Session.SID)
	c.Request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: sessionB.RefreshToken})

	AuthLogout(c)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Equal(t, "AUTH_SESSION_MISMATCH", response.Code)
	for _, sid := range []string{sessionA.Session.SID, sessionB.Session.SID} {
		stored, err := model.GetUserSessionBySID(sid)
		require.NoError(t, err)
		assert.Equal(t, model.UserSessionStatusActive, stored.Status)
	}
}

func TestWebAuthLogoutRejectsDesktopAccessToken(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousSecret := common.SessionSecret
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "desktop-web-logout-test-secret"
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.SessionSecret = previousSecret
	})

	user := &model.User{
		Username: "desktop-logout-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	bundle, err := service.CreateDesktopLoginSession(user.Id, "password", "127.0.0.1", "desktop-agent", service.SessionDeviceMetadata{
		DeviceID: "desktop-id", DeviceName: "desktop", Platform: "win32", Arch: "x64", ClientVersion: "1.0.0",
	})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/logout", nil)
	c.Request.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
	c.Request.Header.Set("X-Auth-Session", bundle.Session.SID)

	AuthLogout(c)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusActive, stored.Status)
}

func TestWriteAuthSessionErrorMapsSessionGrowthLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name           string
		err            error
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "active session limit",
			err:            model.ErrUserSessionLimit,
			expectedStatus: http.StatusConflict,
			expectedCode:   "AUTH_SESSION_LIMIT",
		},
		{
			name:           "issuance limit",
			err:            model.ErrUserSessionIssuanceLimit,
			expectedStatus: http.StatusTooManyRequests,
			expectedCode:   "AUTH_SESSION_ISSUANCE_LIMIT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			writeAuthSessionError(c, test.err)

			assert.Equal(t, test.expectedStatus, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, test.expectedCode, response.Code)
		})
	}
}

func setupDesktopAuthTest(t *testing.T) *model.User {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedis, previousSecret := common.RedisEnabled, common.SessionSecret
	previousLogin, previousTurnstile := common.PasswordLoginEnabled, common.TurnstileCheckEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.AuditLog{}))
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled = false
	common.SessionSecret = "desktop-auth-test-secret"
	common.PasswordLoginEnabled, common.TurnstileCheckEnabled = true, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled, common.SessionSecret = previousRedis, previousSecret
		common.PasswordLoginEnabled, common.TurnstileCheckEnabled = previousLogin, previousTurnstile
	})
	password, err := common.Password2Hash("desktop-password")
	require.NoError(t, err)
	user := &model.User{Username: "desktop-user", Password: password, DisplayName: "Desktop User", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
	require.NoError(t, db.Create(user).Error)
	return user
}

func TestDesktopAuthLoginRefreshLogout(t *testing.T) {
	user := setupDesktopAuthTest(t)

	loginBody, err := common.Marshal(map[string]string{
		"username": user.Username, "password": "desktop-password", "device_id": "install-1",
		"device_name": "Workstation", "platform": "win32", "arch": "x64", "client_version": "1.0.0",
	})
	require.NoError(t, err)
	login := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/login", string(loginBody), "", service.AuthIdentity{}, DesktopLogin)
	assert.Equal(t, http.StatusOK, login.Code)
	assert.Equal(t, "no-store", login.Header().Get("Cache-Control"))
	assert.Empty(t, login.Header().Values("Set-Cookie"))
	var result struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Data    struct {
			AccessToken  string                   `json:"access_token"`
			RefreshToken string                   `json:"refresh_token"`
			Session      service.LoginSessionView `json:"session"`
			User         desktopUserView          `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(login.Body.Bytes(), &result))
	require.True(t, result.Success, login.Body.String())
	assert.Equal(t, "OK", result.Code)
	assert.Equal(t, "desktop", result.Data.Session.ClientType)
	assert.Equal(t, strconv.Itoa(user.Id), result.Data.User.ID)
	assert.NotEmpty(t, result.Data.RefreshToken)

	refreshBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: result.Data.RefreshToken, SID: result.Data.Session.SID})
	require.NoError(t, err)
	refresh := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(refreshBody), "", service.AuthIdentity{}, DesktopRefresh)
	require.NoError(t, common.Unmarshal(refresh.Body.Bytes(), &result))
	require.True(t, result.Success, refresh.Body.String())
	assert.NotEmpty(t, result.Data.RefreshToken)

	logoutBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: result.Data.RefreshToken, SID: result.Data.Session.SID})
	require.NoError(t, err)
	logout := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/logout", string(logoutBody), "", service.AuthIdentity{}, DesktopLogout)
	assert.Equal(t, http.StatusOK, logout.Code)
	var logoutResult struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Data    struct {
			LoggedOut bool `json:"logged_out"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(logout.Body.Bytes(), &logoutResult))
	assert.True(t, logoutResult.Success)
	assert.True(t, logoutResult.Data.LoggedOut)
}

func TestDesktopAuthRejectsInvalidCredentialsAndCrossClientRefresh(t *testing.T) {
	user := setupDesktopAuthTest(t)
	body := `{"username":"` + user.Username + `","password":"wrong","device_id":"install-1","device_name":"Workstation","platform":"win32","arch":"x64","client_version":"1.0.0"}`
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/login", body, "", service.AuthIdentity{}, DesktopLogin)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"AUTH_INVALID_CREDENTIALS"`)

	web, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	require.NoError(t, err)
	refreshBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: web.RefreshToken, SID: web.Session.SID})
	require.NoError(t, err)
	response = securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(refreshBody), "", service.AuthIdentity{}, DesktopRefresh)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"AUTH_UNAUTHORIZED"`)
}

func TestDesktopLogoutRejectsMismatchedAccessToken(t *testing.T) {
	user := setupDesktopAuthTest(t)
	device := service.SessionDeviceMetadata{DeviceID: "install-1", DeviceName: "Workstation", Platform: "win32", Arch: "x64", ClientVersion: "1.0.0"}
	first, err := service.CreateDesktopLoginSession(user.Id, "password", "127.0.0.1", "desktop", device)
	require.NoError(t, err)
	second, err := service.CreateDesktopLoginSession(user.Id, "password", "127.0.0.1", "desktop", device)
	require.NoError(t, err)
	body, err := common.Marshal(desktopRefreshRequest{RefreshToken: first.RefreshToken, SID: first.Session.SID})
	require.NoError(t, err)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/desktop/auth/logout", strings.NewReader(string(body)))
	c.Request.Header.Set("Authorization", "Bearer "+second.AccessToken)
	DesktopLogout(c)
	assert.Equal(t, http.StatusConflict, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"AUTH_SESSION_MISMATCH"`)
	stored, err := model.GetUserSessionBySID(first.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusActive, stored.Status)
}

func TestDesktopLogoutIgnoresUnusableOptionalAccessToken(t *testing.T) {
	user := setupDesktopAuthTest(t)
	device := service.SessionDeviceMetadata{DeviceID: "install-1", DeviceName: "Workstation", Platform: "win32", Arch: "x64", ClientVersion: "1.0.0"}
	bundle, err := service.CreateDesktopLoginSession(user.Id, "password", "127.0.0.1", "desktop", device)
	require.NoError(t, err)
	body, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
	require.NoError(t, err)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/desktop/auth/logout", strings.NewReader(string(body)))
	c.Request.Header.Set("Authorization", "Bearer expired-or-invalid-access-token")
	DesktopLogout(c)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `"logged_out":true`)
}
func TestDesktopLoginReturnsVerificationChallengeAndCompletesTOTP(t *testing.T) {
	user := setupDesktopAuthTest(t)
	factor := &model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
	require.NoError(t, model.DB.Create(factor).Error)
	loginBody := `{"username":"desktop-user","password":"desktop-password","device_id":"install-1","device_name":"Workstation","platform":"win32","arch":"x64","client_version":"1.0.0"}`
	login := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/login", loginBody, "", service.AuthIdentity{}, DesktopLogin)
	var challenge struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Data    struct {
			FlowToken string   `json:"flow_token"`
			Methods   []string `json:"methods"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(login.Body.Bytes(), &challenge))
	require.True(t, challenge.Success, login.Body.String())
	assert.Equal(t, "AUTH_VERIFICATION_REQUIRED", challenge.Code)
	assert.Equal(t, []string{"totp"}, challenge.Data.Methods)

	code, err := totp.GenerateCode(factor.Secret, time.Now())
	require.NoError(t, err)
	verifyBody, err := common.Marshal(map[string]string{"flow_token": challenge.Data.FlowToken, "method": "totp", "code": code})
	require.NoError(t, err)
	verify := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(verifyBody), "", service.AuthIdentity{}, DesktopVerify)
	assert.Equal(t, http.StatusOK, verify.Code)
	assert.Contains(t, verify.Body.String(), `"code":"OK"`)
	assert.Contains(t, verify.Body.String(), `"client_type":"desktop"`)
	assert.Empty(t, verify.Header().Values("Set-Cookie"))

	replay := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(verifyBody), "", service.AuthIdentity{}, DesktopVerify)
	assert.Equal(t, http.StatusUnauthorized, replay.Code)
	assert.Contains(t, replay.Body.String(), `"code":"AUTH_FLOW_EXPIRED"`)
}

func TestDesktopLoginRequiresCaptchaToken(t *testing.T) {
	setupDesktopAuthTest(t)
	common.TurnstileCheckEnabled = true
	body := `{"username":"desktop-user","password":"desktop-password","device_id":"install-1","device_name":"Workstation","platform":"win32","arch":"x64","client_version":"1.0.0"}`
	missing := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/login", body, "", service.AuthIdentity{}, DesktopLogin)
	assert.Equal(t, http.StatusForbidden, missing.Code)
	assert.Contains(t, missing.Body.String(), `"code":"AUTH_CAPTCHA_REQUIRED"`)
}

func TestSessionLimitDoesNotRecordRejectedLoginAsSuccessful(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousActiveLimit := common.UserSessionActiveLimit
	previousIssuanceLimit := common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.PasskeyCredential{}))
	model.DB = db
	common.RedisEnabled = false
	common.UserSessionActiveLimit = 1
	common.UserSessionIssuanceLimit = 100
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.UserSessionActiveLimit = previousActiveLimit
		common.UserSessionIssuanceLimit = previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
	})

	const previousLastLoginAt = int64(123)
	user := &model.User{
		Username: "rejected-login-audit-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, LastLoginAt: previousLastLoginAt,
	}
	require.NoError(t, db.Create(user).Error)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.UserSession{
		SID: "existing-active-session", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
		Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
		CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	setupLogin(user, nil, c)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	var stored model.User
	require.NoError(t, db.First(&stored, user.Id).Error)
	assert.Equal(t, previousLastLoginAt, stored.LastLoginAt)
}
