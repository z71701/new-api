package model

import (
	"errors"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const TokenCreateIdempotencyTTL = 24 * time.Hour

const (
	tokenCreateLockShards      = 256
	maxIdempotentCreateRetries = 6
	idempotentRetryBaseDelay   = 10 * time.Millisecond
)

var (
	ErrTokenCreateIdempotencyConflict        = errors.New("idempotency key was reused with a different request")
	ErrTokenCreateIdempotencyResourceDeleted = errors.New("idempotency record points to a deleted token")
	ErrUserTokenLimit                        = errors.New("user token limit reached")
	tokenCreateLocks                         [tokenCreateLockShards]sync.Mutex
)

// TokenCreateIdempotency stores only digests and the created token identifier.
// The API key plaintext is kept exclusively in the tokens table.
type TokenCreateIdempotency struct {
	ID          int64  `gorm:"primaryKey"`
	UserID      int    `gorm:"not null;uniqueIndex:uniq_token_create_idempotency,priority:1"`
	Route       string `gorm:"type:varchar(64);not null;uniqueIndex:uniq_token_create_idempotency,priority:2"`
	KeyHash     string `gorm:"type:varchar(64);not null;uniqueIndex:uniq_token_create_idempotency,priority:3"`
	RequestHash string `gorm:"type:varchar(64);not null"`
	TokenID     int    `gorm:"not null;index"`
	CreatedAt   int64  `gorm:"bigint;not null"`
	ExpiresAt   int64  `gorm:"bigint;not null;index"`
}

func tokenCreateLock(userID int) *sync.Mutex {
	return &tokenCreateLocks[uint(userID)%uint(len(tokenCreateLocks))]
}

// loadTokenCreateReplay resolves an existing idempotent token creation.
//
// When locked is true the idempotency record row is read with SELECT ... FOR
// UPDATE (a no-op on SQLite). This must be used only after the per-user owner
// lock is held, so a concurrent winner's committed row is visible as a current
// read instead of a stale REPEATABLE READ snapshot.
//
// If the record points at a token that no longer exists (hard delete) or that
// was soft-deleted, the record is intentionally left in place and
// ErrTokenCreateIdempotencyResourceDeleted is returned. Silently deleting the
// record and minting a brand-new credential would let a delayed retry of an
// already-revoked request issue a fresh valid token.
func loadTokenCreateReplay(tx *gorm.DB, userID int, route, keyHash, requestHash string, now int64, locked bool) (*Token, bool, error) {
	recordQuery := tx.Where("user_id = ? AND route = ? AND key_hash = ?", userID, route, keyHash)
	if locked {
		recordQuery = lockForUpdate(recordQuery)
	}
	var record TokenCreateIdempotency
	err := recordQuery.First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if record.ExpiresAt <= now {
		if err := tx.Delete(&record).Error; err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if record.RequestHash != requestHash {
		return nil, false, ErrTokenCreateIdempotencyConflict
	}
	// Unscoped so soft-deleted tokens are still visible: we must distinguish a
	// live replay from a deleted-credential case rather than collapsing both to
	// "record missing".
	var token Token
	if err := tx.Unscoped().Where("id = ? AND user_id = ?", record.TokenID, userID).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrTokenCreateIdempotencyResourceDeleted
		}
		return nil, false, err
	}
	if token.DeletedAt.Valid {
		return nil, false, ErrTokenCreateIdempotencyResourceDeleted
	}
	return &token, true, nil
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database is locked")
}

// isSerializableConflict reports database-level deadlocks / serialization
// failures (MySQL error 1213 SQLSTATE 40001, PostgreSQL serialization errors).
// These are expected under concurrent writers and must be retried; the retry
// loop then replays the winner's committed idempotency record.
func isSerializableConflict(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "deadlock found when trying to get lock") ||
		strings.Contains(message, "try restarting transaction") ||
		strings.Contains(message, "40001") ||
		strings.Contains(message, "could not serialize access")
}

func isRetryableIdempotencyError(err error) bool {
	return isSQLiteBusy(err) || errors.Is(err, gorm.ErrDuplicatedKey) || isSerializableConflict(err)
}

// CreateTokenIdempotent atomically inserts a token and its 24-hour idempotency
// record. Per-user striping prevents same-process writers from racing; bounded
// retries cover transient SQLite locks and cross-instance unique-key races.
//
// Within a single process the striping mutex serializes same-user creators.
// Across processes or instances the (user_id, route, key_hash) unique index is
// the final arbiter: a losing transaction retries after the winner commits and
// then replays its result from the database.
func CreateTokenIdempotent(token *Token, route, keyHash, requestHash string, now int64, maxTokens int) (*Token, bool, error) {
	lock := tokenCreateLock(token.UserId)
	lock.Lock()
	defer lock.Unlock()

	for attempt := range maxIdempotentCreateRetries {
		token.Id = 0
		var result *Token
		var replayed bool
		err := DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("user_id = ? AND expires_at <= ?", token.UserId, now).Delete(&TokenCreateIdempotency{}).Error; err != nil {
				return err
			}
			existing, replay, err := loadTokenCreateReplay(tx, token.UserId, route, keyHash, requestHash, now, false)
			if err != nil {
				return err
			}
			if replay {
				result, replayed = existing, true
				return nil
			}

			var owner User
			if err := lockForUpdate(tx).Select("id").Where("id = ?", token.UserId).First(&owner).Error; err != nil {
				return err
			}

			// Re-read the idempotency record with a current (locked) read now that
			// the per-user owner lock is held. On Postgres READ COMMITTED a
			// concurrent winner may have committed between the first read and this
			// point; on MySQL REPEATABLE READ the earlier snapshot would otherwise
			// keep hiding it until commit. The FOR UPDATE read sees the winner's
			// row, and a replay/conflict here short-circuits before we count or
			// create anything.
			existing, replay, err = loadTokenCreateReplay(tx, token.UserId, route, keyHash, requestHash, now, true)
			if err != nil {
				return err
			}
			if replay {
				result, replayed = existing, true
				return nil
			}

			// Lock the user's token rows instead of using an aggregate COUNT. A
			// plain COUNT under MySQL REPEATABLE READ reads the transaction
			// snapshot, so waiting on the owner lock would not refresh it and
			// could let two near-limit transactions both pass the check. SELECT
			// id ... FOR UPDATE is a current read and serializes the two writers.
			var tokenIDs []int
			if err := lockForUpdate(tx).Model(&Token{}).Select("id").Where("user_id = ?", token.UserId).Find(&tokenIDs).Error; err != nil {
				return err
			}
			if len(tokenIDs) >= maxTokens {
				return ErrUserTokenLimit
			}
			if err := tx.Create(token).Error; err != nil {
				return err
			}
			record := TokenCreateIdempotency{
				UserID: token.UserId, Route: route, KeyHash: keyHash, RequestHash: requestHash,
				TokenID: token.Id, CreatedAt: now, ExpiresAt: now + int64(TokenCreateIdempotencyTTL/time.Second),
			}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			result = token
			return nil
		})
		if err == nil {
			return result, replayed, nil
		}
		if errors.Is(err, ErrTokenCreateIdempotencyConflict) || errors.Is(err, ErrUserTokenLimit) || errors.Is(err, ErrTokenCreateIdempotencyResourceDeleted) {
			return nil, false, err
		}

		// Another instance may have won the unique-key race. Read its committed
		// result before treating the transaction error as a storage failure.
		existing, replay, replayErr := loadTokenCreateReplay(DB, token.UserId, route, keyHash, requestHash, now, false)
		if replayErr == nil && replay {
			return existing, true, nil
		}
		if errors.Is(replayErr, ErrTokenCreateIdempotencyConflict) {
			return nil, false, replayErr
		}
		if replayErr != nil {
			return nil, false, replayErr
		}
		if !isRetryableIdempotencyError(err) || attempt == maxIdempotentCreateRetries-1 {
			return nil, false, err
		}
		time.Sleep(idempotentRetryBaseDelay * time.Duration(1<<attempt))
	}
	return nil, false, errors.New("idempotent token creation retry exhausted")
}
