package system_setting

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeServerAddressProduction(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "canonical HTTPS origin", input: "https://www.aliai.xin", want: "https://www.aliai.xin"},
		{name: "trims whitespace and trailing slash", input: "  https://www.aliai.xin/  ", want: "https://www.aliai.xin"},
		{name: "rejects localhost", input: "https://localhost:3000", wantErr: ErrServerAddressNonPublic},
		{name: "rejects loopback IPv4", input: "https://127.0.0.1:3000", wantErr: ErrServerAddressNonPublic},
		{name: "rejects loopback IPv6", input: "https://[::1]:3000", wantErr: ErrServerAddressNonPublic},
		{name: "rejects private IP", input: "https://10.0.0.8", wantErr: ErrServerAddressNonPublic},
		{name: "rejects HTTP", input: "http://www.aliai.xin", wantErr: ErrServerAddressInsecure},
		{name: "rejects path", input: "https://www.aliai.xin/v1", wantErr: ErrServerAddressInvalid},
		{name: "rejects query", input: "https://www.aliai.xin?source=test", wantErr: ErrServerAddressInvalid},
		{name: "rejects credentials", input: "https://user:pass@www.aliai.xin", wantErr: ErrServerAddressInvalid},
		{name: "empty remains unconfigured", input: " ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeServerAddress(tt.input, false)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "got %v", err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeServerAddressAllowsLocalDevelopment(t *testing.T) {
	got, err := NormalizeServerAddress(" http://localhost:3000/ ", true)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:3000", got)
}
