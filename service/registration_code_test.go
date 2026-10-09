package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRegistrationCodeTest(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousDB := model.DB
	model.DB = nil // These Redis lifecycle tests isolate validity from the separately tested admin ledger.
	previousClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	previousRegistrationEnabled := common.RegistrationCodeEnabled.Load()
	common.RDB = client
	common.RedisEnabled = true
	common.RegistrationCodeEnabled.Store(true)
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, client.Close())
		common.RDB = previousClient
		common.RedisEnabled = previousRedisEnabled
		common.RegistrationCodeEnabled.Store(previousRegistrationEnabled)
	})
	return server
}

func TestRegistrationCodeLifecycleAndValidation(t *testing.T) {
	server := setupRegistrationCodeTest(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	server.SetTime(now)
	ctx := context.Background()

	codes, err := GenerateRegistrationCodes(ctx, 2, 30*time.Minute)
	require.NoError(t, err)
	require.Len(t, codes, 2)
	assert.NotEqual(t, codes[0], codes[1])
	assert.Regexp(t, registrationCodePattern, codes[0])

	consumed, err := ConsumeRegistrationCode(ctx, strings.ToLower(codes[0]))
	require.NoError(t, err)
	assert.Equal(t, now.Add(30*time.Minute).UnixMilli(), consumed.ExpiresAtUnixMilli)
	_, err = ConsumeRegistrationCode(ctx, codes[0])
	assert.ErrorIs(t, err, ErrRegistrationCodeInvalid)

	require.NoError(t, RestoreRegistrationCode(ctx, consumed))
	_, err = ConsumeRegistrationCode(ctx, codes[0])
	require.NoError(t, err)

	server.FastForward(31 * time.Minute)
	_, err = ConsumeRegistrationCode(ctx, codes[1])
	assert.ErrorIs(t, err, ErrRegistrationCodeInvalid)
	_, err = ConsumeRegistrationCode(ctx, "bad")
	assert.ErrorIs(t, err, ErrRegistrationCodeInvalid)
}

func TestRegistrationCodeRestorePreservesExpiry(t *testing.T) {
	for _, test := range []struct {
		name  string
		delay time.Duration
	}{
		{"before expiry", 3 * time.Second},
		{"at expiry", 10 * time.Second},
		{"after expiry", 12 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := setupRegistrationCodeTest(t)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			server.SetTime(now)
			ctx := context.Background()
			codes, err := GenerateRegistrationCodes(ctx, 1, 10*time.Second)
			require.NoError(t, err)
			consumed, err := ConsumeRegistrationCode(ctx, codes[0])
			require.NoError(t, err)

			server.FastForward(test.delay)
			server.SetTime(now.Add(test.delay))
			require.NoError(t, RestoreRegistrationCode(ctx, consumed))
			key := registrationCodePrefix + consumed.Digest
			if test.delay >= 10*time.Second {
				assert.False(t, server.Exists(key), "an expired code must not be recreated")
				_, err = ConsumeRegistrationCode(ctx, codes[0])
				assert.ErrorIs(t, err, ErrRegistrationCodeInvalid)
				return
			}
			assert.Equal(t, 10*time.Second-test.delay, server.TTL(key))

			// A second failed attempt must still use the original deadline.
			consumed, err = ConsumeRegistrationCode(ctx, codes[0])
			require.NoError(t, err)
			server.FastForward(time.Second)
			server.SetTime(now.Add(test.delay + time.Second))
			require.NoError(t, RestoreRegistrationCode(ctx, consumed))
			assert.Equal(t, 9*time.Second-test.delay, server.TTL(key))
		})
	}
}

func TestRegistrationCodeRestoreDoesNotOverwriteExistingKey(t *testing.T) {
	server := setupRegistrationCodeTest(t)
	ctx := context.Background()
	codes, err := GenerateRegistrationCodes(ctx, 1, time.Minute)
	require.NoError(t, err)
	consumed, err := ConsumeRegistrationCode(ctx, codes[0])
	require.NoError(t, err)
	key := registrationCodePrefix + consumed.Digest
	require.NoError(t, common.RDB.Set(ctx, key, "existing", 5*time.Second).Err())
	require.NoError(t, RestoreRegistrationCode(ctx, consumed))
	value, err := server.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "existing", value)
	assert.Equal(t, 5*time.Second, server.TTL(key))
}

func TestRegistrationCodeConcurrentConsumptionAllowsOneWinner(t *testing.T) {
	setupRegistrationCodeTest(t)
	ctx := context.Background()
	codes, err := GenerateRegistrationCodes(ctx, 1, time.Minute)
	require.NoError(t, err)

	const contenders = 24
	var wait sync.WaitGroup
	wait.Add(contenders)
	results := make(chan error, contenders)
	for range contenders {
		go func() {
			defer wait.Done()
			_, err := ConsumeRegistrationCode(ctx, codes[0])
			results <- err
		}()
	}
	wait.Wait()
	close(results)

	successes := 0
	invalid := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRegistrationCodeInvalid):
			invalid++
		default:
			require.NoError(t, err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, contenders-1, invalid)
}
