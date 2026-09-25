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

// desktopPostHandlers maps each documented desktop auth path to the handler
// function that implements it in controller/desktop_auth.go. These four routes
// sit behind DesktopAuthRateLimit and use the six-field desktop envelope.
var desktopPostHandlers = map[string]string{
	"/api/desktop/auth/login":   "DesktopLogin",
	"/api/desktop/auth/verify":  "DesktopVerify",
	"/api/desktop/auth/refresh": "DesktopRefresh",
	"/api/desktop/auth/logout":  "DesktopLogout",
}

// bootstrapGetPaths are the two pre-login GET probes reused by the desktop
// client. They use the legacy web envelope ({success,message,data}) and are
// NOT behind DesktopAuthRateLimit.
var bootstrapGetPaths = map[string]string{
	"/api/status":                    "getStatus",
	"/api/user/login/encryption-key": "getPasswordEncryptionKey",
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

// knownAuthCodes is the complete, authoritative set of machine-readable codes
// these endpoints can emit (OK on success; everything else is an error).
var knownAuthCodes = []string{
	"OK",
	"INVALID_ARGUMENT",
	"AUTH_PASSWORD_LOGIN_DISABLED",
	"AUTH_CAPTCHA_REQUIRED",
	"AUTH_CAPTCHA_INVALID",
	"AUTH_CAPTCHA_UNAVAILABLE",
	"AUTH_ENCRYPTION_PAYLOAD_INVALID",
	"AUTH_ENCRYPTION_KEY_STALE",
	"AUTH_INVALID_CREDENTIALS",
	"AUTH_VERIFICATION_UNSUPPORTED",
	"AUTH_VERIFICATION_REQUIRED",
	"AUTH_FLOW_EXPIRED",
	"AUTH_VERIFICATION_FAILED",
	"AUTH_SESSION_LIMIT",
	"AUTH_SESSION_ISSUANCE_LIMIT",
	"AUTH_SESSION_MISMATCH",
	"AUTH_REFRESH_RACE",
	"AUTH_TOKEN_EXPIRED",
	"AUTH_SESSION_EXPIRED",
	"AUTH_SESSION_REVOKED",
	"AUTH_UNAUTHORIZED",
	"AUTH_RATE_LIMITED",
	"AUTH_INTERNAL_ERROR",
}

// codeRe matches an envelope machine-readable code. The TOTP request field
// `code` ("123456") must NOT match this.
var codeRe = regexp.MustCompile(`^(?:OK|INVALID_ARGUMENT|AUTH_[A-Z_]+)$`)

// ---- generic YAML navigation helpers (yaml.v3 decodes to map[string]any) ----

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "expected a YAML mapping, got %T", v)
	return m
}

func asSlice(t *testing.T, v any) []any {
	t.Helper()
	s, ok := v.([]any)
	require.True(t, ok, "expected a YAML sequence, got %T", v)
	return s
}

func asString(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	require.True(t, ok, "expected a YAML string, got %T", v)
	return s
}

func asBool(t *testing.T, v any) bool {
	t.Helper()
	b, ok := v.(bool)
	require.True(t, ok, "expected a YAML bool, got %T", v)
	return b
}

func repoRoot(t *testing.T) string {
	t.Helper()
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

// loadExample resolves components/examples/<name> and returns its `value` object.
func loadExample(t *testing.T, examples map[string]any, name string) map[string]any {
	t.Helper()
	e, ok := examples[name].(map[string]any)
	require.True(t, ok, "components/examples must define %q", name)
	require.NotEmpty(t, e["summary"], "example %q must declare a summary", name)
	v, ok := e["value"].(map[string]any)
	require.True(t, ok, "example %q must have an object `value`", name)
	return v
}

func TestDesktopAuthOpenAPIDrift(t *testing.T) {
	root := repoRoot(t)
	specBytes := readRepoFile(t, root, "docs/desktop-auth-openapi.yaml")

	// 0. Parse the whole spec into a generic structured map (not text contains).
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(specBytes), &doc), "spec must be valid YAML")

	paths := asMap(t, doc["paths"])
	components := asMap(t, doc["components"])
	schemas := asMap(t, components["schemas"])
	examples := asMap(t, components["examples"])

	// 1. Path validation: exactly six paths, correct methods, each with an
	//    operationId, and matching router registrations. The two new bootstrap
	//    GET endpoints pin their operationIds; the four existing POST routes
	//    keep their established camelCase operationIds and only must declare one.
	require.Len(t, paths, len(bootstrapGetPaths)+len(desktopPostHandlers),
		"spec must document exactly the six desktop-related paths")

	routerSource := readRepoFile(t, root, "router/api-router.go")
	assert.Contains(t, routerSource, `apiRouter.GET("/status", controller.GetStatus)`,
		"router must register GET /api/status -> GetStatus")
	assert.Contains(t, routerSource, `userRoute.GET("/login/encryption-key", middleware.DisableCache(), controller.GetPasswordEncryptionKey)`,
		"router must register GET /api/user/login/encryption-key -> GetPasswordEncryptionKey")

	for path, wantOpID := range bootstrapGetPaths {
		entry := asMap(t, paths[path])
		op, ok := entry["get"].(map[string]any)
		require.True(t, ok, "path %s must declare method get", path)
		assert.Equal(t, wantOpID, asString(t, op["operationId"]), "path %s operationId mismatch", path)
	}
	for path := range desktopPostHandlers {
		entry := asMap(t, paths[path])
		op, ok := entry["post"].(map[string]any)
		require.True(t, ok, "path %s must declare method post", path)
		require.NotEmpty(t, asString(t, op["operationId"]), "path %s must declare an operationId", path)
		suffix := strings.TrimPrefix(path, "/api/desktop/auth/")
		assert.Contains(t, routerSource, fmt.Sprintf(`desktopAuthRoute.POST("/%s"`, suffix),
			"router must register %s", path)
	}

	// 2. Handler reachable-status drift for the four POST desktop routes:
	//    every HTTP status a handler can actually emit must be declared.
	controllerSource := readRepoFile(t, root, "controller/desktop_auth.go")
	serviceSource := readRepoFile(t, root, "service/auth_session.go")
	errorStatuses := authSessionErrorStatuses(t, serviceSource)
	writeResponseRe := regexp.MustCompile(`writeDesktopAuthResponse\(c,\s*http\.(Status\w+)`)

	declaredStatuses := func(path string) map[int]bool {
		entry := asMap(t, paths[path])
		op := asMap(t, entry["post"])
		resps := asMap(t, op["responses"])
		out := map[int]bool{}
		for codeStr := range resps {
			n, err := strconv.Atoi(codeStr)
			require.NoError(t, err, "response key %q on %s must be numeric", codeStr, path)
			out[n] = true
		}
		return out
	}

	for path, handler := range desktopPostHandlers {
		declared := declaredStatuses(path)
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
		// Every desktop auth route sits behind DesktopAuthRateLimit -> 429.
		actual[429] = true
		for code := range actual {
			assert.True(t, declared[code],
				"spec for %s must declare status %d (handler %s can return it)", path, code, handler)
		}
		assert.True(t, declared[200], "spec for %s must declare a 200 response", path)
	}

	// 3. Unified envelope field check. The four desktop routes use the
	//    six-field Envelope; the two bootstrap GET routes use the three-field
	//    WebEnvelope (verified against common.ApiSuccess / GetStatus).
	envelopeFields := []string{"success", "code", "message", "request_id", "server_time", "data"}
	envelope := asMap(t, schemas["Envelope"])
	envelopeProps := asMap(t, envelope["properties"])
	for _, field := range envelopeFields {
		assert.Contains(t, envelopeProps, field, "Envelope must include %q", field)
	}
	errorResp := asMap(t, schemas["ErrorResponse"])
	errorProps := asMap(t, errorResp["properties"])
	for _, field := range envelopeFields {
		assert.Contains(t, errorProps, field, "ErrorResponse must include %q", field)
	}
	webEnvelope := asMap(t, schemas["WebEnvelope"])
	webProps := asMap(t, webEnvelope["properties"])
	for _, field := range []string{"success", "message", "data"} {
		assert.Contains(t, webProps, field, "WebEnvelope must include %q", field)
	}

	// 4. oneOf + discriminator on POST /api/desktop/auth/login 200 data.
	loginPost := asMap(t, asMap(t, paths["/api/desktop/auth/login"])["post"])
	login200 := asMap(t, asMap(t, loginPost["responses"])["200"])
	loginSchema := asMap(t, asMap(t, asMap(t, login200["content"])["application/json"])["schema"])
	var loginDataSchema map[string]any
	for _, item := range asSlice(t, loginSchema["allOf"]) {
		m := asMap(t, item)
		if props, ok := m["properties"].(map[string]any); ok {
			if ds, ok := props["data"].(map[string]any); ok {
				loginDataSchema = ds
			}
		}
	}
	require.NotNil(t, loginDataSchema, "login 200 schema must inline `data` via allOf")
	oneOf := asSlice(t, loginDataSchema["oneOf"])
	require.Len(t, oneOf, 2, "login data must be a oneOf over success + challenge")
	disc := asMap(t, loginDataSchema["discriminator"])
	assert.Equal(t, "require_verification", asString(t, disc["propertyName"]),
		"login discriminator must key on require_verification")
	mapping := asMap(t, disc["mapping"])
	assert.Contains(t, mapping, "true", "discriminator must map true -> challenge")
	assert.Contains(t, mapping, "false", "discriminator must map false -> success")

	// 5. Schema fixture validation: load every named example and assert the
	//    data/contract structure the desktop client relies on.
	expectedExamples := []string{
		"login_success", "login_totp_challenge",
		"totp_verify_success", "totp_verify_failed",
		"refresh_rotation", "refresh_30s_retry", "refresh_out_of_window_replay",
		"logout_success", "logout_duplicate", "logout_random_token",
		"rate_limited_429", "internal_error_5xx",
	}
	require.Len(t, examples, len(expectedExamples), "components/examples must define exactly the 12 named examples")
	for _, name := range expectedExamples {
		_, ok := examples[name]
		require.True(t, ok, "components/examples missing named example %q", name)
	}

	// login_success: bundle shape.
	succ := loadExample(t, examples, "login_success")
	assert.Equal(t, "OK", asString(t, succ["code"]))
	assert.True(t, asBool(t, succ["success"]))
	succData := asMap(t, succ["data"])
	assert.Contains(t, succData, "access_token")
	assert.Contains(t, succData, "refresh_token")
	assert.Equal(t, "desktop", asString(t, asMap(t, succData["session"])["client_type"]),
		"login session must be a desktop session")
	assert.NotEmpty(t, asString(t, asMap(t, succData["user"])["id"]), "login success must carry user.id")

	// login_totp_challenge: challenge shape.
	chal := loadExample(t, examples, "login_totp_challenge")
	assert.Equal(t, "AUTH_VERIFICATION_REQUIRED", asString(t, chal["code"]))
	chalData := asMap(t, chal["data"])
	assert.True(t, asBool(t, chalData["require_verification"]), "challenge must set require_verification=true")
	assert.NotEmpty(t, asString(t, chalData["flow_token"]), "challenge must carry flow_token")
	methods := asSlice(t, chalData["methods"])
	hasTOTP := false
	for _, m := range methods {
		if asString(t, m) == "totp" {
			hasTOTP = true
		}
	}
	assert.True(t, hasTOTP, "challenge methods must include totp")

	// totp_verify_success: bundle issued after 2FA.
	vSucc := loadExample(t, examples, "totp_verify_success")
	assert.Equal(t, "OK", asString(t, vSucc["code"]))
	assert.NotEmpty(t, asString(t, asMap(t, vSucc["data"])["refresh_token"]))

	// totp_verify_failed: 401 error envelope.
	vFail := loadExample(t, examples, "totp_verify_failed")
	assert.False(t, asBool(t, vFail["success"]))
	assert.Equal(t, "AUTH_VERIFICATION_FAILED", asString(t, vFail["code"]))

	// refresh_rotation: a NEW refresh_token is returned.
	rot := loadExample(t, examples, "refresh_rotation")
	assert.Equal(t, "OK", asString(t, rot["code"]))
	rotRT := asString(t, asMap(t, rot["data"])["refresh_token"])
	assert.NotEmpty(t, rotRT, "rotation must return a fresh refresh_token")

	// refresh_30s_retry: in-window replay returns the same bundle (still 200 OK).
	retry := loadExample(t, examples, "refresh_30s_retry")
	assert.Equal(t, "OK", asString(t, retry["code"]))
	assert.Equal(t, rotRT, asString(t, asMap(t, retry["data"])["refresh_token"]),
		"in-window replay must return the same bundle")

	// refresh_out_of_window_replay: 401 AUTH_SESSION_REVOKED.
	oo := loadExample(t, examples, "refresh_out_of_window_replay")
	assert.False(t, asBool(t, oo["success"]))
	assert.Equal(t, "AUTH_SESSION_REVOKED", asString(t, oo["code"]))

	// logout_success / logout_duplicate: idempotent {logged_out:true}.
	for _, name := range []string{"logout_success", "logout_duplicate"} {
		lo := loadExample(t, examples, name)
		assert.Equal(t, "OK", asString(t, lo["code"]))
		assert.True(t, asBool(t, asMap(t, lo["data"])["logged_out"]),
			"%s must return data.logged_out=true", name)
	}

	// logout_random_token: 401 AUTH_UNAUTHORIZED.
	lrand := loadExample(t, examples, "logout_random_token")
	assert.False(t, asBool(t, lrand["success"]))
	assert.Equal(t, "AUTH_UNAUTHORIZED", asString(t, lrand["code"]))

	// rate_limited_429 / internal_error_5xx: error envelopes.
	rl := loadExample(t, examples, "rate_limited_429")
	assert.False(t, asBool(t, rl["success"]))
	assert.Equal(t, "AUTH_RATE_LIMITED", asString(t, rl["code"]))
	ie := loadExample(t, examples, "internal_error_5xx")
	assert.False(t, asBool(t, ie["success"]))
	assert.Equal(t, "AUTH_INTERNAL_ERROR", asString(t, ie["code"]))

	// 6. Error-code coverage: every code string the spec actually uses must be a
	//    known code, and every known code must appear somewhere (example value or
	//    description). Walk the whole parsed doc recursively.
	usedCodes := map[string]bool{}
	authCodeRe := regexp.MustCompile(`\b(?:AUTH_[A-Z_]+|INVALID_ARGUMENT)\b`)
	var walkCodes func(v any)
	walkCodes = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			// Only treat a `code` field as an envelope code when it actually
			// looks like one; the TOTP request field `code: "123456"` must be ignored.
			if c, ok := t["code"].(string); ok && codeRe.MatchString(c) {
				usedCodes[c] = true
			}
			for _, val := range t {
				walkCodes(val)
			}
		case []any:
			for _, val := range t {
				walkCodes(val)
			}
		case string:
			for _, m := range authCodeRe.FindAllString(t, -1) {
				usedCodes[m] = true
			}
			if strings.TrimSpace(t) == "OK" {
				usedCodes["OK"] = true
			}
		}
	}
	walkCodes(doc)

	knownSet := map[string]bool{}
	for _, c := range knownAuthCodes {
		knownSet[c] = true
	}
	for c := range usedCodes {
		assert.True(t, knownSet[c], "spec references unknown code %q", c)
	}
	for c := range knownSet {
		assert.True(t, usedCodes[c], "known code %q is never documented (example value or description)", c)
	}

	// 7. Request-field drift: documented request schemas must match the Go json
	//    tags in desktop_auth.go for the four POST routes.
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
	for schemaName, fields := range expectedFields {
		schema, ok := schemas[schemaName].(map[string]any)
		require.True(t, ok, "components/schemas must define %s", schemaName)
		props := asMap(t, schema["properties"])
		for _, field := range fields {
			assert.True(t, goTags[field],
				"field %q in %s must correspond to a json tag in desktop_auth.go", field, schemaName)
			assert.Contains(t, props, field, "request schema %s must declare property %q", schemaName, field)
		}
	}

	// DesktopAuthSuccessData must expose the bundle fields the handler returns.
	succSchema, ok := schemas["DesktopAuthSuccessData"].(map[string]any)
	require.True(t, ok, "components/schemas must define DesktopAuthSuccessData")
	succProps := asMap(t, succSchema["properties"])
	for _, field := range []string{"access_token", "refresh_token", "token_type", "access_expires_at", "session", "user"} {
		assert.Contains(t, succProps, field, "DesktopAuthSuccessData must declare %q", field)
	}

	t.Logf("drift check OK: %d paths, %d named examples, %d known error codes",
		len(paths), len(examples), len(knownAuthCodes))
}
