package common

// Interop tests proving the desktop Node.js client and the Go server agree on
// the v2 password envelope. These tests shell out to
// scripts/desktop-auth-crypto-interop.js (pure Node built-in crypto, no npm
// deps) so we exercise a real independent implementation rather than two Go
// code paths.
//
// Test-only RSA key pair (2048-bit, PKCS#8 PEM). Generated once for these
// tests; NEVER use in production. keyID =
// hex(sha256(SPKI DER)[:16]) = 6add1cb8921f2753ab7783a43138629b.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const interopTestPrivateKeyPEM = `-----BEGIN PRIVATE KEY-----
MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQDRpELjxd3yl37r
5A3lIIH9/+IxGipbQCrCtmNQKcO2pALt5GoOSUTFSG1uN/FSvwPG5OQjZoyvhTet
DS7qa0OLy1aANWP4d0af8GjiXRpfcxvu6zptYpVK3xCD+ZuZC9xdh7qYI+A13r9l
1IJ1glmtCQtX9I6t9oRK0XSzmsX8NlX2qeUvLRKuceT1DyjLd0s0EbgF8xsQByGO
Cc6A/ih0eJHDTgolrziM3A1Ha1VyaFtvbQKU73Oc0FJcJ9npbrGoqrBn0qCWkc2X
HZco45OHY8q5hx78dj8o8NGFKMLmuRCfnlTg1kp634NfTo5Cy7UyOMeDf7YQoRTZ
i8gAa291AgMBAAECggEAFOINqQ4tz3Dt5hD1SAzBRjcLe3lax3Tl4uLePmmv+EKa
6W2mbuk3iNtvzdnpbSUKpZu+v2lYvFHt5jbOT0WuGH/XNwrv0XFi6fhodPFx+MNz
qzdoE5C4GcQ3VfOIX2ost8MXKvlFPWMvTQp/5bVoD3I7h2ByJ2bLbindO9hAE/Z7
X+XBEK8/rpP0OM3GNxfhypLxIVAHCk4vxZhkFHQr2EwxrwIJt8sk4wOP2gKie+mf
7wfXpzjmZhqqnzHxFavYhFm3J/e3IfJmxKISqhvfdQeFKoDtOaoE1Ww9eR6JMHzg
q3iaev7775m1aTXHWMThQCk92PMX5uivtOkUHmSABQKBgQDWMhicuYSAvOl1ANxR
2x2sBU9DdbPjHkPSdKzBCNK7eK1BW4BO1B+LQ9DJim+3TvJLmwcdN1to+iOCJmHI
ozMIpPvsvRYudafVmYH6ziD69ucCvh/peAhr930dcqShlnyQRn1RUQjn67o8KOzW
QSZvBdGvnLcLB4DTXYtoI+qa1wKBgQD6jqFtbxj0iJJ73wyrvl/99RLa4AO0V+Ep
H2Vg729K+uCvr9LuhnPtF8YmM3EBDZ1pibfCqK1MBH9gGoLbeavKeBKl2c+Gn9qU
/OHRVpHAsmh8qilultmQjULpn8bLWylHzSojuDafxmKvtP7Jdg/ojkLBSLc//sor
soescLTqkwKBgGi63eXjn7ICrHOVFCTB6mQtxG/LoUUviyHgAofv9HnNq4kFYFsq
xLGnWvLwSWdrpnTpPDVA1+UgSTRd5/neMhnL8ZHzcmENDh8Wi8NB/kY3awSgSaIy
GowP2pEHeQ+5MPaqQKP950jerZS0vfiUqmImijw/eBBgftDaMEufJBrJAoGBAJq8
32EQTZ8ngR/THqYqSmoyolReKKuF4l5dL2TwOhFaYszdjy0UCCASoKMS/eUinWaC
UOR8+5mI5YlalhopSDkgcpPOsmV376w3iNaZ2iXhiLoE9NWBgBfPxdU2gbUxNYtM
X4vzxnhiMqxE4V1V9nku8ncgC1wQZJccCMIsUO7VAoGBAL1f6Pz5u57hyNBM4CKe
iTNUwlkMLDAhyDhlrePlEWDfVoA15xCemTbNiGetu/u3sGd8lMfHvatAeyTH4DF2
UF30O3LS/3KP1gvSPcFzkYg1RlTbwal/6NgUIM8zy5CayWHY7pZjhSHIw4DYuSS0
8SBlwtSTjLh7Lk7o5bTW0Ara
-----END PRIVATE KEY-----
`

const interopTestPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0aRC48Xd8pd+6+QN5SCB
/f/iMRoqW0AqwrZjUCnDtqQC7eRqDklExUhtbjfxUr8DxuTkI2aMr4U3rQ0u6mtD
i8tWgDVj+HdGn/Bo4l0aX3Mb7us6bWKVSt8Qg/mbmQvcXYe6mCPgNd6/ZdSCdYJZ
rQkLV/SOrfaEStF0s5rF/DZV9qnlLy0SrnHk9Q8oy3dLNBG4BfMbEAchjgnOgP4o
dHiRw04KJa84jNwNR2tVcmhbb20ClO9znNBSXCfZ6W6xqKqwZ9KglpHNlx2XKOOT
h2PKuYce/HY/KPDRhSjC5rkQn55U4NZKet+DX06OQsu1MjjHg3+2EKEU2YvIAGtv
dQIDAQAB
-----END PUBLIC KEY-----
`

// interopTestKeyID is sha256(SPKI DER)[:16] hex of the public key above.
const interopTestKeyID = "6add1cb8921f2753ab7783a43138629b"

// Fixed 32-byte AES-256 key and 12-byte GCM nonce used across interop tests.
var interopTestAESKey = mustHexDecode("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
var interopTestNonce = mustHexDecode("000102030405060708090a0b")

func mustHexDecode(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// interopWorktreeRoot returns the repo root (parent of common/). Tests shell
// out to node with cwd set there so the relative script path resolves.
func interopWorktreeRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	// Go test runs with cwd = common/; repo root is the parent.
	return filepath.Dir(wd)
}

// interopWriteTempPEM writes data to a temp file and returns its path.
func interopWriteTempPEM(t *testing.T, name, data string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(data), 0o600))
	return p
}

// interopNodeRun invokes the Node helper script and returns trimmed stdout.
func interopNodeRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("node", append([]string{"scripts/desktop-auth-crypto-interop.js"}, args...)...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "node script failed: %s", stderr.String())
	return strings.TrimSpace(stdout.String())
}

// interopLoadTestKey loads the hardcoded test private key into the common
// package's active state and returns the parsed public key.
func interopLoadTestKey(t *testing.T) *rsa.PublicKey {
	t.Helper()
	require.NoError(t, LoadPasswordEncryptionPrivateKey(interopTestPrivateKeyPEM))
	gotID, gotPEM := PasswordEncryptionPublicKey()
	require.Equal(t, interopTestKeyID, gotID, "keyID mismatch")
	require.Equal(t, strings.TrimSpace(interopTestPublicKeyPEM), strings.TrimSpace(gotPEM))

	block, _ := pem.Decode([]byte(interopTestPublicKeyPEM))
	require.NotNil(t, block)
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	return pub.(*rsa.PublicKey)
}

// interopBuildV2Envelope constructs a v2 envelope in Go using the fixed AES
// key/nonce and the given public key. Used for Go->Node interop and fixed
// vectors.
func interopBuildV2Envelope(t *testing.T, pub *rsa.PublicKey, password string) string {
	t.Helper()
	wrappedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, interopTestAESKey, []byte("password-v2"))
	require.NoError(t, err)

	block, err := aes.NewCipher(interopTestAESKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	aad := []byte("password-v2:" + interopTestKeyID)
	sealed := gcm.Seal(nil, interopTestNonce, []byte(password), aad)

	return "v2." +
		base64.StdEncoding.EncodeToString(wrappedKey) + "." +
		base64.StdEncoding.EncodeToString(interopTestNonce) + "." +
		base64.StdEncoding.EncodeToString(sealed)
}

func TestPasswordCryptoInteropNodeToGo(t *testing.T) {
	pub := interopLoadTestKey(t)
	_ = pub
	pubPath := interopWriteTempPEM(t, "pub.pem", interopTestPublicKeyPEM)
	root := interopWorktreeRoot(t)

	cases := []string{
		"p@ssw0rd-中文-🔐",
		"ascii-only-123",
		strings.Repeat("a", MaxAccountPasswordLength), // 128 ASCII runes, max length
	}
	for _, pw := range cases {
		out := interopNodeRun(t, root,
			"encrypt",
			"--public-key", pubPath,
			"--key-id", interopTestKeyID,
			"--password", pw,
			"--aes-key", hex.EncodeToString(interopTestAESKey),
			"--nonce", hex.EncodeToString(interopTestNonce),
		)
		got, err := DecryptPassword(out, interopTestKeyID)
		require.NoError(t, err, "password=%q", pw)
		assert.Equal(t, pw, got)
	}
}

func TestPasswordCryptoInteropGoToNode(t *testing.T) {
	pub := interopLoadTestKey(t)
	privPath := interopWriteTempPEM(t, "priv.pem", interopTestPrivateKeyPEM)
	root := interopWorktreeRoot(t)

	cases := []string{
		"p@ssw0rd-中文-🔐",
		"ascii-only-123",
		strings.Repeat("Z", MaxAccountPasswordLength),
	}
	for _, pw := range cases {
		envelope := interopBuildV2Envelope(t, pub, pw)
		out := interopNodeRun(t, root,
			"decrypt",
			"--private-key", privPath,
			"--key-id", interopTestKeyID,
			"--ciphertext", envelope,
		)
		assert.Equal(t, pw, out)
	}
}

// TestPasswordCryptoInteropFixedVectors proves the deterministic AES-GCM
// layer matches byte-for-byte across Go and Node. The RSA-OAEP wrappedKey
// segment is intentionally randomized by OAEP's seed, so it cannot be
// byte-compared across implementations; the NodeToGo/GoToNode tests already
// prove the RSA-OAEP layer interops. Here we compare the nonce + GCM seal
// output (ciphertext||tag), which is fully deterministic given the fixed
// AES key, nonce, plaintext, and AAD.
func TestPasswordCryptoInteropFixedVectors(t *testing.T) {
	pub := interopLoadTestKey(t)
	pubPath := interopWriteTempPEM(t, "pub.pem", interopTestPublicKeyPEM)
	root := interopWorktreeRoot(t)

	const pw = "fixed-vector-中文-🔐"

	goEnvelope := interopBuildV2Envelope(t, pub, pw)
	nodeEnvelope := interopNodeRun(t, root,
		"encrypt",
		"--public-key", pubPath,
		"--key-id", interopTestKeyID,
		"--password", pw,
		"--aes-key", hex.EncodeToString(interopTestAESKey),
		"--nonce", hex.EncodeToString(interopTestNonce),
	)

	goParts := strings.Split(goEnvelope, ".")
	nodeParts := strings.Split(nodeEnvelope, ".")
	require.Len(t, goParts, 4)
	require.Len(t, nodeParts, 4)

	// parts[0] == "v2.", parts[1] == wrappedKey (randomized OAEP seed),
	// parts[2] == nonce (fixed), parts[3] == GCM ciphertext||tag (deterministic).
	assert.Equal(t, goParts[2], nodeParts[2], "nonce mismatch")
	assert.Equal(t, goParts[3], nodeParts[3], "GCM ciphertext+tag mismatch: Go and Node AES-256-GCM outputs must be identical for fixed key/nonce/plaintext/AAD")

	// Both envelopes must round-trip to the same plaintext.
	goPlain, err := DecryptPassword(goEnvelope, interopTestKeyID)
	require.NoError(t, err)
	assert.Equal(t, pw, goPlain)
	nodePlain := interopNodeRun(t, root,
		"decrypt",
		"--private-key", interopWriteTempPEM(t, "priv.pem", interopTestPrivateKeyPEM),
		"--key-id", interopTestKeyID,
		"--ciphertext", goEnvelope,
	)
	assert.Equal(t, pw, nodePlain)

	t.Logf("fixed-vector Go envelope (wrappedKey randomized): %s", goEnvelope)
	t.Logf("fixed-vector parts[2] nonce b64: %s", goParts[2])
	t.Logf("fixed-vector parts[3] ciphertext b64: %s", goParts[3])
}

// TestPasswordCryptoLegacyRSADirect exercises the non-v2 path: RSA-OAEP with
// SHA-256 and no label directly over the password.
func TestPasswordCryptoLegacyRSADirect(t *testing.T) {
	pub := interopLoadTestKey(t)
	pubPath := interopWriteTempPEM(t, "pub.pem", interopTestPublicKeyPEM)
	privPath := interopWriteTempPEM(t, "priv.pem", interopTestPrivateKeyPEM)
	root := interopWorktreeRoot(t)

	// Node encrypts legacy, Go decrypts.
	const pw = "legacy-pw-中文"
	nodeCT := interopNodeRun(t, root,
		"encrypt-legacy",
		"--public-key", pubPath,
		"--password", pw,
	)
	got, err := DecryptPassword(nodeCT, interopTestKeyID)
	require.NoError(t, err)
	assert.Equal(t, pw, got)

	// Go encrypts legacy, Node decrypts.
	plain := []byte("legacy-go-中文-🔐")
	goCT, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, plain, nil)
	require.NoError(t, err)
	goEnvelope := base64.StdEncoding.EncodeToString(goCT)
	out := interopNodeRun(t, root,
		"decrypt",
		"--private-key", privPath,
		"--key-id", interopTestKeyID,
		"--ciphertext", goEnvelope,
	)
	assert.Equal(t, string(plain), out)
}

func TestPasswordCryptoInteropTamperDetection(t *testing.T) {
	pub := interopLoadTestKey(t)
	root := interopWorktreeRoot(t)
	_ = root

	const pw = "tamper-me-中文"
	envelope := interopBuildV2Envelope(t, pub, pw)
	parts := strings.Split(envelope, ".")
	require.Len(t, parts, 4)

	// 1. Flip one byte in the GCM ciphertext → GCM auth tag fails.
	ct, err := base64.StdEncoding.DecodeString(parts[3])
	require.NoError(t, err)
	ct[0] ^= 0xff
	tampered := strings.Join([]string{parts[0], parts[1], parts[2], base64.StdEncoding.EncodeToString(ct)}, ".")
	_, err = DecryptPassword(tampered, interopTestKeyID)
	assert.ErrorIs(t, err, ErrPasswordEncryptionInvalid)

	// 2. Wrong (but well-formed) keyID → key-stale error.
	_, err = DecryptPassword(envelope, "00000000000000000000000000000000")
	assert.ErrorIs(t, err, ErrPasswordEncryptionKeyStale)

	// 3. Malformed keyID (wrong length) → invalid.
	_, err = DecryptPassword(envelope, "short")
	assert.ErrorIs(t, err, ErrPasswordEncryptionInvalid)

	// 4. Wrong AAD: seal in Go with a wrong AAD, then decrypt with the correct
	//    keyID. GCM authentication must fail.
	block, err := aes.NewCipher(interopTestAESKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	badAAD := []byte("password-v2:wrong-keyid-but-32-hex-00000000")
	sealed := gcm.Seal(nil, interopTestNonce, []byte(pw), badAAD)
	// Reuse the valid wrappedKey from the good envelope so only the AAD differs.
	badEnvelope := "v2." + parts[1] + "." + parts[2] + "." + base64.StdEncoding.EncodeToString(sealed)
	_, err = DecryptPassword(badEnvelope, interopTestKeyID)
	assert.ErrorIs(t, err, ErrPasswordEncryptionInvalid)
}
