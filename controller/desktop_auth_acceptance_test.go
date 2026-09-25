package controller

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
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
	"gorm.io/gorm/logger"
)

// desktopAcceptanceResponse is the shared envelope emitted by every desktop
// auth endpoint via writeDesktopAuthResponse.
type desktopAcceptanceResponse struct {
	Success    bool            `json:"success"`
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	RequestID  string          `json:"request_id"`
	ServerTime int64           `json:"server_time"`
	Data       json.RawMessage `json:"data"`
}

type desktopAcceptanceBundleData struct {
	AccessToken  string                   `json:"access_token"`
	RefreshToken string                   `json:"refresh_token"`
	TokenType    string                   `json:"token_type"`
	Session      service.LoginSessionView `json:"session"`
	User         desktopUserView          `json:"user"`
}

// setupDesktopAuthAcceptanceTest mirrors setupDesktopAuthTest but supports the
// external-database CI matrix: TEST_SECURITY_DIALECT selects sqlite (default),
// mysql or postgres, and TEST_<DIALECT>_DSN points at a loopback disposable
// instance. It always returns a fresh "desktop-user"/"desktop-password" user
// with Turnstile and password encryption disabled.
func setupDesktopAuthAcceptanceTest(t *testing.T) *model.User {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousSecret := common.RedisEnabled, common.SessionSecret
	previousLogin, previousTurnstile := common.PasswordLoginEnabled, common.TurnstileCheckEnabled
	previousEncryption := common.PasswordLoginEncryptionEnabled
	previousActiveLimit := common.UserSessionActiveLimit
	previousIssuanceLimit := common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds

	dialect := os.Getenv("TEST_SECURITY_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
	db, _ := newAuditTestDatabase(t, dialect, dsn)
	db.Logger = logger.Default.LogMode(logger.Silent)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.AuditLog{}))
	model.DB, model.LOG_DB = db, db
	dbType := common.DatabaseTypeSQLite
	switch dialect {
	case "mysql":
		dbType = common.DatabaseTypeMySQL
	case "postgres":
		dbType = common.DatabaseTypePostgreSQL
	}
	common.SetDatabaseTypes(dbType, dbType)
	common.RedisEnabled = false
	common.SessionSecret = "desktop-acceptance-test-secret"
	common.PasswordLoginEnabled, common.TurnstileCheckEnabled = true, false
	common.PasswordLoginEncryptionEnabled = false
	common.UserSessionActiveLimit = common.DefaultUserSessionActiveLimit
	common.UserSessionIssuanceLimit = common.DefaultUserSessionIssuanceLimit
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.SessionSecret = previousRedis, previousSecret
		common.PasswordLoginEnabled, common.TurnstileCheckEnabled = previousLogin, previousTurnstile
		common.PasswordLoginEncryptionEnabled = previousEncryption
		common.UserSessionActiveLimit = previousActiveLimit
		common.UserSessionIssuanceLimit = previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
		// Release the file-backed sqlite handle before t.TempDir removes the
		// directory (Windows cannot unlink an open file).
		if sqlConn, err := db.DB(); err == nil {
			_ = sqlConn.Close()
		}
	})
	password, err := common.Password2Hash("desktop-password")
	require.NoError(t, err)
	user := &model.User{
		Username: "desktop-user", Password: password, DisplayName: "Desktop User",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

// desktopAcceptanceLoginBody builds the standard login payload; entries in mods
// override base fields, and a nil value removes the key entirely.
func desktopAcceptanceLoginBody(t *testing.T, mods map[string]any) string {
	t.Helper()
	body := map[string]any{
		"username": "desktop-user", "password": "desktop-password",
		"device_id": "install-accept-1", "device_name": "Workstation",
		"platform": "win32", "arch": "x64", "client_version": "1.0.0",
	}
	for key, value := range mods {
		if value == nil {
			delete(body, key)
		} else {
			body[key] = value
		}
	}
	out, err := common.Marshal(body)
	require.NoError(t, err)
	return string(out)
}

func decodeDesktopAcceptanceResponse(t *testing.T, recorder *httptest.ResponseRecorder) desktopAcceptanceResponse {
	t.Helper()
	var parsed desktopAcceptanceResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &parsed), recorder.Body.String())
	return parsed
}

func loginDesktopAcceptance(t *testing.T, mods map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/login", desktopAcceptanceLoginBody(t, mods), "", service.AuthIdentity{}, DesktopLogin)
}

func decodeDesktopAcceptanceBundle(t *testing.T, recorder *httptest.ResponseRecorder) desktopAcceptanceBundleData {
	t.Helper()
	var parsed struct {
		Data desktopAcceptanceBundleData `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &parsed), recorder.Body.String())
	return parsed.Data
}

// encryptDesktopPasswordV2 reproduces the client-side v2 envelope expected by
// common.DecryptPassword: a 32-byte AES-GCM key wrapped with RSA-OAEP-SHA256
// (label "password-v2") and the password sealed with GCM (AAD
// "password-v2:<keyID>").
func encryptDesktopPasswordV2(t *testing.T, keyID, publicKeyPEM, password string) string {
	t.Helper()
	block, rest := pem.Decode([]byte(publicKeyPEM))
	require.NotNil(t, block, "public key PEM must decode")
	require.Empty(t, rest)
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	publicKey, ok := parsed.(*rsa.PublicKey)
	require.True(t, ok)

	aesKey := make([]byte, 32)
	_, err = rand.Read(aesKey)
	require.NoError(t, err)
	wrappedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, aesKey, []byte("password-v2"))
	require.NoError(t, err)
	nonce := make([]byte, 12)
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	cipherBlock, err := aes.NewCipher(aesKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(cipherBlock)
	require.NoError(t, err)
	ciphertext := gcm.Seal(nil, nonce, []byte(password), []byte("password-v2:"+keyID))
	return "v2." +
		base64.StdEncoding.EncodeToString(wrappedKey) + "." +
		base64.StdEncoding.EncodeToString(nonce) + "." +
		base64.StdEncoding.EncodeToString(ciphertext)
}

// loadTestEncryptionKey generates and loads a fresh RSA key pair, returning the
// active key ID. The previous in-memory key is replaced; callers that need a
// clean slate load their own key.
func loadTestEncryptionKey(t *testing.T) string {
	t.Helper()
	pemBlock, err := common.GeneratePasswordEncryptionPrivateKey()
	require.NoError(t, err)
	require.NoError(t, common.LoadPasswordEncryptionPrivateKey(pemBlock))
	keyID, _ := common.PasswordEncryptionPublicKey()
	require.NotEmpty(t, keyID)
	return keyID
}

func enableDesktopEncryption(t *testing.T) string {
	t.Helper()
	previous := common.PasswordLoginEncryptionEnabled
	common.PasswordLoginEncryptionEnabled = true
	t.Cleanup(func() { common.PasswordLoginEncryptionEnabled = previous })
	return loadTestEncryptionKey(t)
}

func seedDesktopTOTP(t *testing.T, user *model.User) string {
	t.Helper()
	secret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, model.DB.Create(&model.TwoFA{UserId: user.Id, Secret: secret, IsEnabled: true}).Error)
	return secret
}

// desktopAcceptanceChallenge runs a password login and returns the pending
// verification flow token. The caller must have already seeded TOTP via
// seedDesktopTOTP.
func desktopAcceptanceChallenge(t *testing.T, user *model.User) string {
	t.Helper()
	login := loginDesktopAcceptance(t, nil)
	parsed := decodeDesktopAcceptanceResponse(t, login)
	require.True(t, parsed.Success, login.Body.String())
	require.Equal(t, "AUTH_VERIFICATION_REQUIRED", parsed.Code)
	var challenge struct {
		FlowToken string   `json:"flow_token"`
		Methods   []string `json:"methods"`
	}
	require.NoError(t, common.Unmarshal(parsed.Data, &challenge))
	require.Equal(t, []string{"totp"}, challenge.Methods)
	return challenge.FlowToken
}

// ---------------------------------------------------------------------------
// 1. Captcha
// ---------------------------------------------------------------------------

func TestDesktopAcceptanceCaptchaRequired(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.TurnstileCheckEnabled = true
	response := loginDesktopAcceptance(t, nil)
	assert.Equal(t, http.StatusForbidden, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.False(t, parsed.Success)
	assert.Equal(t, "AUTH_CAPTCHA_REQUIRED", parsed.Code)
}

// TestDesktopAcceptanceCaptchaInvalid and TestDesktopAcceptanceCaptchaUnavailable
// cannot be exercised from the controller package: middleware.turnstileSiteVerify
// is an unexported package variable, so the rejected/unavailable seams live in
// middleware/turnstile_desktop_test.go. The HTTP mapping in DesktopLogin
// (ErrTurnstileRejected -> 400 AUTH_CAPTCHA_INVALID, any other error -> 503
// AUTH_CAPTCHA_UNAVAILABLE) is covered by these seam tests plus the static
// branch in controller/desktop_auth.go.

// ---------------------------------------------------------------------------
// 2. Password encryption
// ---------------------------------------------------------------------------

func TestDesktopAcceptanceEncryptionDisabledRejectsEncryptedPayload(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	response := loginDesktopAcceptance(t, map[string]any{
		"password_encrypted": "v2.aaa.bbb.ccc",
		"encryption_key_id":  strings.Repeat("0", 32),
	})
	assert.Equal(t, http.StatusBadRequest, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "INVALID_ARGUMENT", parsed.Code)
}

func TestDesktopAcceptanceEncryptionSuccess(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	keyID := enableDesktopEncryption(t)
	_, publicKeyPEM := common.PasswordEncryptionPublicKey()
	ciphertext := encryptDesktopPasswordV2(t, keyID, publicKeyPEM, "desktop-password")
	response := loginDesktopAcceptance(t, map[string]any{
		"password":           nil,
		"password_encrypted": ciphertext,
		"encryption_key_id":  keyID,
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	parsed := decodeDesktopAcceptanceResponse(t, response)
	require.True(t, parsed.Success, response.Body.String())
	assert.Equal(t, "OK", parsed.Code)
	bundle := decodeDesktopAcceptanceBundle(t, response)
	assert.NotEmpty(t, bundle.AccessToken)
	assert.Equal(t, "desktop", bundle.Session.ClientType)
	assert.Equal(t, strconv.Itoa(user.Id), bundle.User.ID)
}

func TestDesktopAcceptanceEncryptionPayloadInvalid(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	keyID := enableDesktopEncryption(t)
	response := loginDesktopAcceptance(t, map[string]any{
		"password":           nil,
		"password_encrypted": "v2.not-valid-base64!!",
		"encryption_key_id":  keyID,
	})
	assert.Equal(t, http.StatusBadRequest, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_ENCRYPTION_PAYLOAD_INVALID", parsed.Code)
}

func TestDesktopAcceptanceEncryptionKeyStale(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	enableDesktopEncryption(t)
	response := loginDesktopAcceptance(t, map[string]any{
		"password":           nil,
		"password_encrypted": "v2.aaa.bbb.ccc",
		"encryption_key_id":  strings.Repeat("0", 32),
	})
	assert.Equal(t, http.StatusConflict, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_ENCRYPTION_KEY_STALE", parsed.Code)
}

func TestDesktopAcceptanceEncryptionRequiresEncryptedNotPlain(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	enableDesktopEncryption(t)
	// Encryption enabled: a plaintext password without password_encrypted is rejected.
	response := loginDesktopAcceptance(t, map[string]any{})
	assert.Equal(t, http.StatusBadRequest, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_ENCRYPTION_PAYLOAD_INVALID", parsed.Code)
}

// ---------------------------------------------------------------------------
// 3. TOTP / login verification flow
// ---------------------------------------------------------------------------

func TestDesktopAcceptanceTOTPChallengeAndVerify(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	secret := seedDesktopTOTP(t, user)
	login := loginDesktopAcceptance(t, nil)
	parsed := decodeDesktopAcceptanceResponse(t, login)
	require.True(t, parsed.Success, login.Body.String())
	require.Equal(t, "AUTH_VERIFICATION_REQUIRED", parsed.Code)
	var challenge struct {
		FlowToken string   `json:"flow_token"`
		Methods   []string `json:"methods"`
	}
	require.NoError(t, common.Unmarshal(parsed.Data, &challenge))
	require.Equal(t, []string{"totp"}, challenge.Methods)
	require.NotEmpty(t, challenge.FlowToken)

	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	verifyBody, err := common.Marshal(map[string]string{"flow_token": challenge.FlowToken, "method": "totp", "code": code})
	require.NoError(t, err)
	verify := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(verifyBody), "", service.AuthIdentity{}, DesktopVerify)
	require.Equal(t, http.StatusOK, verify.Code, verify.Body.String())
	verifyParsed := decodeDesktopAcceptanceResponse(t, verify)
	assert.True(t, verifyParsed.Success)
	assert.Equal(t, "OK", verifyParsed.Code)
}

func TestDesktopAcceptanceTOTPReplay(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	secret := seedDesktopTOTP(t, user)
	flowToken := desktopAcceptanceChallenge(t, user)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	verifyBody, err := common.Marshal(map[string]string{"flow_token": flowToken, "method": "totp", "code": code})
	require.NoError(t, err)
	first := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(verifyBody), "", service.AuthIdentity{}, DesktopVerify)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	replay := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(verifyBody), "", service.AuthIdentity{}, DesktopVerify)
	assert.Equal(t, http.StatusUnauthorized, replay.Code)
	replayParsed := decodeDesktopAcceptanceResponse(t, replay)
	assert.Equal(t, "AUTH_FLOW_EXPIRED", replayParsed.Code)
}

func TestDesktopAcceptanceTOTPWrongCode(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	seedDesktopTOTP(t, user)
	flowToken := desktopAcceptanceChallenge(t, user)
	verifyBody, err := common.Marshal(map[string]string{"flow_token": flowToken, "method": "totp", "code": "000000"})
	require.NoError(t, err)
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(verifyBody), "", service.AuthIdentity{}, DesktopVerify)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_VERIFICATION_FAILED", parsed.Code)
}

func TestDesktopAcceptanceFlowExpired(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	secret := seedDesktopTOTP(t, user)
	flowToken := desktopAcceptanceChallenge(t, user)
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("user_id = ?", user.Id).
		Update("expires_at", time.Now().Add(-time.Hour)).Error)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	verifyBody, err := common.Marshal(map[string]string{"flow_token": flowToken, "method": "totp", "code": code})
	require.NoError(t, err)
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(verifyBody), "", service.AuthIdentity{}, DesktopVerify)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_FLOW_EXPIRED", parsed.Code)
}

func TestDesktopAcceptanceVerifyInvalidArgument(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	cases := []map[string]string{
		{"flow_token": "", "method": "totp", "code": "123456"},
		{"flow_token": "some-token", "method": "totp", "code": ""},
		{"flow_token": "some-token", "method": "sms", "code": "123456"},
	}
	for _, body := range cases {
		payload, err := common.Marshal(body)
		require.NoError(t, err)
		response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", string(payload), "", service.AuthIdentity{}, DesktopVerify)
		assert.Equal(t, http.StatusBadRequest, response.Code, body)
		parsed := decodeDesktopAcceptanceResponse(t, response)
		assert.Equal(t, "INVALID_ARGUMENT", parsed.Code)
	}
}

// ---------------------------------------------------------------------------
// 4. Session / refresh / logout
// ---------------------------------------------------------------------------

func TestDesktopAcceptanceRefreshRotatesToken(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	first := decodeDesktopAcceptanceBundle(t, login)
	require.NotEmpty(t, first.RefreshToken)
	require.NotEmpty(t, first.Session.SID)

	refreshBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: first.RefreshToken, SID: first.Session.SID})
	require.NoError(t, err)
	refresh := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(refreshBody), "", service.AuthIdentity{}, DesktopRefresh)
	require.Equal(t, http.StatusOK, refresh.Code, refresh.Body.String())
	second := decodeDesktopAcceptanceBundle(t, refresh)
	require.NotEmpty(t, second.RefreshToken)
	assert.NotEqual(t, first.RefreshToken, second.RefreshToken, "refresh token must rotate")

	// The previous token remains accepted inside the replay grace window.
	replayBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: first.RefreshToken, SID: first.Session.SID})
	require.NoError(t, err)
	replay := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(replayBody), "", service.AuthIdentity{}, DesktopRefresh)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	replayed := decodeDesktopAcceptanceBundle(t, replay)
	assert.Equal(t, second.RefreshToken, replayed.RefreshToken, "replayed old token must resolve to the current token")
}

func TestDesktopAcceptanceRefreshCrossClientRejected(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	web, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	require.NoError(t, err)
	refreshBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: web.RefreshToken, SID: web.Session.SID})
	require.NoError(t, err)
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(refreshBody), "", service.AuthIdentity{}, DesktopRefresh)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_UNAUTHORIZED", parsed.Code)
}

func TestDesktopAcceptanceRefreshSIDMismatch(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	bundle := decodeDesktopAcceptanceBundle(t, login)
	refreshBody, err := common.Marshal(desktopRefreshRequest{
		RefreshToken: bundle.RefreshToken,
		SID:          "00000000-0000-0000-0000-000000000000",
	})
	require.NoError(t, err)
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(refreshBody), "", service.AuthIdentity{}, DesktopRefresh)
	assert.Equal(t, http.StatusConflict, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_SESSION_MISMATCH", parsed.Code)
}

func TestDesktopAcceptanceLogoutSuccess(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	bundle := decodeDesktopAcceptanceBundle(t, login)
	logoutBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
	require.NoError(t, err)
	logout := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/logout", string(logoutBody), "", service.AuthIdentity{}, DesktopLogout)
	require.Equal(t, http.StatusOK, logout.Code, logout.Body.String())
	var result struct {
		Data struct {
			LoggedOut bool `json:"logged_out"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(logout.Body.Bytes(), &result))
	assert.True(t, result.Data.LoggedOut)
	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
}

func TestDesktopAcceptanceLogoutMismatchedAccessToken(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	device := service.SessionDeviceMetadata{
		DeviceID: "install-1", DeviceName: "Workstation", Platform: "win32", Arch: "x64", ClientVersion: "1.0.0",
	}
	first, err := service.CreateDesktopLoginSession(user.Id, "password", "127.0.0.1", "desktop", device)
	require.NoError(t, err)
	second, err := service.CreateDesktopLoginSession(user.Id, "password", "127.0.0.1", "desktop", device)
	require.NoError(t, err)
	body, err := common.Marshal(desktopRefreshRequest{RefreshToken: first.RefreshToken, SID: first.Session.SID})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/desktop/auth/logout", strings.NewReader(string(body)))
	c.Request.Header.Set("Authorization", "Bearer "+second.AccessToken)
	DesktopLogout(c)
	assert.Equal(t, http.StatusConflict, recorder.Code)
	parsed := decodeDesktopAcceptanceResponse(t, recorder)
	assert.Equal(t, "AUTH_SESSION_MISMATCH", parsed.Code)
	stored, err := model.GetUserSessionBySID(first.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusActive, stored.Status, "session must not be revoked on mismatch")
}

func TestDesktopAcceptanceLogoutInvalidRefreshToken(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	logoutBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: "not-a-refresh-token", SID: "not-a-sid"})
	require.NoError(t, err)
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/logout", string(logoutBody), "", service.AuthIdentity{}, DesktopLogout)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_UNAUTHORIZED", parsed.Code)
}

// ---------------------------------------------------------------------------
// 5. Rate limiting
// ---------------------------------------------------------------------------

func TestDesktopAcceptanceSessionIssuanceLimit(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.UserSessionIssuanceLimit = 1
	first := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := loginDesktopAcceptance(t, nil)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
	parsed := decodeDesktopAcceptanceResponse(t, second)
	assert.Equal(t, "AUTH_SESSION_ISSUANCE_LIMIT", parsed.Code)
}

func TestDesktopAcceptanceSessionActiveLimit(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.UserSessionActiveLimit = 1
	first := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := loginDesktopAcceptance(t, nil)
	assert.Equal(t, http.StatusConflict, second.Code)
	parsed := decodeDesktopAcceptanceResponse(t, second)
	assert.Equal(t, "AUTH_SESSION_LIMIT", parsed.Code)
}

// ---------------------------------------------------------------------------
// 6. Audit log secrecy
// ---------------------------------------------------------------------------

func desktopAuditLogStrings(t *testing.T) (content string, other string) {
	t.Helper()
	var logs []model.AuditLog
	require.NoError(t, model.LOG_DB.Order("id").Find(&logs).Error)
	require.NotEmpty(t, logs, "a successful login must write an audit log")
	var contents strings.Builder
	var others strings.Builder
	for _, log := range logs {
		contents.WriteString(log.Content)
		encoded, err := common.Marshal(log.Other)
		require.NoError(t, err)
		others.Write(encoded)
	}
	return contents.String(), others.String()
}

func TestDesktopAcceptanceAuditExcludesPassword(t *testing.T) {
	user := setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	content, other := desktopAuditLogStrings(t)
	assert.NotContains(t, content, "desktop-password")
	assert.NotContains(t, other, "desktop-password")
	assert.NotContains(t, content, stored.Password, "audit log must not store the password hash")
	assert.NotContains(t, other, stored.Password)
}

func TestDesktopAcceptanceAuditExcludesEncryptedPassword(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	keyID := enableDesktopEncryption(t)
	_, publicKeyPEM := common.PasswordEncryptionPublicKey()
	ciphertext := encryptDesktopPasswordV2(t, keyID, publicKeyPEM, "desktop-password")
	login := loginDesktopAcceptance(t, map[string]any{
		"password":           nil,
		"password_encrypted": ciphertext,
		"encryption_key_id":  keyID,
	})
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	content, other := desktopAuditLogStrings(t)
	assert.NotContains(t, content, ciphertext, "audit log must not store the encrypted password envelope")
	assert.NotContains(t, other, ciphertext)
	assert.NotContains(t, content, "desktop-password")
	assert.NotContains(t, other, "desktop-password")
}

// ---------------------------------------------------------------------------
// 7. Response headers and uniform shell
// ---------------------------------------------------------------------------

func TestDesktopAcceptanceNoStoreHeader(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	assert.Equal(t, "no-store", login.Header().Get("Cache-Control"))
	bundle := decodeDesktopAcceptanceBundle(t, login)
	refreshBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
	require.NoError(t, err)
	refresh := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(refreshBody), "", service.AuthIdentity{}, DesktopRefresh)
	require.Equal(t, http.StatusOK, refresh.Code, refresh.Body.String())
	assert.Equal(t, "no-store", refresh.Header().Get("Cache-Control"))
}

func TestDesktopAcceptanceResponseShellFields(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	var raw map[string]any
	require.NoError(t, common.Unmarshal(login.Body.Bytes(), &raw))
	for _, field := range []string{"success", "code", "message", "request_id", "server_time", "data"} {
		_, ok := raw[field]
		assert.True(t, ok, "response must contain %q", field)
	}
	assert.NotEmpty(t, raw["request_id"])
	serverTime, ok := raw["server_time"].(float64)
	require.True(t, ok, "server_time must be a JSON number")
	assert.Greater(t, int64(serverTime), int64(0))
}

func TestDesktopAcceptanceNoSetCookie(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	login := loginDesktopAcceptance(t, nil)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	assert.Empty(t, login.Header().Values("Set-Cookie"), "desktop login must not set cookies")
	bundle := decodeDesktopAcceptanceBundle(t, login)
	refreshBody, err := common.Marshal(desktopRefreshRequest{RefreshToken: bundle.RefreshToken, SID: bundle.Session.SID})
	require.NoError(t, err)
	refresh := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/refresh", string(refreshBody), "", service.AuthIdentity{}, DesktopRefresh)
	require.Equal(t, http.StatusOK, refresh.Code, refresh.Body.String())
	assert.Empty(t, refresh.Header().Values("Set-Cookie"), "desktop refresh must not set cookies")
}

// ---------------------------------------------------------------------------
// 8. Input validation
// ---------------------------------------------------------------------------

func TestDesktopAcceptanceMissingRequiredFields(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	cases := []map[string]any{
		{"device_id": nil},
		{"device_name": nil},
		{"platform": nil},
		{"arch": nil},
		{"client_version": nil},
		{"username": nil},
	}
	for _, mods := range cases {
		response := loginDesktopAcceptance(t, mods)
		assert.Equal(t, http.StatusBadRequest, response.Code, mods)
		parsed := decodeDesktopAcceptanceResponse(t, response)
		assert.Equal(t, "INVALID_ARGUMENT", parsed.Code)
	}
}

func TestDesktopAcceptanceInvalidDeviceMetadata(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	cases := []map[string]any{
		{"platform": "linux"},
		{"arch": "ia32"},
	}
	for _, mods := range cases {
		response := loginDesktopAcceptance(t, mods)
		assert.Equal(t, http.StatusBadRequest, response.Code, mods)
		parsed := decodeDesktopAcceptanceResponse(t, response)
		assert.Equal(t, "INVALID_ARGUMENT", parsed.Code)
	}
}

func TestDesktopAcceptancePasswordLoginDisabled(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	common.PasswordLoginEnabled = false
	response := loginDesktopAcceptance(t, nil)
	assert.Equal(t, http.StatusForbidden, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_PASSWORD_LOGIN_DISABLED", parsed.Code)
}

func TestDesktopAcceptanceInvalidJSON(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/login", "{not-json", "", service.AuthIdentity{}, DesktopLogin)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "INVALID_ARGUMENT", parsed.Code)
}

// ---------------------------------------------------------------------------
// 9. Internal error mapping
// ---------------------------------------------------------------------------

// ValidateAndFill deliberately maps any credential lookup failure to 401
// AUTH_INVALID_CREDENTIALS, so the 500 AUTH_INTERNAL_ERROR path is exercised on
// the verify endpoint, where a database outage is not a credential error.
func TestDesktopAcceptanceInternalErrorOnDBFailure(t *testing.T) {
	setupDesktopAuthAcceptanceTest(t)
	closedDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := closedDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	previousDB := model.DB
	model.DB = closedDB
	t.Cleanup(func() { model.DB = previousDB })

	body := `{"flow_token":"any-flow-token","method":"totp","code":"123456"}`
	response := securityEnrollmentRequest(http.MethodPost, "/api/desktop/auth/verify", body, "", service.AuthIdentity{}, DesktopVerify)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	parsed := decodeDesktopAcceptanceResponse(t, response)
	assert.Equal(t, "AUTH_INTERNAL_ERROR", parsed.Code)
}
