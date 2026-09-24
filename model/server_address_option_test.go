package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionNormalizesServerAddress(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	previousDebug := common.DebugEnabled
	previousMap := common.OptionMap
	previousAddress := system_setting.ServerAddress
	previousPasskey := *system_setting.GetPasskeySettings()
	common.DebugEnabled = false
	common.OptionMap = map[string]string{}
	system_setting.ServerAddress = ""
	*system_setting.GetPasskeySettings() = system_setting.PasskeySettings{}
	t.Cleanup(func() {
		common.DebugEnabled = previousDebug
		common.OptionMap = previousMap
		system_setting.ServerAddress = previousAddress
		*system_setting.GetPasskeySettings() = previousPasskey
	})

	require.NoError(t, UpdateOption("ServerAddress", "  https://www.aliai.xin/  "))
	assert.Equal(t, "https://www.aliai.xin", requireOptionValue(t, db, "ServerAddress"))
	assert.Equal(t, "https://www.aliai.xin", system_setting.ServerAddress)
	assert.Equal(t, "https://www.aliai.xin", common.OptionMap["ServerAddress"])

	require.ErrorIs(t, UpdateOption("ServerAddress", "http://localhost:3000"), system_setting.ErrServerAddressInsecure)
	assert.Equal(t, "https://www.aliai.xin", requireOptionValue(t, db, "ServerAddress"))
	assert.Equal(t, "https://www.aliai.xin", system_setting.ServerAddress)
}

func TestUpdateOptionsBulkNormalizesServerAddress(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	previousDebug := common.DebugEnabled
	previousMap := common.OptionMap
	previousAddress := system_setting.ServerAddress
	previousPasskey := *system_setting.GetPasskeySettings()
	common.DebugEnabled = false
	common.OptionMap = map[string]string{}
	system_setting.ServerAddress = ""
	*system_setting.GetPasskeySettings() = system_setting.PasskeySettings{}
	t.Cleanup(func() {
		common.DebugEnabled = previousDebug
		common.OptionMap = previousMap
		system_setting.ServerAddress = previousAddress
		*system_setting.GetPasskeySettings() = previousPasskey
	})

	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"ServerAddress": "https://www.aliai.xin/",
		"SystemName":    "Aliai",
	}))
	assert.Equal(t, "https://www.aliai.xin", requireOptionValue(t, db, "ServerAddress"))
	assert.Equal(t, "Aliai", requireOptionValue(t, db, "SystemName"))
	assert.Equal(t, "https://www.aliai.xin", system_setting.ServerAddress)
}
func TestLoadOptionsIgnoresInvalidProductionServerAddress(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	previousDebug := common.DebugEnabled
	previousMap := common.OptionMap
	previousAddress := system_setting.ServerAddress
	previousPasskey := *system_setting.GetPasskeySettings()
	common.DebugEnabled = false
	common.OptionMap = map[string]string{}
	system_setting.ServerAddress = ""
	*system_setting.GetPasskeySettings() = system_setting.PasskeySettings{}
	t.Cleanup(func() {
		common.DebugEnabled = previousDebug
		common.OptionMap = previousMap
		system_setting.ServerAddress = previousAddress
		*system_setting.GetPasskeySettings() = previousPasskey
	})

	require.NoError(t, db.Create(&Option{Key: "ServerAddress", Value: "http://localhost:3000"}).Error)
	loadOptionsFromDatabase()
	assert.Empty(t, system_setting.ServerAddress)
	assert.Empty(t, common.OptionMap["ServerAddress"])
	assert.Equal(t, "http://localhost:3000", requireOptionValue(t, db, "ServerAddress"), "invalid persisted value remains visible to administrators for correction")
}
