package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type generateRegistrationCodesRequest struct {
	Count      int    `json:"count"`
	TTLSeconds int    `json:"ttl_seconds"`
	Name       string `json:"name"`
}

type registrationCodeListItem struct {
	model.RegistrationCodeRecord
	Code   string `json:"code"`
	Status string `json:"status"`
}

func ListRegistrationCodes(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("p", "1"))
	if err != nil || page < 1 || page > 1000000 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	size, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	keyword := strings.TrimSpace(c.Query("keyword"))
	if err != nil || size < 1 || size > 100 || utf8.RuneCountInString(keyword) > 64 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	records, total, err := model.GetRegistrationCodeRecords(page, size, keyword)
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgDatabaseError)
		return
	}
	items := make([]registrationCodeListItem, 0, len(records))
	for _, record := range records {
		code, err := service.DecryptRegistrationCode(record.Ciphertext)
		if err != nil {
			code = ""
		}
		status := "unused"
		if record.UsedTime > 0 {
			status = "used"
		} else if record.ExpiredTime <= time.Now().Unix() {
			status = "expired"
		}
		items = append(items, registrationCodeListItem{RegistrationCodeRecord: record, Code: code, Status: status})
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": total})
}

func GetRegistrationCodeSettings(c *gin.Context) {
	maxCount, maxTTL := service.RegistrationCodeLimits()
	common.ApiSuccess(c, gin.H{
		"enabled":             common.RegistrationCodeEnabled.Load(),
		"available":           service.ValidateRegistrationCodeDependencies() == nil,
		"default_ttl_seconds": common.RegistrationCodeTTLSeconds,
		"max_count":           maxCount,
		"max_ttl_seconds":     maxTTL,
	})
}

func UpdateRegistrationCodeSettings(c *gin.Context) {
	params := map[string]any{"success": false}
	defer func() {
		recordUserSecurityAudit(c, c.GetInt("id"), "registration_code.settings", params)
		markAuditLogged(c)
	}()
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Enabled == nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	params["enabled"] = *request.Enabled
	context, err := common.Marshal(service.RegistrationCodeSettingsContext{Enabled: *request.Enabled})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeRegistrationCodeSettings, Context: context}) == nil {
		return
	}
	if *request.Enabled {
		if err := service.ValidateRegistrationCodeDependencies(); err != nil {
			common.ApiErrorMsg(c, "Registration code service is not configured. Contact your administrator.")
			return
		}
		if err := common.RDB.Ping(c.Request.Context()).Err(); err != nil {
			common.ApiErrorI18n(c, i18n.MsgRegistrationCodeUnavailable)
			return
		}
	}
	if err := model.UpdateOptionsBulk(map[string]string{"RegistrationCodeEnabled": strconv.FormatBool(*request.Enabled)}); err != nil {
		common.ApiErrorI18n(c, i18n.MsgDatabaseError)
		return
	}
	params["success"] = true
	common.ApiSuccess(c, gin.H{"enabled": common.RegistrationCodeEnabled.Load()})
}

func GenerateRegistrationCodes(c *gin.Context) {
	generateRegistrationCodes(c, false)
}

func GenerateRegistrationCodesForDashboard(c *gin.Context) {
	generateRegistrationCodes(c, true)
}

func generateRegistrationCodes(c *gin.Context, dashboard bool) {
	params := map[string]any{"success": false}
	if dashboard {
		defer func() {
			recordUserSecurityAudit(c, c.GetInt("id"), "registration_code.generate", params)
			markAuditLogged(c)
		}()
	}
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
	request.Name = strings.TrimSpace(request.Name)
	maxCount, maxTTLSeconds := service.RegistrationCodeLimits()
	if request.Count < 1 || request.Count > maxCount || request.TTLSeconds < 1 || request.TTLSeconds > maxTTLSeconds || utf8.RuneCountInString(request.Name) > 64 {
		common.ApiErrorMsg(c, "count or ttl_seconds is out of range")
		return
	}
	params["count"], params["ttl_seconds"] = request.Count, request.TTLSeconds
	createdAt := time.Now().Unix()
	codes, err := service.GenerateRegistrationCodes(c.Request.Context(), request.Count, time.Duration(request.TTLSeconds)*time.Second)
	if err != nil {
		common.SysError("failed to generate registration codes: " + err.Error())
		common.ApiErrorMsg(c, "registration code service is unavailable")
		return
	}
	if dashboard {
		published := false
		defer func() {
			if published {
				return
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
			defer cancel()
			if err := service.DiscardRegistrationCodes(cleanup, codes); err != nil {
				common.SysError("failed to revoke unpublished registration codes")
			}
		}()
		records := make([]model.RegistrationCodeRecord, 0, len(codes))
		for _, code := range codes {
			digest, _ := service.RegistrationCodeDigest(code)
			encrypted, err := service.EncryptRegistrationCode(code)
			if err != nil {
				common.ApiErrorMsg(c, "Registration code service is unavailable")
				return
			}
			records = append(records, model.RegistrationCodeRecord{Name: request.Name, Digest: digest, Ciphertext: encrypted, CreatedTime: createdAt, ExpiredTime: createdAt + int64(request.TTLSeconds), CreatedBy: c.GetInt("id")})
		}
		if err := model.SaveRegistrationCodeRecords(records); err != nil {
			common.ApiErrorI18n(c, i18n.MsgDatabaseError)
			return
		}
		published = true
	}
	params["success"] = true
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
