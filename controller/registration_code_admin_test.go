package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRegistrationCodeAdminTest(t *testing.T) (*model.User, service.AuthIdentity, *miniredis.Miniredis) {
	t.Helper()
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.RegistrationCodeRecord{}))
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	previousClient, previousRedis := common.RDB, common.RedisEnabled
	previousEnabled := common.RegistrationCodeEnabled.Load()
	previousKey, previousTTL := common.RegistrationCodeAPIKey, common.RegistrationCodeTTLSeconds
	previousOptions := common.OptionMap
	server := miniredis.RunT(t)
	server.SetTime(time.Now())
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RDB, common.RedisEnabled = client, true
	common.RegistrationCodeEnabled.Store(false)
	common.RegistrationCodeAPIKey = "registration-code-admin-test-key-at-least-32"
	common.RegistrationCodeTTLSeconds = 1800
	model.InitOptionMap()
	t.Cleanup(func() {
		common.RDB, common.RedisEnabled = previousClient, previousRedis
		common.RegistrationCodeEnabled.Store(previousEnabled)
		common.RegistrationCodeAPIKey, common.RegistrationCodeTTLSeconds = previousKey, previousTTL
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
		require.NoError(t, client.Close())
	})
	return user, identity, server
}

func registrationCodeAdminRequest(t *testing.T, method, body, proof string, identity *service.AuthIdentity) *httptest.ResponseRecorder {
	t.Helper()
	return registrationCodeManagementRequest(t, method, "/api/option/registration-codes", body, proof, identity)
}

func registrationCodeManagementRequest(t *testing.T, method, path, body, proof string, identity *service.AuthIdentity) *httptest.ResponseRecorder {
	t.Helper()
	engine := gin.New()
	group := engine.Group("/api/option", middleware.RootAuth(), middleware.DisableCache())
	group.GET("/registration-codes", GetRegistrationCodeSettings)
	group.PUT("/registration-codes", UpdateRegistrationCodeSettings)
	admin := engine.Group("/api/registration-codes", middleware.AdminAuth(), middleware.DisableCache())
	admin.GET("/", ListRegistrationCodes)
	admin.GET("/settings", GetRegistrationCodeSettings)
	admin.POST("/", GenerateRegistrationCodesForDashboard)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Security-Proof", proof)
	if identity != nil {
		token, _, err := service.IssueAccessToken(*identity)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

func TestRegistrationCodeAdminPermissionsAndProofBinding(t *testing.T) {
	for _, scenario := range []string{"anonymous", "ordinary user", "missing proof", "wrong scope", "changed context", "expired proof", "other session"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity, _ := setupRegistrationCodeAdminTest(t)
			operation := service.VerificationOperation{Scope: service.VerificationScopeRegistrationCodeSettings, Context: []byte(`{"enabled":true}`)}
			proof := ""
			requestIdentity := &identity
			switch scenario {
			case "anonymous":
				requestIdentity = nil
			case "ordinary user":
				require.NoError(t, model.DB.Model(user).Update("role", common.RoleCommonUser).Error)
				require.NoError(t, model.PublishUserAuthCache(user.Id))
			case "wrong scope":
				proof = issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopePasswordChange}, service.VerificationMethodPassword)
			case "changed context":
				operation.Context = []byte(`{"enabled":false}`)
				proof = issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
			case "expired proof", "other session":
				proof = issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
				if scenario == "expired proof" {
					require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeSecurityProof).Update("expires_at", time.Now().Add(-time.Minute)).Error)
				} else {
					bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-session")
					require.NoError(t, err)
					other, err := service.ParseAccessToken(bundle.AccessToken)
					require.NoError(t, err)
					requestIdentity = &other
				}
			}
			response := registrationCodeAdminRequest(t, http.MethodPut, `{"enabled":true}`, proof, requestIdentity)
			assert.False(t, common.RegistrationCodeEnabled.Load())
			assert.Contains(t, response.Body.String(), `"success":false`)
			if scenario == "anonymous" {
				assert.Equal(t, http.StatusUnauthorized, response.Code)
			} else {
				assert.Equal(t, http.StatusForbidden, response.Code)
			}
			var count int64
			require.NoError(t, model.DB.Model(&model.Option{}).Where(map[string]any{"key": "RegistrationCodeEnabled"}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestRegistrationCodeAdminTogglePersistsAndRejectsReplay(t *testing.T) {
	_, identity, _ := setupRegistrationCodeAdminTest(t)
	for _, enabled := range []bool{true, false} {
		context, err := common.Marshal(service.RegistrationCodeSettingsContext{Enabled: enabled})
		require.NoError(t, err)
		proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeRegistrationCodeSettings, Context: context}, service.VerificationMethodPassword)
		response := registrationCodeAdminRequest(t, http.MethodPut, string(context), proof, &identity)
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), `"success":true`)
		assert.Equal(t, enabled, common.RegistrationCodeEnabled.Load())
		var stored model.Option
		require.NoError(t, model.DB.First(&stored, model.Option{Key: "RegistrationCodeEnabled"}).Error)
		assert.Equal(t, enabled, stored.Value == "true")
		// Restart-style option initialization must override the environment default.
		common.RegistrationCodeEnabled.Store(!enabled)
		model.InitOptionMap()
		assert.Equal(t, enabled, common.RegistrationCodeEnabled.Load())
		model.InitOptionMap()
		assert.Equal(t, enabled, common.RegistrationCodeEnabled.Load())
		replay := registrationCodeAdminRequest(t, http.MethodPut, string(context), proof, &identity)
		assert.Contains(t, replay.Body.String(), "SECURITY_PROOF_CONSUMED")
		assert.Equal(t, enabled, common.RegistrationCodeEnabled.Load())
	}
	response := securityEnrollmentRequest(http.MethodPut, "/api/option/", `{"key":"RegistrationCodeEnabled","value":true}`, "", identity, UpdateOption)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.False(t, common.RegistrationCodeEnabled.Load())
}

func TestRegistrationCodeAdminFailedEnableKeepsPersistedAndRuntimeState(t *testing.T) {
	for _, scenario := range []string{"missing Redis", "short API key", "Redis unavailable", "database write failure"} {
		t.Run(scenario, func(t *testing.T) {
			_, identity, server := setupRegistrationCodeAdminTest(t)
			require.NoError(t, model.UpdateOptionsBulk(map[string]string{"RegistrationCodeEnabled": "false"}))
			proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeRegistrationCodeSettings, Context: []byte(`{"enabled":true}`)}, service.VerificationMethodPassword)
			switch scenario {
			case "missing Redis":
				common.RedisEnabled = false
			case "short API key":
				common.RegistrationCodeAPIKey = "short"
			case "Redis unavailable":
				server.SetError("ERR unavailable")
			case "database write failure":
				require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("registration-option-write-failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "options" {
						tx.AddError(errors.New("injected option write failure"))
					}
				}))
				t.Cleanup(func() { require.NoError(t, model.DB.Callback().Update().Remove("registration-option-write-failure")) })
			}
			response := registrationCodeAdminRequest(t, http.MethodPut, `{"enabled":true}`, proof, &identity)
			assert.Contains(t, response.Body.String(), `"success":false`)
			assert.False(t, common.RegistrationCodeEnabled.Load())
			var stored model.Option
			require.NoError(t, model.DB.First(&stored, model.Option{Key: "RegistrationCodeEnabled"}).Error)
			assert.Equal(t, "false", stored.Value)
		})
	}
}

func TestRegistrationCodeManagementRequiresAdministrator(t *testing.T) {
	for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser} {
		t.Run(strconv.Itoa(role), func(t *testing.T) {
			user, identity, _ := setupRegistrationCodeAdminTest(t)
			common.RegistrationCodeEnabled.Store(true)
			require.NoError(t, model.DB.Model(user).Update("role", role).Error)
			require.NoError(t, model.PublishUserAuthCache(user.Id))
			for _, endpoint := range []struct{ method, path, body string }{
				{http.MethodGet, "/api/registration-codes/", ""},
				{http.MethodGet, "/api/registration-codes/settings", ""},
				{http.MethodPost, "/api/registration-codes/", `{"count":1,"ttl_seconds":60}`},
			} {
				anonymous := registrationCodeManagementRequest(t, endpoint.method, endpoint.path, endpoint.body, "", nil)
				assert.Equal(t, http.StatusUnauthorized, anonymous.Code)
				response := registrationCodeManagementRequest(t, endpoint.method, endpoint.path, endpoint.body, "", &identity)
				if role == common.RoleCommonUser {
					assert.Equal(t, http.StatusForbidden, response.Code)
				} else {
					require.Equal(t, http.StatusOK, response.Code)
					assert.Contains(t, response.Body.String(), `"success":true`)
				}
			}
		})
	}
}

func TestRegistrationCodeManagementPersistsPrivateHistoryAndSingleUse(t *testing.T) {
	_, identity, server := setupRegistrationCodeAdminTest(t)
	common.RegistrationCodeEnabled.Store(true)
	response := registrationCodeManagementRequest(t, http.MethodPost, "/api/registration-codes/", `{"name":"Invitation","count":2,"ttl_seconds":60}`, "", &identity)
	require.Equal(t, http.StatusOK, response.Code)
	var result struct {
		Success bool
		Data    struct {
			Codes     []string
			ExpiresIn int `json:"expires_in"`
		}
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success)
	require.Len(t, result.Data.Codes, 2)
	assert.Equal(t, 60, result.Data.ExpiresIn)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var records []model.RegistrationCodeRecord
	require.NoError(t, model.DB.Order("id asc").Find(&records).Error)
	require.Len(t, records, 2)
	for i, record := range records {
		assert.Equal(t, "Invitation", record.Name)
		assert.Equal(t, identity.UserID, record.CreatedBy)
		assert.NotContains(t, record.Ciphertext, result.Data.Codes[i])
		plain, err := service.DecryptRegistrationCode(record.Ciphertext)
		require.NoError(t, err)
		assert.Equal(t, result.Data.Codes[i], plain)
	}
	list := registrationCodeManagementRequest(t, http.MethodGet, "/api/registration-codes/?p=1&page_size=1&keyword=Invitation", "", "", &identity)
	var page struct {
		Data struct {
			Items []registrationCodeListItem
			Total int
		}
	}
	require.NoError(t, common.Unmarshal(list.Body.Bytes(), &page))
	require.Len(t, page.Data.Items, 1)
	assert.Equal(t, 2, page.Data.Total)
	assert.Equal(t, result.Data.Codes[1], page.Data.Items[0].Code)
	assert.Equal(t, "unused", page.Data.Items[0].Status)
	assert.NotContains(t, list.Body.String(), "ciphertext")
	assert.NotContains(t, list.Body.String(), "digest")
	assert.Contains(t, list.Header().Get("Cache-Control"), "no-store")
	consumed, err := service.ConsumeRegistrationCode(context.Background(), result.Data.Codes[0])
	require.NoError(t, err)
	_, err = service.ConsumeRegistrationCode(context.Background(), result.Data.Codes[0])
	assert.ErrorIs(t, err, service.ErrRegistrationCodeInvalid)
	require.NoError(t, service.RestoreRegistrationCode(context.Background(), consumed))
	newer, err := service.ConsumeRegistrationCode(context.Background(), result.Data.Codes[0])
	require.NoError(t, err)
	// A delayed compensation for an older attempt cannot clear a newer use.
	require.NoError(t, model.ResetRegistrationCodeConsumption(consumed.Digest, consumed.UseRef))
	require.NoError(t, model.DB.First(&records[0], records[0].Id).Error)
	assert.Equal(t, newer.UseRef, records[0].UseRef)
	assert.Positive(t, records[0].UsedTime)
	server.FastForward(time.Minute)
	_, err = service.ConsumeRegistrationCode(context.Background(), result.Data.Codes[1])
	assert.ErrorIs(t, err, service.ErrRegistrationCodeInvalid)
	require.NoError(t, model.DB.Model(&records[1]).Update("expired_time", time.Now().Unix()-1).Error)
	list = registrationCodeManagementRequest(t, http.MethodGet, "/api/registration-codes/", "", "", &identity)
	require.NoError(t, common.Unmarshal(list.Body.Bytes(), &page))
	require.Len(t, page.Data.Items, 2)
	assert.Equal(t, "expired", page.Data.Items[0].Status)
	assert.Equal(t, "used", page.Data.Items[1].Status)
	settings := registrationCodeManagementRequest(t, http.MethodGet, "/api/registration-codes/settings", "", "", &identity)
	assert.Contains(t, settings.Body.String(), `"enabled":true`)
	assert.NotContains(t, settings.Body.String(), common.RegistrationCodeAPIKey)
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "registration_code.generate").Find(&audits).Error)
	require.NotEmpty(t, audits)
	auditJSON, err := common.Marshal(audits)
	require.NoError(t, err)
	for _, code := range result.Data.Codes {
		assert.NotContains(t, string(auditJSON), code)
	}
	assert.NotContains(t, string(auditJSON), common.RegistrationCodeAPIKey)
}

func TestRegistrationCodeLedgerFailureDoesNotPublishOrBurnCodes(t *testing.T) {
	_, identity, server := setupRegistrationCodeAdminTest(t)
	common.RegistrationCodeEnabled.Store(true)
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("registration-record-create-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "registration_code_records" {
			tx.AddError(errors.New("injected ledger write failure"))
		}
	}))
	response := registrationCodeManagementRequest(t, http.MethodPost, "/api/registration-codes/", `{"count":2,"ttl_seconds":60}`, "", &identity)
	assert.Contains(t, response.Body.String(), `"success":false`)
	for _, key := range server.Keys() {
		assert.NotContains(t, key, "registration:code:")
	}
	require.NoError(t, model.DB.Callback().Create().Remove("registration-record-create-failure"))
	response = registrationCodeManagementRequest(t, http.MethodPost, "/api/registration-codes/", `{"count":1,"ttl_seconds":60}`, "", &identity)
	var result struct {
		Success bool
		Data    struct{ Codes []string }
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success)
	require.Len(t, result.Data.Codes, 1)
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("registration-record-update-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "registration_code_records" {
			tx.AddError(errors.New("injected ledger update failure"))
		}
	}))
	_, err := service.ConsumeRegistrationCode(context.Background(), result.Data.Codes[0])
	assert.ErrorIs(t, err, service.ErrRegistrationCodeUnavailable)
	require.NoError(t, model.DB.Callback().Update().Remove("registration-record-update-failure"))
	var codeKeys int
	for _, key := range server.Keys() {
		if strings.HasPrefix(key, "registration:code:") {
			codeKeys++
		}
	}
	assert.Equal(t, 1, codeKeys)
	_, err = service.ConsumeRegistrationCode(context.Background(), result.Data.Codes[0])
	require.NoError(t, err)
}

func TestRegistrationCodeLedgerMigrationPreservesReleasedDataAndUniqueness(t *testing.T) {
	user, _, _ := setupRegistrationCodeAdminTest(t)
	// The user and option tables retain their released schemas; the ledger is new.
	require.NoError(t, model.DB.Migrator().DropTable(&model.RegistrationCodeRecord{}))
	require.NoError(t, model.UpdateOptionsBulk(map[string]string{"RegistrationCodeEnabled": "false"}))
	require.NoError(t, model.DB.AutoMigrate(&model.RegistrationCodeRecord{}))
	record := model.RegistrationCodeRecord{Name: "upgrade", Digest: strings.Repeat("a", 64), Ciphertext: "cipher", CreatedTime: 100, ExpiredTime: 200, CreatedBy: user.Id}
	require.NoError(t, model.SaveRegistrationCodeRecords([]model.RegistrationCodeRecord{record}))
	require.NoError(t, model.DB.AutoMigrate(&model.RegistrationCodeRecord{}))
	require.NoError(t, model.DB.AutoMigrate(&model.RegistrationCodeRecord{}))
	var preserved model.User
	require.NoError(t, model.DB.First(&preserved, user.Id).Error)
	assert.Equal(t, user.Username, preserved.Username)
	assert.Equal(t, user.Password, preserved.Password)
	var option model.Option
	require.NoError(t, model.DB.First(&option, model.Option{Key: "RegistrationCodeEnabled"}).Error)
	assert.Equal(t, "false", option.Value)
	records, total, err := model.GetRegistrationCodeRecords(1, 20, "upgrade")
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, records, 1)
	assert.Equal(t, "cipher", records[0].Ciphertext)
	assert.Error(t, model.SaveRegistrationCodeRecords([]model.RegistrationCodeRecord{record}), "digest uniqueness must survive repeated migration")
}

func TestRegistrationCodeLedgerRejectsTamperingAndKeepsValidityAfterKeyRotation(t *testing.T) {
	_, identity, _ := setupRegistrationCodeAdminTest(t)
	common.RegistrationCodeEnabled.Store(true)
	response := registrationCodeManagementRequest(t, http.MethodPost, "/api/registration-codes/", `{"name":"before","count":1,"ttl_seconds":60}`, "", &identity)
	var result struct {
		Success bool
		Data    struct{ Codes []string }
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success)
	require.Len(t, result.Data.Codes, 1)
	var record model.RegistrationCodeRecord
	require.NoError(t, model.DB.First(&record).Error)
	bytes, err := base64.RawStdEncoding.DecodeString(record.Ciphertext)
	require.NoError(t, err)
	bytes[len(bytes)-1] ^= 1
	_, err = service.DecryptRegistrationCode(base64.RawStdEncoding.EncodeToString(bytes))
	assert.ErrorIs(t, err, service.ErrRegistrationCodeUnavailable)
	common.RegistrationCodeAPIKey = "rotated-registration-code-admin-key-at-least-32"
	list := registrationCodeManagementRequest(t, http.MethodGet, "/api/registration-codes/", "", "", &identity)
	var page struct {
		Success bool
		Data    struct{ Items []registrationCodeListItem }
	}
	require.NoError(t, common.Unmarshal(list.Body.Bytes(), &page))
	require.True(t, page.Success)
	require.Len(t, page.Data.Items, 1)
	assert.Empty(t, page.Data.Items[0].Code)
	_, err = service.ConsumeRegistrationCode(context.Background(), result.Data.Codes[0])
	require.NoError(t, err, "history key rotation must not revoke a still-valid registration code")
	response = registrationCodeManagementRequest(t, http.MethodPost, "/api/registration-codes/", `{"name":"after","count":1,"ttl_seconds":60}`, "", &identity)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success)
	require.Len(t, result.Data.Codes, 1)
	list = registrationCodeManagementRequest(t, http.MethodGet, "/api/registration-codes/?keyword=after", "", "", &identity)
	require.NoError(t, common.Unmarshal(list.Body.Bytes(), &page))
	require.True(t, page.Success)
	require.Len(t, page.Data.Items, 1)
	assert.Equal(t, result.Data.Codes[0], page.Data.Items[0].Code)
}
