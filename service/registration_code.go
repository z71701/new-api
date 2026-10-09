package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-redis/redis/v8"
)

const (
	registrationCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	registrationCodePrefix   = "registration:code:"
	registrationCodeLength   = 16
	maxRegistrationCodeCount = 100
	maxRegistrationCodeTTL   = 24 * time.Hour
)

var (
	ErrRegistrationCodeInvalid     = errors.New("registration code is invalid, expired, or already used")
	ErrRegistrationCodeUnavailable = errors.New("registration code service is unavailable")
	registrationCodePattern        = regexp.MustCompile(`^REG-[2-9A-HJ-NP-Z]{4}(?:-[2-9A-HJ-NP-Z]{4}){3}$`)
	consumeRegistrationCodeScript  = redis.NewScript(`
-- Required for TIME followed by writes on Redis versions before 7.0.
if redis.replicate_commands then redis.replicate_commands() end
local value = redis.call('GET', KEYS[1])
if not value then
  return nil
end
local now = redis.call('TIME')
local ttl = redis.call('PTTL', KEYS[1])
redis.call('DEL', KEYS[1])
if ttl <= 0 then
  return nil
end
local expires_at = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000) + ttl
return {value, expires_at}
`)
	restoreRegistrationCodeScript = redis.NewScript(`
if redis.replicate_commands then redis.replicate_commands() end
local now = redis.call('TIME')
local now_ms = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
if tonumber(ARGV[1]) <= now_ms then
  return 0
end
if redis.call('SETNX', KEYS[1], '1') == 0 then
  return 0
end
redis.call('PEXPIREAT', KEYS[1], ARGV[1])
return 1
`)
)

type ConsumedRegistrationCode struct {
	Digest string
	UseRef string
	// Absolute deadline measured by Redis, not the application host's clock.
	ExpiresAtUnixMilli int64
}

func ValidateRegistrationCodeConfig() error {
	if !common.RegistrationCodeEnabled.Load() {
		return nil
	}
	return ValidateRegistrationCodeDependencies()
}

func ValidateRegistrationCodeDependencies() error {
	if !common.RedisEnabled || common.RDB == nil {
		return errors.New("REGISTRATION_CODE_ENABLED requires REDIS_CONN_STRING")
	}
	if len(common.RegistrationCodeAPIKey) < 32 {
		return errors.New("REGISTRATION_CODE_API_KEY must contain at least 32 characters")
	}
	if common.RegistrationCodeTTLSeconds <= 0 || common.RegistrationCodeTTLSeconds > int(maxRegistrationCodeTTL/time.Second) {
		return fmt.Errorf("REGISTRATION_CODE_TTL_SECONDS must be between 1 and %d", int(maxRegistrationCodeTTL/time.Second))
	}
	return nil
}

func RegistrationCodeLimits() (maxCount int, maxTTLSeconds int) {
	return maxRegistrationCodeCount, int(maxRegistrationCodeTTL / time.Second)
}

func RegistrationCodeDigest(code string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if !registrationCodePattern.MatchString(normalized) {
		return "", ErrRegistrationCodeInvalid
	}
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:]), nil
}

// Registration code history can be copied by administrators; store ciphertext
// instead of plaintext. Rotating the independent code API key also rotates
// this encryption key. Redis validity is unaffected by history decryption.
func EncryptRegistrationCode(code string) (string, error) {
	key := sha256.Sum256([]byte("registration-code-history:v1:" + common.RegistrationCodeAPIKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(code), []byte("registration-code:v1"))), nil
}

func DecryptRegistrationCode(encrypted string) (string, error) {
	key := sha256.Sum256([]byte("registration-code-history:v1:" + common.RegistrationCodeAPIKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	data, err := base64.RawStdEncoding.DecodeString(encrypted)
	if err != nil || len(data) < gcm.NonceSize() {
		return "", ErrRegistrationCodeUnavailable
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte("registration-code:v1"))
	if err != nil {
		return "", ErrRegistrationCodeUnavailable
	}
	return string(plain), nil
}

func GenerateRegistrationCodes(ctx context.Context, count int, ttl time.Duration) ([]string, error) {
	if !common.RegistrationCodeEnabled.Load() || common.RDB == nil {
		return nil, ErrRegistrationCodeUnavailable
	}
	if count < 1 || count > maxRegistrationCodeCount || ttl <= 0 || ttl > maxRegistrationCodeTTL {
		return nil, errors.New("registration code generation parameters are invalid")
	}

	codes := make([]string, 0, count)
	for len(codes) < count {
		random := make([]byte, registrationCodeLength)
		if _, err := rand.Read(random); err != nil {
			return nil, fmt.Errorf("generate registration code: %w", err)
		}
		for i := range random {
			limit := 256 - (256 % len(registrationCodeAlphabet))
			for int(random[i]) >= limit {
				if _, err := rand.Read(random[i : i+1]); err != nil {
					return nil, fmt.Errorf("generate registration code: %w", err)
				}
			}
			random[i] = registrationCodeAlphabet[int(random[i])%len(registrationCodeAlphabet)]
		}
		code := fmt.Sprintf("REG-%s-%s-%s-%s", random[0:4], random[4:8], random[8:12], random[12:16])
		digest, _ := RegistrationCodeDigest(code)
		created, err := common.RDB.SetNX(ctx, registrationCodePrefix+digest, "1", ttl).Result()
		if err != nil {
			return nil, fmt.Errorf("store registration code: %w", err)
		}
		if created {
			codes = append(codes, code)
		}
	}
	return codes, nil
}

func DiscardRegistrationCodes(ctx context.Context, codes []string) error {
	keys := make([]string, 0, len(codes))
	for _, code := range codes {
		digest, err := RegistrationCodeDigest(code)
		if err != nil {
			return err
		}
		keys = append(keys, registrationCodePrefix+digest)
	}
	if len(keys) == 0 {
		return nil
	}
	return common.RDB.Del(ctx, keys...).Err()
}

func ConsumeRegistrationCode(ctx context.Context, code string) (*ConsumedRegistrationCode, error) {
	digest, err := RegistrationCodeDigest(code)
	if err != nil {
		return nil, err
	}
	return ConsumeRegistrationCodeDigest(ctx, digest)
}

func ConsumeRegistrationCodeDigest(ctx context.Context, digest string) (*ConsumedRegistrationCode, error) {
	if !common.RegistrationCodeEnabled.Load() || common.RDB == nil {
		return nil, ErrRegistrationCodeUnavailable
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != strings.ToLower(digest) {
		return nil, ErrRegistrationCodeInvalid
	}

	result, err := consumeRegistrationCodeScript.Run(ctx, common.RDB, []string{registrationCodePrefix + digest}).Result()
	if errors.Is(err, redis.Nil) || result == nil {
		return nil, ErrRegistrationCodeInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("consume registration code: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return nil, ErrRegistrationCodeUnavailable
	}
	expiresAtUnixMilli, ok := values[1].(int64)
	if !ok || expiresAtUnixMilli <= 0 {
		return nil, ErrRegistrationCodeInvalid
	}
	consumed := &ConsumedRegistrationCode{
		Digest:             digest,
		UseRef:             common.GetUUID(),
		ExpiresAtUnixMilli: expiresAtUnixMilli,
	}
	if err := model.RecordRegistrationCodeConsumption(digest, time.Now().Unix(), consumed.UseRef); err != nil {
		// Do not burn a valid code when its administration ledger cannot be saved.
		compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if restoreErr := RestoreRegistrationCode(compensation, consumed); restoreErr != nil {
			common.SysError("registration code ledger compensation failed")
		}
		return nil, ErrRegistrationCodeUnavailable
	}
	return consumed, nil
}

func RestoreRegistrationCode(ctx context.Context, consumed *ConsumedRegistrationCode) error {
	if consumed == nil || consumed.ExpiresAtUnixMilli <= 0 || common.RDB == nil {
		return nil
	}
	// Evaluate expiry and restore atomically on Redis so DB/network delays cannot
	// extend the original lifetime and an existing key is never overwritten.
	restored, err := restoreRegistrationCodeScript.Run(ctx, common.RDB,
		[]string{registrationCodePrefix + consumed.Digest}, consumed.ExpiresAtUnixMilli).Result()
	if err != nil {
		return fmt.Errorf("restore registration code: %w", err)
	}
	if restored == int64(1) {
		if err := model.ResetRegistrationCodeConsumption(consumed.Digest, consumed.UseRef); err != nil {
			return err
		}
	}
	return nil
}
