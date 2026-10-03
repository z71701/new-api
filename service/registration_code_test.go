package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRegistrationCodeTest(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	previousRegistrationEnabled := common.RegistrationCodeEnabled
	common.RDB = client
	common.RedisEnabled = true
	common.RegistrationCodeEnabled = true
	t.Cleanup(func() {
		require.NoError(t, client.Close())
		common.RDB = previousClient
		common.RedisEnabled = previousRedisEnabled
		common.RegistrationCodeEnabled = previousRegistrationEnabled
	})
	return server
}

func TestRegistrationCodeLifecycleAndValidation(t *testing.T) {
	server := setupRegistrationCodeTest(t)
	ctx := context.Background()

	codes, err := GenerateRegistrationCodes(ctx, 2, 30*time.Minute)
	require.NoError(t, err)
	require.Len(t, codes, 2)
	assert.NotEqual(t, codes[0], codes[1])
	assert.Regexp(t, registrationCodePattern, codes[0])

	consumed, err := ConsumeRegistrationCode(ctx, strings.ToLower(codes[0]))
	require.NoError(t, err)
	assert.Positive(t, consumed.RemainingTTL)
	assert.LessOrEqual(t, consumed.RemainingTTL, 30*time.Minute)
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
