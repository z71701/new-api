package middleware

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type turnstileCheckResponse struct {
	Success bool `json:"success"`
}

var turnstileSiteVerify = func(response, remoteIP string) error {
	client := service.GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	rawRes, err := client.PostForm("https://challenges.cloudflare.com/turnstile/v0/siteverify", url.Values{
		"secret":   {common.TurnstileSecretKey},
		"response": {response},
		"remoteip": {remoteIP},
	})
	if err != nil {
		return err
	}
	defer rawRes.Body.Close()
	if rawRes.StatusCode < http.StatusOK || rawRes.StatusCode >= http.StatusMultipleChoices {
		return errors.New("turnstile verification service unavailable")
	}
	var result turnstileCheckResponse
	if err := common.DecodeJson(rawRes.Body, &result); err != nil {
		return err
	}
	if !result.Success {
		return ErrTurnstileRejected
	}
	return nil
}

var ErrTurnstileRejected = errors.New("turnstile token rejected")

// SetTurnstileVerifierForTest replaces the Turnstile site verification function.
// It is intended for integration tests only; production code must not call it.
// The returned restore function must be called to put the production verifier
// back in place.
func SetTurnstileVerifierForTest(fn func(response, remoteIP string) error) func() {
	original := turnstileSiteVerify
	turnstileSiteVerify = fn
	return func() { turnstileSiteVerify = original }
}

func ValidateTurnstileToken(response, remoteIP string) error {
	if !common.TurnstileCheckEnabled {
		return nil
	}
	if response == "" {
		return ErrTurnstileRejected
	}
	return turnstileSiteVerify(response, remoteIP)
}

func TurnstileCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		if common.TurnstileCheckEnabled {
			response := c.Query("turnstile")
			if response == "" {
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": "Turnstile token 为空",
				})
				c.Abort()
				return
			}
			if err := ValidateTurnstileToken(response, c.ClientIP()); err != nil {
				if !errors.Is(err, ErrTurnstileRejected) {
					common.SysLog(err.Error())
				}
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": "Turnstile 校验失败，请刷新重试！",
				})
				c.Abort()
				return
			}
		}
		c.Next()
	}
}
