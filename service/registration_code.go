package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
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
local value = redis.call('GET', KEYS[1])
if not value then
  return nil
end
local ttl = redis.call('PTTL', KEYS[1])
redis.call('DEL', KEYS[1])
return {value, ttl}
`)
)

type ConsumedRegistrationCode struct {
	Digest       string
	RemainingTTL time.Duration
}

func ValidateRegistrationCodeConfig() error {
	if !common.RegistrationCodeEnabled {
		return nil
	}
	if !common.RedisEnabled || common.RDB == nil {
		return errors.New("REGISTRATION_CODE_ENABLED requires REDIS_CONN_STRING")
	}
	if len(common.RegistrationCodeAPIKey) < 32 {
		return errors.New("REGISTRATION_CODE_API_KEY must contain at least 32 characters")
	}
	if common.RegistrationCodeTTLSeconds <= 0 || time.Duration(common.RegistrationCodeTTLSeconds)*time.Second > maxRegistrationCodeTTL {
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

func GenerateRegistrationCodes(ctx context.Context, count int, ttl time.Duration) ([]string, error) {
	if !common.RegistrationCodeEnabled || common.RDB == nil {
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

func ConsumeRegistrationCode(ctx context.Context, code string) (*ConsumedRegistrationCode, error) {
	digest, err := RegistrationCodeDigest(code)
	if err != nil {
		return nil, err
	}
	return ConsumeRegistrationCodeDigest(ctx, digest)
}

func ConsumeRegistrationCodeDigest(ctx context.Context, digest string) (*ConsumedRegistrationCode, error) {
	if !common.RegistrationCodeEnabled || common.RDB == nil {
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
	ttlMillis, ok := values[1].(int64)
	if !ok || ttlMillis <= 0 {
		return nil, ErrRegistrationCodeInvalid
	}
	return &ConsumedRegistrationCode{
		Digest:       digest,
		RemainingTTL: time.Duration(ttlMillis) * time.Millisecond,
	}, nil
}

func RestoreRegistrationCode(ctx context.Context, consumed *ConsumedRegistrationCode) error {
	if consumed == nil || consumed.RemainingTTL <= 0 || common.RDB == nil {
		return nil
	}
	_, err := common.RDB.SetNX(ctx, registrationCodePrefix+consumed.Digest, "1", consumed.RemainingTTL).Result()
	if err != nil {
		return fmt.Errorf("restore registration code: %w", err)
	}
	return nil
}
