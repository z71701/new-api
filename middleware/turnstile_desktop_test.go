package middleware

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// controller.DesktopLogin maps ValidateTurnstileToken errors onto HTTP codes:
// ErrTurnstileRejected -> 400 AUTH_CAPTCHA_INVALID, any other error -> 503
// AUTH_CAPTCHA_UNAVAILABLE. turnstileSiteVerify is an unexported package var,
// so the rejected/unavailable seams are exercised here rather than from the
// controller package.

func TestDesktopAcceptanceCaptchaInvalid(t *testing.T) {
	previous := common.TurnstileCheckEnabled
	common.TurnstileCheckEnabled = true
	t.Cleanup(func() { common.TurnstileCheckEnabled = previous })

	original := turnstileSiteVerify
	turnstileSiteVerify = func(response, remoteIP string) error {
		return ErrTurnstileRejected
	}
	t.Cleanup(func() { turnstileSiteVerify = original })

	err := ValidateTurnstileToken("provided-token", "127.0.0.1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTurnstileRejected)
}

func TestDesktopAcceptanceCaptchaUnavailable(t *testing.T) {
	previous := common.TurnstileCheckEnabled
	common.TurnstileCheckEnabled = true
	t.Cleanup(func() { common.TurnstileCheckEnabled = previous })

	original := turnstileSiteVerify
	networkErr := errors.New("dial tcp challenges.cloudflare.com: connect: connection refused")
	turnstileSiteVerify = func(response, remoteIP string) error {
		return networkErr
	}
	t.Cleanup(func() { turnstileSiteVerify = original })

	err := ValidateTurnstileToken("provided-token", "127.0.0.1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrTurnstileRejected, "transport failures must surface as unavailable, not invalid")
}
