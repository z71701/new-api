package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type desktopLoginRequest struct {
	Username          string `json:"username"`
	Password          string `json:"password"`
	PasswordEncrypted string `json:"password_encrypted"`
	EncryptionKeyID   string `json:"encryption_key_id"`
	DeviceID          string `json:"device_id"`
	DeviceName        string `json:"device_name"`
	Platform          string `json:"platform"`
	Arch              string `json:"arch"`
	ClientVersion     string `json:"client_version"`
	CaptchaToken      string `json:"captcha_token"`
}

type desktopRefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
	SID          string `json:"sid"`
}

type desktopUserView struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

func DesktopLogin(c *gin.Context) {
	setAuthNoStore(c)
	if !common.PasswordLoginEnabled {
		writeDesktopAuthResponse(c, http.StatusForbidden, false, "AUTH_PASSWORD_LOGIN_DISABLED", "Password login is disabled", nil)
		return
	}
	var request desktopLoginRequest
	if common.DecodeJson(c.Request.Body, &request) != nil {
		writeDesktopAuthResponse(c, http.StatusBadRequest, false, "INVALID_ARGUMENT", "Invalid request", nil)
		return
	}
	if desktopLoginRequiredFieldsMissing(request) {
		writeDesktopAuthResponse(c, http.StatusBadRequest, false, "INVALID_ARGUMENT", "Invalid request", nil)
		return
	}
	device := service.SessionDeviceMetadata{
		DeviceID: request.DeviceID, DeviceName: request.DeviceName, Platform: request.Platform,
		Arch: request.Arch, ClientVersion: request.ClientVersion,
	}
	if _, err := service.NewDesktopSessionPolicy(device); err != nil {
		writeDesktopAuthResponse(c, http.StatusBadRequest, false, "INVALID_ARGUMENT", "Invalid device metadata", nil)
		return
	}
	if common.TurnstileCheckEnabled {
		if request.CaptchaToken == "" {
			writeDesktopAuthResponse(c, http.StatusForbidden, false, "AUTH_CAPTCHA_REQUIRED", "Captcha is required", nil)
			return
		}
		if err := middleware.ValidateTurnstileToken(request.CaptchaToken, c.ClientIP()); err != nil {
			if errors.Is(err, middleware.ErrTurnstileRejected) {
				writeDesktopAuthResponse(c, http.StatusBadRequest, false, "AUTH_CAPTCHA_INVALID", "Captcha is invalid", nil)
			} else {
				writeDesktopAuthResponse(c, http.StatusServiceUnavailable, false, "AUTH_CAPTCHA_UNAVAILABLE", "Captcha service is unavailable", nil)
			}
			return
		}
	}
	password := request.Password
	if common.PasswordLoginEncryptionEnabled {
		if request.PasswordEncrypted == "" || request.EncryptionKeyID == "" || request.Password != "" {
			writeDesktopAuthResponse(c, http.StatusBadRequest, false, "AUTH_ENCRYPTION_PAYLOAD_INVALID", "Password encryption payload is invalid", nil)
			return
		}
		var err error
		password, err = common.DecryptPassword(request.PasswordEncrypted, request.EncryptionKeyID)
		if err != nil {
			if errors.Is(err, common.ErrPasswordEncryptionKeyStale) {
				writeDesktopAuthResponse(c, http.StatusConflict, false, "AUTH_ENCRYPTION_KEY_STALE", "Password encryption key is stale", nil)
			} else {
				writeDesktopAuthResponse(c, http.StatusBadRequest, false, "AUTH_ENCRYPTION_PAYLOAD_INVALID", "Password encryption payload is invalid", nil)
			}
			return
		}
	} else if request.PasswordEncrypted != "" || request.EncryptionKeyID != "" {
		writeDesktopAuthResponse(c, http.StatusBadRequest, false, "INVALID_ARGUMENT", "Invalid request", nil)
		return
	}
	if password == "" {
		writeDesktopAuthResponse(c, http.StatusBadRequest, false, "INVALID_ARGUMENT", "Invalid request", nil)
		return
	}
	user := model.User{Username: request.Username, Password: password}
	if err := user.ValidateAndFill(); err != nil {
		writeDesktopAuthResponse(c, http.StatusUnauthorized, false, "AUTH_INVALID_CREDENTIALS", "Invalid username or password", nil)
		return
	}
	challenge, err := service.StartDesktopLoginVerification(&user, "password", device)
	if err != nil {
		if errors.Is(err, service.ErrVerificationUnavailable) {
			writeDesktopAuthResponse(c, http.StatusForbidden, false, "AUTH_VERIFICATION_UNSUPPORTED", "Verification method is unsupported", nil)
			return
		}
		writeDesktopAuthError(c, err)
		return
	}
	if challenge != nil {
		methods := make([]string, 0, 1)
		for _, option := range challenge.Methods {
			if option.Method == service.VerificationMethodTwoFA && option.Available {
				methods = append(methods, "totp")
			}
		}
		if len(methods) == 0 {
			writeDesktopAuthResponse(c, http.StatusForbidden, false, "AUTH_VERIFICATION_UNSUPPORTED", "Verification method is unsupported", nil)
			return
		}
		writeDesktopAuthResponse(c, http.StatusOK, true, "AUTH_VERIFICATION_REQUIRED", "Verification required", gin.H{
			"require_verification": true, "flow_token": challenge.FlowToken, "expires_at": challenge.ExpiresAt, "methods": methods,
		})
		return
	}
	bundle, err := service.CreateDesktopLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "password", c.ClientIP(), c.Request.UserAgent(), device)
	if err != nil {
		writeDesktopAuthError(c, err)
		return
	}
	writeDesktopLoginSuccess(c, &user, bundle, "")
}

func DesktopVerify(c *gin.Context) {
	setAuthNoStore(c)
	var request struct {
		FlowToken string `json:"flow_token"`
		Method    string `json:"method"`
		Code      string `json:"code"`
	}
	if common.DecodeJson(c.Request.Body, &request) != nil || request.FlowToken == "" || request.Method != "totp" || request.Code == "" {
		writeDesktopAuthResponse(c, http.StatusBadRequest, false, "INVALID_ARGUMENT", "Invalid request", nil)
		return
	}
	bundle, err := service.VerifyDesktopLoginCode(request.FlowToken, request.Code, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeDesktopVerificationError(c, err)
		return
	}
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	if err != nil {
		writeDesktopAuthError(c, err)
		return
	}
	user, err := model.GetSelfUserById(identity.UserID)
	if err != nil {
		writeDesktopAuthError(c, err)
		return
	}
	writeDesktopLoginSuccess(c, user, bundle, service.VerificationMethodTwoFA)
}

func DesktopRefresh(c *gin.Context) {
	setAuthNoStore(c)
	var request desktopRefreshRequest
	if common.DecodeJson(c.Request.Body, &request) != nil || request.RefreshToken == "" || request.SID == "" {
		writeDesktopAuthResponse(c, http.StatusBadRequest, false, "INVALID_ARGUMENT", "Invalid request", nil)
		return
	}
	bundle, user, err := service.RefreshDesktopLoginSession(request.RefreshToken, request.SID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeDesktopAuthError(c, err)
		return
	}
	writeDesktopAuthResponse(c, http.StatusOK, true, "OK", "", desktopAuthBundleData(user, bundle))
}

func DesktopLogout(c *gin.Context) {
	setAuthNoStore(c)
	var request desktopRefreshRequest
	if common.DecodeJson(c.Request.Body, &request) != nil || request.RefreshToken == "" || request.SID == "" {
		writeDesktopAuthResponse(c, http.StatusBadRequest, false, "INVALID_ARGUMENT", "Invalid request", nil)
		return
	}
	if rawAccessToken, ok := dashboardBearer(c.GetHeader("Authorization")); ok {
		identity, err := service.ParseAccessToken(rawAccessToken)
		if err != nil || service.DesktopRefreshTokenMatchesAccess(request.RefreshToken, identity) != nil {
			writeDesktopAuthResponse(c, http.StatusConflict, false, "AUTH_SESSION_MISMATCH", "Session credentials do not match", nil)
			return
		}
	}
	if err := service.RevokeDesktopByRefreshTokenStrict(request.RefreshToken, request.SID, "desktop_logout"); err != nil {
		writeDesktopAuthError(c, err)
		return
	}
	writeDesktopAuthResponse(c, http.StatusOK, true, "OK", "", gin.H{"logged_out": true})
}

func writeDesktopLoginSuccess(c *gin.Context, user *model.User, bundle *service.AuthBundle, verificationMethod string) {
	c.Set("login_method", bundle.Session.LoginMethod)
	if verificationMethod != "" {
		c.Set("login_verification_method", verificationMethod)
	}
	model.UpdateUserLastLoginAt(user.Id)
	recordLoginAudit(user, c)
	writeDesktopAuthResponse(c, http.StatusOK, true, "OK", "", desktopAuthBundleData(user, bundle))
}

func desktopAuthBundleData(user *model.User, bundle *service.AuthBundle) gin.H {
	return gin.H{
		"access_token": bundle.AccessToken, "refresh_token": bundle.RefreshToken, "token_type": bundle.TokenType,
		"access_expires_at": bundle.AccessExpiresAt, "session": bundle.Session,
		"user": desktopUserView{ID: strconv.Itoa(user.Id), Username: user.Username, DisplayName: user.DisplayName},
	}
}

func writeDesktopVerificationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrAuthFlowExpired), errors.Is(err, model.ErrAuthFlowConsumed), errors.Is(err, model.ErrAuthFlowInvalid):
		writeDesktopAuthResponse(c, http.StatusUnauthorized, false, "AUTH_FLOW_EXPIRED", "Verification flow expired", nil)
	case errors.Is(err, service.ErrVerificationFailed), errors.Is(err, service.ErrVerificationLocked), errors.Is(err, model.ErrTwoFANotEnabled):
		writeDesktopAuthResponse(c, http.StatusUnauthorized, false, "AUTH_VERIFICATION_FAILED", "Verification failed", nil)
	case errors.Is(err, service.ErrVerificationUnavailable), errors.Is(err, service.ErrProofMethod):
		writeDesktopAuthResponse(c, http.StatusForbidden, false, "AUTH_VERIFICATION_UNSUPPORTED", "Verification method is unsupported", nil)
	default:
		writeDesktopAuthError(c, err)
	}
}

func writeDesktopAuthError(c *gin.Context, err error) {
	status, code := service.AuthSessionErrorCode(err)
	writeDesktopAuthResponse(c, status, false, code, http.StatusText(status), nil)
}

func writeDesktopAuthResponse(c *gin.Context, status int, success bool, code, message string, data any) {
	requestID := c.GetString(common.RequestIdKey)
	if requestID == "" {
		requestID = common.NewRequestId()
	}
	c.JSON(status, gin.H{
		"success": success, "code": code, "message": message, "request_id": requestID,
		"server_time": time.Now().Unix(), "data": data,
	})
}

func desktopLoginRequiredFieldsMissing(request desktopLoginRequest) bool {
	return strings.TrimSpace(request.Username) == "" || strings.TrimSpace(request.DeviceID) == "" ||
		strings.TrimSpace(request.DeviceName) == "" || strings.TrimSpace(request.Platform) == "" ||
		strings.TrimSpace(request.Arch) == "" || strings.TrimSpace(request.ClientVersion) == ""
}
