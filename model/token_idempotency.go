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
	ErrTokenCreateIdempotencyConflict = errors.New("idempotency key was reused with a different request")
	ErrUserTokenLimit                 = errors.New("user token limit reached")
	tokenCreateLocks                  [tokenCreateLockShards]sync.Mutex
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

func loadTokenCreateReplay(tx *gorm.DB, userID int, route, keyHash, requestHash string, now int64) (*Token, bool, error) {
	var record TokenCreateIdempotency
	err := tx.Where("user_id = ? AND route = ? AND key_hash = ?", userID, route, keyHash).First(&record).Error
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
	var token Token
	if err := tx.Where("id = ? AND user_id = ?", record.TokenID, userID).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The token was soft-deleted after the idempotency record was written.
			// Treat the record as stale: remove it and let the caller create fresh.
			if delErr := tx.Delete(&record).Error; delErr != nil {
				return nil, false, delErr
			}
			return nil, false, nil
		}
		return nil, false, err
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

func isRetryableIdempotencyError(err error) bool {
	return isSQLiteBusy(err) || errors.Is(err, gorm.ErrDuplicatedKey)
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
			existing, replay, err := loadTokenCreateReplay(tx, token.UserId, route, keyHash, requestHash, now)
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
			var count int64
			if err := tx.Model(&Token{}).Where("user_id = ?", token.UserId).Count(&count).Error; err != nil {
				return err
			}
			if int(count) >= maxTokens {
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
		if errors.Is(err, ErrTokenCreateIdempotencyConflict) || errors.Is(err, ErrUserTokenLimit) {
			return nil, false, err
		}

		// Another instance may have won the unique-key race. Read its committed
		// result before treating the transaction error as a storage failure.
		existing, replay, replayErr := loadTokenCreateReplay(DB, token.UserId, route, keyHash, requestHash, now)
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
