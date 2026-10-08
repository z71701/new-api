package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type generateRegistrationCodesRequest struct {
	Count      int `json:"count"`
	TTLSeconds int `json:"ttl_seconds"`
}

func GenerateRegistrationCodes(c *gin.Context) {
	var request generateRegistrationCodesRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorMsg(c, "invalid request")
		return
	}
	if request.Count == 0 {
		request.Count = 1
	}
	if request.TTLSeconds == 0 {
		request.TTLSeconds = common.RegistrationCodeTTLSeconds
	}
	maxCount, maxTTLSeconds := service.RegistrationCodeLimits()
	if request.Count < 1 || request.Count > maxCount || request.TTLSeconds < 1 || request.TTLSeconds > maxTTLSeconds {
		common.ApiErrorMsg(c, "count or ttl_seconds is out of range")
		return
	}
	codes, err := service.GenerateRegistrationCodes(c.Request.Context(), request.Count, time.Duration(request.TTLSeconds)*time.Second)
	if err != nil {
		common.SysError("failed to generate registration codes: " + err.Error())
		common.ApiErrorMsg(c, "registration code service is unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"codes":      codes,
			"expires_in": request.TTLSeconds,
			"expires_at": time.Now().Add(time.Duration(request.TTLSeconds) * time.Second).Unix(),
		},
	})
}

func consumeRegistrationCode(c *gin.Context, code string) (*service.ConsumedRegistrationCode, bool) {
	consumed, err := service.ConsumeRegistrationCode(c.Request.Context(), code)
	if err == nil {
		return consumed, true
	}
	if errors.Is(err, service.ErrRegistrationCodeInvalid) {
		common.ApiErrorI18n(c, i18n.MsgRegistrationCodeInvalid)
		return nil, false
	}
	common.SysError("registration code consumption failed: " + err.Error())
	common.ApiErrorI18n(c, i18n.MsgRegistrationCodeUnavailable)
	return nil, false
}

func consumeRegistrationCodeDigest(c *gin.Context, digest string) (*service.ConsumedRegistrationCode, bool) {
	consumed, err := service.ConsumeRegistrationCodeDigest(c.Request.Context(), digest)
	if err == nil {
		return consumed, true
	}
	if errors.Is(err, service.ErrRegistrationCodeInvalid) {
		common.ApiErrorI18n(c, i18n.MsgRegistrationCodeInvalid)
		return nil, false
	}
	common.SysError("registration code consumption failed: " + err.Error())
	common.ApiErrorI18n(c, i18n.MsgRegistrationCodeUnavailable)
	return nil, false
}

func restoreRegistrationCode(c *gin.Context, consumed *service.ConsumedRegistrationCode) {
	if consumed == nil {
		return
	}
	// Database work can outlive the request. Compensation must still run after
	// cancellation, but must not block the handler indefinitely on Redis I/O.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
	defer cancel()
	if err := service.RestoreRegistrationCode(ctx, consumed); err != nil {
		common.SysError("failed to restore registration code: " + err.Error())
	}
}
