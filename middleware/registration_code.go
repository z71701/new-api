package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func RegistrationCodeAPIAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !common.RegistrationCodeEnabled || common.RegistrationCodeAPIKey == "" {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"success": false, "message": "not found"})
			return
		}
		header := strings.TrimSpace(c.GetHeader("Authorization"))
		parts := strings.Fields(header)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(common.RegistrationCodeAPIKey)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "unauthorized"})
			return
		}
		c.Next()
	}
}
