package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openIdempotencyTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	originalDB := DB
	originalLOGDB := LOG_DB
	DB = db
	LOG_DB = db

	t.Cleanup(func() {
		DB = originalDB
		LOG_DB = originalLOGDB
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

func TestCreateTokenIdempotentSoftDeletedReturnsResourceDeleted(t *testing.T) {
	db := openIdempotencyTestDB(t)
	seedIdempotencyTestUser(t, db, 50)

	now := time.Now().Unix()
	first, _, err := CreateTokenIdempotent(newIdempotencyToken(50, "will-delete"), "/api/token/", "keyhash-deleted", "reqhash-d", now, 100)
	require.NoError(t, err)

	// Soft-delete the token, then replay the exact idempotent request. The
	// response must be stable: the idempotency record is retained, no fresh
	// credential is minted, and a second retry returns the same error.
	require.NoError(t, db.Delete(&Token{}, first.Id).Error)

	for range 2 {
		second, replayed, err := CreateTokenIdempotent(newIdempotencyToken(50, "after-delete"), "/api/token/", "keyhash-deleted", "reqhash-d", now, 100)
		require.ErrorIs(t, err, ErrTokenCreateIdempotencyResourceDeleted)
		assert.False(t, replayed)
		assert.Nil(t, second)
	}

	var liveTokenCount, allTokenCount, recordCount int64
	db.Model(&Token{}).Where("user_id = ?", 50).Count(&liveTokenCount)
	db.Model(&Token{}).Unscoped().Where("user_id = ?", 50).Count(&allTokenCount)
	db.Model(&TokenCreateIdempotency{}).Where("user_id = ?", 50).Count(&recordCount)
	assert.Zero(t, liveTokenCount, "no live token should be created")
	assert.EqualValues(t, 1, allTokenCount, "the soft-deleted row must remain")
	assert.EqualValues(t, 1, recordCount, "idempotency record must be retained")
}

func TestCreateTokenIdempotentHardDeletedReturnsResourceDeleted(t *testing.T) {
	db := openIdempotencyTestDB(t)
	seedIdempotencyTestUser(t, db, 52)

	now := time.Now().Unix()
	created, _, err := CreateTokenIdempotent(newIdempotencyToken(52, "hard-deleted"), "/api/token/", "keyhash-hard", "reqhash-hard", now, 100)
	require.NoError(t, err)

	// Hard-delete the token row (Unscoped delete).
	require.NoError(t, db.Unscoped().Delete(&Token{}, created.Id).Error)

	_, _, err = CreateTokenIdempotent(newIdempotencyToken(52, "retry"), "/api/token/", "keyhash-hard", "reqhash-hard", now, 100)
	require.ErrorIs(t, err, ErrTokenCreateIdempotencyResourceDeleted)

	var tokenCount, recordCount int64
	db.Model(&Token{}).Unscoped().Where("user_id = ?", 52).Count(&tokenCount)
	db.Model(&TokenCreateIdempotency{}).Where("user_id = ?", 52).Count(&recordCount)
	assert.Zero(t, tokenCount)
	assert.EqualValues(t, 1, recordCount, "idempotency record is retained even when the token row is gone")
}

func TestCreateTokenIdempotentRecheckAfterLock(t *testing.T) {
	db := openIdempotencyTestDB(t)
	seedIdempotencyTestUser(t, db, 53)

	now := time.Now().Unix()

	// Directly exercise loadTokenCreateReplay with locked=true (the post-lock
	// re-read path). SQLite has no FOR UPDATE, so locked is a no-op here but the
	// code path and return contract are identical.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		tok, replay, err := loadTokenCreateReplay(tx, 53, "/api/token/", "keyhash-recheck", "reqhash-recheck", now, true)
		require.NoError(t, err)
		assert.False(t, replay)
		assert.Nil(t, tok)
		return nil
	}))

	// Seed a live token + idempotency record, then the locked re-read must replay.
	seeded := newIdempotencyToken(53, "seeded")
	require.NoError(t, db.Create(seeded).Error)
	require.NoError(t, db.Create(&TokenCreateIdempotency{
		UserID: 53, Route: "/api/token/", KeyHash: "keyhash-recheck", RequestHash: "reqhash-recheck",
		TokenID: seeded.Id, CreatedAt: now - 10, ExpiresAt: now + int64(TokenCreateIdempotencyTTL/time.Second),
	}).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		tok, replay, err := loadTokenCreateReplay(tx, 53, "/api/token/", "keyhash-recheck", "reqhash-recheck", now, true)
		require.NoError(t, err)
		assert.True(t, replay)
		require.NotNil(t, tok)
		assert.Equal(t, seeded.Id, tok.Id)
		return nil
	}))

	// After soft-deleting the token, the locked re-read must surface ResourceDeleted.
	require.NoError(t, db.Delete(&Token{}, seeded.Id).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		tok, replay, err := loadTokenCreateReplay(tx, 53, "/api/token/", "keyhash-recheck", "reqhash-recheck", now, true)
		require.ErrorIs(t, err, ErrTokenCreateIdempotencyResourceDeleted)
		assert.False(t, replay)
		assert.Nil(t, tok)
		return nil
	}))
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
