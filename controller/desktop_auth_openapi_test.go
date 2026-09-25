package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// openAPIDoc is the subset of docs/desktop-auth-openapi.yaml this drift test
// needs. It is intentionally shallow: the test compares declarations against
// source-derived facts rather than re-modeling the whole spec.
type openAPIDoc struct {
	Paths map[string]struct {
		Post *struct {
			OperationID string `yaml:"operationId"`
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Ref string `yaml:"$ref"`
					} `yaml:"schema"`
				} `yaml:"content"`
			} `yaml:"requestBody"`
			Responses map[string]map[string]any `yaml:"responses"`
		} `yaml:"post"`
	} `yaml:"paths"`
	Components struct {
		Schemas map[string]struct {
			Properties map[string]map[string]any `yaml:"properties"`
			Required   []string                  `yaml:"required"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

// desktopHandlerForPath maps each documented path to the handler function that
// implements it in controller/desktop_auth.go.
var desktopHandlerForPath = map[string]string{
	"/api/desktop/auth/login":   "DesktopLogin",
	"/api/desktop/auth/verify":  "DesktopVerify",
	"/api/desktop/auth/refresh": "DesktopRefresh",
	"/api/desktop/auth/logout":  "DesktopLogout",
}

var httpStatusNameToCode = map[string]int{
	"StatusOK":                  200,
	"StatusBadRequest":          400,
	"StatusUnauthorized":        401,
	"StatusForbidden":           403,
	"StatusConflict":            409,
	"StatusTooManyRequests":     429,
	"StatusInternalServerError": 500,
	"StatusServiceUnavailable":  503,
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Test file lives at <root>/controller/desktop_auth_openapi_test.go.
	wd, err := os.Getwd()
	require.NoError(t, err)
	dir := wd
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "docs", "desktop-auth-openapi.yaml")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "router", "api-router.go")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("cannot locate repository root from %s", wd)
	return ""
}

// readRepoFile reads a file relative to the repository root.
func readRepoFile(t *testing.T, root, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	require.NoError(t, err)
	return string(raw)
}

// extractHandlerBody returns the source text of a top-level handler function
// (up to the next top-level "func" declaration).
func extractHandlerBody(t *testing.T, source, handler string) string {
	t.Helper()
	start := strings.Index(source, "func "+handler+"(c *gin.Context) {")
	require.GreaterOrEqual(t, start, 0, "handler %s not found in desktop_auth.go", handler)
	rest := source[start:]
	// Skip the handler's own declaration, then find the next top-level func.
	afterDecl := strings.Index(rest[1:], "\nfunc ")
	if afterDecl < 0 {
		return rest
	}
	return rest[:afterDecl+1]
}

// authSessionErrorStatuses returns the HTTP status codes emitted by
// service.AuthSessionErrorCode, i.e. everything a handler can produce via
// writeDesktopAuthError.
func authSessionErrorStatuses(t *testing.T, serviceSource string) map[int]bool {
	t.Helper()
	re := regexp.MustCompile(`return http\.(Status\w+),`)
	out := map[int]bool{}
	for _, match := range re.FindAllStringSubmatch(serviceSource, -1) {
		if code, ok := httpStatusNameToCode[match[1]]; ok {
			out[code] = true
		}
	}
	require.NotEmpty(t, out, "expected at least one mapped status in authSessionErrorCode")
	return out
}

func TestDesktopAuthOpenAPIDrift(t *testing.T) {
	root := repoRoot(t)

	specBytes, err := os.ReadFile(filepath.Join(root, "docs", "desktop-auth-openapi.yaml"))
	require.NoError(t, err)
	var doc openAPIDoc
	require.NoError(t, yaml.Unmarshal(specBytes, &doc), "spec must be valid YAML")

	routerSource := readRepoFile(t, root, "router/api-router.go")
	controllerSource := readRepoFile(t, root, "controller/desktop_auth.go")
	serviceSource := readRepoFile(t, root, "service/auth_session.go")
	errorStatuses := authSessionErrorStatuses(t, serviceSource)

	// 1. All four documented paths exist, are POST, have an operationId, and
	//    match the router registrations in router/api-router.go.
	require.Len(t, doc.Paths, len(desktopHandlerForPath), "spec must document exactly the four desktop auth paths")
	for path, handler := range desktopHandlerForPath {
		entry, ok := doc.Paths[path]
		require.True(t, ok, "spec is missing path %s", path)
		require.NotNil(t, entry.Post, "path %s must be POST", path)
		require.NotEmpty(t, entry.Post.OperationID, "path %s must declare operationId", path)

		// /api/desktop/auth/login -> desktopAuthRoute.POST("/login", ...)
		suffix := strings.TrimPrefix(path, "/api/desktop/auth/")
		registration := fmt.Sprintf(`desktopAuthRoute.POST("/%s"`, suffix)
		assert.Contains(t, routerSource, registration,
			"router must register %s with handler %s", path, handler)
	}

	// 2. Declared response status codes must be a superset of the status codes
	//    each handler can actually return.
	writeResponseRe := regexp.MustCompile(`writeDesktopAuthResponse\(c,\s*http\.(Status\w+)`)
	for path, handler := range desktopHandlerForPath {
		entry := doc.Paths[path].Post

		declared := map[int]bool{}
		for codeStr := range entry.Responses {
			code, err := strconv.Atoi(codeStr)
			require.NoError(t, err, "response key %q on %s must be a numeric HTTP status", codeStr, path)
			declared[code] = true
		}

		actual := map[int]bool{}
		body := extractHandlerBody(t, controllerSource, handler)
		for _, match := range writeResponseRe.FindAllStringSubmatch(body, -1) {
			code, ok := httpStatusNameToCode[match[1]]
			require.True(t, ok, "unmapped http status %s in %s", match[1], handler)
			actual[code] = true
		}
		// writeDesktopAuthError / writeDesktopVerificationError delegate to
		// AuthSessionErrorCode, whose reachable set we parsed from the service.
		if strings.Contains(body, "writeDesktopAuthError") || strings.Contains(body, "writeDesktopVerificationError") {
			for code := range errorStatuses {
				actual[code] = true
			}
		}
		// Every route sits behind DesktopAuthRateLimit, which can emit 429.
		actual[429] = true

		for code := range actual {
			assert.True(t, declared[code],
				"spec for %s must declare status %d (handler %s can return it)", path, code, handler)
		}
		assert.NotEmpty(t, declared[200], "spec for %s must declare a 200 response", path)
	}

	// 3. The unified envelope's six top-level fields appear in both Envelope
	//    and ErrorResponse schemas.
	envelopeFields := []string{"success", "code", "message", "request_id", "server_time", "data"}
	for _, schemaName := range []string{"Envelope", "ErrorResponse"} {
		schema, ok := doc.Components.Schemas[schemaName]
		require.True(t, ok, "components/schemas must define %s", schemaName)
		for _, field := range envelopeFields {
			assert.Contains(t, schema.Properties, field,
				"%s must include envelope field %q", schemaName, field)
		}
	}

	// 4. Request field names must match the Go json tags in desktop_auth.go and
	//    be documented in the matching request schema.
	expectedFields := map[string][]string{
		"DesktopLoginRequest": {
			"username", "password", "password_encrypted", "encryption_key_id",
			"device_id", "device_name", "platform", "arch", "client_version",
			"captcha_token",
		},
		"DesktopVerifyRequest":  {"flow_token", "method", "code"},
		"DesktopRefreshRequest": {"refresh_token", "sid"},
		"DesktopLogoutRequest":  {"refresh_token", "sid"},
	}
	jsonTagRe := regexp.MustCompile(`json:"([a-z_]+)`)
	goTags := map[string]bool{}
	for _, tag := range jsonTagRe.FindAllStringSubmatch(controllerSource, -1) {
		goTags[tag[1]] = true
	}

	// Every documented request body must reference a known request schema.
	for path, entry := range doc.Paths {
		ref := entry.Post.RequestBody.Content["application/json"].Schema.Ref
		require.True(t, strings.HasPrefix(ref, "#/components/schemas/"),
			"%s request body must use a $ref to components/schemas", path)
	}

	for schemaName, fields := range expectedFields {
		schema, ok := doc.Components.Schemas[schemaName]
		require.True(t, ok, "components/schemas must define %s", schemaName)
		for _, field := range fields {
			assert.True(t, goTags[field],
				"field %q in %s must correspond to a json tag in desktop_auth.go", field, schemaName)
			assert.Contains(t, schema.Properties, field,
				"request schema %s must declare property %q", schemaName, field)
		}
	}

	// 5. DesktopAuthSuccessData must expose the bundle fields the handler
	//    actually returns (desktopAuthBundleData).
	successSchema, ok := doc.Components.Schemas["DesktopAuthSuccessData"]
	require.True(t, ok, "components/schemas must define DesktopAuthSuccessData")
	for _, field := range []string{"access_token", "refresh_token", "token_type", "access_expires_at", "session", "user"} {
		assert.Contains(t, successSchema.Properties, field, "DesktopAuthSuccessData must declare %q", field)
	}

	t.Logf("drift check OK: %d paths, error codes exercised across %d handlers",
		len(doc.Paths), len(desktopHandlerForPath))
}
