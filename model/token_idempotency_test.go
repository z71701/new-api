package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openIdempotencyTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	LOG_DB = db

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	require.NoError(t, db.AutoMigrate(&Token{}, &TokenCreateIdempotency{}, &User{}))
	return db
}

func seedIdempotencyTestUser(t *testing.T, db *gorm.DB, id int) {
	t.Helper()
	user := &User{
		Id:          id,
		Username:    fmt.Sprintf("idem-user-%d", id),
		Password:    "placeholder",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AffCode:     fmt.Sprintf("aff%d", id),
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
}

func newIdempotencyToken(userID int, name string) *Token {
	return &Token{
		UserId:         userID,
		Name:           name,
		Key:            "sk-test-" + name,
		Status:         common.TokenStatusEnabled,
		CreatedTime:    1,
		AccessedTime:   1,
		ExpiredTime:    -1,
		RemainQuota:    100,
		UnlimitedQuota: true,
		Group:          "default",
	}
}

func TestCreateTokenIdempotentTTLExpiryReusesKey(t *testing.T) {
	db := openIdempotencyTestDB(t)
	seedIdempotencyTestUser(t, db, 10)

	now := time.Now().Unix()
	expired := now - 10
	// Insert a stale idempotency record pointing at a token that still exists.
	staleToken := newIdempotencyToken(10, "stale")
	require.NoError(t, db.Create(staleToken).Error)
	staleRecord := TokenCreateIdempotency{
		UserID: 10, Route: "/api/token/", KeyHash: "keyhash-expired", RequestHash: "reqhash-a",
		TokenID: staleToken.Id, CreatedAt: expired - 100, ExpiresAt: expired,
	}
	require.NoError(t, db.Create(&staleRecord).Error)

	freshToken := newIdempotencyToken(10, "fresh")
	created, replayed, err := CreateTokenIdempotent(freshToken, "/api/token/", "keyhash-expired", "reqhash-b", now, 100)
	require.NoError(t, err)
	assert.False(t, replayed, "expired record should not replay")
	assert.NotEqual(t, staleToken.Id, created.Id, "should create a new token, not return stale one")

	var records []TokenCreateIdempotency
	require.NoError(t, db.Where("user_id = ?", 10).Find(&records).Error)
	require.Len(t, records, 1)
	assert.Equal(t, created.Id, records[0].TokenID)
	assert.Equal(t, "reqhash-b", records[0].RequestHash, "old expired record should be replaced")
}

func TestCreateTokenIdempotentMaxTokensZeroRejects(t *testing.T) {
	db := openIdempotencyTestDB(t)
	seedIdempotencyTestUser(t, db, 20)

	now := time.Now().Unix()
	_, _, err := CreateTokenIdempotent(newIdempotencyToken(20, "blocked"), "/api/token/", "keyhash-x", "reqhash-x", now, 0)
	require.ErrorIs(t, err, ErrUserTokenLimit)

	var tokenCount, recordCount int64
	db.Model(&Token{}).Where("user_id = ?", 20).Count(&tokenCount)
	db.Model(&TokenCreateIdempotency{}).Where("user_id = ?", 20).Count(&recordCount)
	assert.Zero(t, tokenCount)
	assert.Zero(t, recordCount)
}

func TestCreateTokenIdempotentReplaysSameBody(t *testing.T) {
	db := openIdempotencyTestDB(t)
	seedIdempotencyTestUser(t, db, 30)

	now := time.Now().Unix()
	first, replayed1, err := CreateTokenIdempotent(newIdempotencyToken(30, "replay"), "/api/token/", "keyhash-same", "reqhash-same", now, 100)
	require.NoError(t, err)
	assert.False(t, replayed1)

	second, replayed2, err := CreateTokenIdempotent(newIdempotencyToken(30, "replay"), "/api/token/", "keyhash-same", "reqhash-same", now, 100)
	require.NoError(t, err)
	assert.True(t, replayed2, "same key+body should replay")
	assert.Equal(t, first.Id, second.Id)

	var tokenCount int64
	db.Model(&Token{}).Where("user_id = ?", 30).Count(&tokenCount)
	assert.EqualValues(t, 1, tokenCount)
}

func TestCreateTokenIdempotentConflictsDifferentBody(t *testing.T) {
	db := openIdempotencyTestDB(t)
	seedIdempotencyTestUser(t, db, 40)

	now := time.Now().Unix()
	_, _, err := CreateTokenIdempotent(newIdempotencyToken(40, "first"), "/api/token/", "keyhash-conflict", "reqhash-1", now, 100)
	require.NoError(t, err)

	_, _, err = CreateTokenIdempotent(newIdempotencyToken(40, "second"), "/api/token/", "keyhash-conflict", "reqhash-2", now, 100)
	require.ErrorIs(t, err, ErrTokenCreateIdempotencyConflict)

	var tokenCount int64
	db.Model(&Token{}).Where("user_id = ?", 40).Count(&tokenCount)
	assert.EqualValues(t, 1, tokenCount)
}

func TestCreateTokenIdempotentReplaySkipsSoftDeletedToken(t *testing.T) {
	db := openIdempotencyTestDB(t)
	seedIdempotencyTestUser(t, db, 50)

	now := time.Now().Unix()
	first, _, err := CreateTokenIdempotent(newIdempotencyToken(50, "will-delete"), "/api/token/", "keyhash-deleted", "reqhash-d", now, 100)
	require.NoError(t, err)

	// Soft-delete the token
	require.NoError(t, db.Delete(&Token{}, first.Id).Error)

	// Replay should detect the missing (soft-deleted) token, clean up the idempotency
	// record, and create a fresh token instead of returning a deleted one.
	second, replayed, err := CreateTokenIdempotent(newIdempotencyToken(50, "after-delete"), "/api/token/", "keyhash-deleted", "reqhash-d", now, 100)
	require.NoError(t, err)
	assert.False(t, replayed)
	assert.NotEqual(t, first.Id, second.Id)
}

func TestIsSQLiteBusyTable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sqlite busy message", errors.New("database is locked"), true},
		{"SQLITE_BUSY wrapped", fmt.Errorf("wrapped: %w", errors.New("SQLITE_BUSY")), true},
		{"duplicated key", errors.New("Error 1062: Duplicate entry"), false},
		{"random", errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSQLiteBusy(tt.err))
		})
	}
}
