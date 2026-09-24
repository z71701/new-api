package model

import (
	"errors"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const TokenCreateIdempotencyTTL = 24 * time.Hour

var (
	ErrTokenCreateIdempotencyConflict = errors.New("idempotency key was reused with a different request")
	ErrUserTokenLimit                 = errors.New("user token limit reached")
	tokenCreateLocks                  [256]sync.Mutex
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
	if err := tx.Unscoped().Where("id = ? AND user_id = ?", record.TokenID, userID).First(&token).Error; err != nil {
		return nil, false, err
	}
	token.DeletedAt = gorm.DeletedAt{}
	return &token, true, nil
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database is locked")
}

// CreateTokenIdempotent atomically inserts a token and its 24-hour idempotency
// record. Per-user striping prevents same-process SQLite writers from racing;
// bounded retries cover transient locks from other SQLite connections/processes.
func CreateTokenIdempotent(token *Token, route, keyHash, requestHash string, now int64, maxTokens int) (*Token, bool, error) {
	lock := tokenCreateLock(token.UserId)
	lock.Lock()
	defer lock.Unlock()

	for attempt := range 6 {
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
		if !isSQLiteBusy(err) || attempt == 5 {
			return nil, false, err
		}
		time.Sleep(10 * time.Millisecond * time.Duration(1<<attempt))
	}
	return nil, false, errors.New("idempotent token creation retry exhausted")
}
