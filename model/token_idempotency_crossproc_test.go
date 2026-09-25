package model

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// workerResult is what each child process writes to its result file.
type workerResult struct {
	TokenID  int    `json:"token_id"`
	Replayed bool   `json:"replayed"`
	Err      string `json:"err"`
}

// crossProcDialects returns the configured external dialect DSNs, if any.
func crossProcDialects(t *testing.T) []struct {
	name      string
	dsn       string
	dialector func(string) gorm.Dialector
} {
	t.Helper()
	var out []struct {
		name      string
		dsn       string
		dialector func(string) gorm.Dialector
	}
	if dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN")); dsn != "" {
		out = append(out, struct {
			name      string
			dsn       string
			dialector func(string) gorm.Dialector
		}{"mysql", dsn, func(d string) gorm.Dialector { return mysql.Open(d) }})
	}
	if dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")); dsn != "" {
		out = append(out, struct {
			name      string
			dsn       string
			dialector func(string) gorm.Dialector
		}{"postgres", dsn, func(d string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: d, PreferSimpleProtocol: true})
		}})
	}
	return out
}

// TestCreateTokenIdempotentCrossProcessRace is the parent test. It is skipped
// unless TEST_MYSQL_DSN or TEST_POSTGRES_DSN is set. It starts a TCP barrier,
// spawns two independent worker processes against the same database, and
// asserts the near-limit idempotency invariants across processes.
//
// The point of spawning real processes (not goroutines) is that the in-process
// striping mutex tokenCreateLocks only serializes writers inside one process;
// cross-process contention must be resolved by the database row locks.
func TestCreateTokenIdempotentCrossProcessRace(t *testing.T) {
	dialects := crossProcDialects(t)
	if len(dialects) == 0 {
		t.Skip("TEST_MYSQL_DSN / TEST_POSTGRES_DSN not configured; skipping cross-process race test")
	}

	for _, d := range dialects {
		t.Run(d.name, func(t *testing.T) {
			db, err := gorm.Open(d.dialector(d.dsn), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })

			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &TokenCreateIdempotency{}))

			scenarios := []struct {
				name    string
				sameKey bool
			}{
				{"same_key", true},
				{"diff_key", false},
			}
			for _, sc := range scenarios {
				t.Run(sc.name, func(t *testing.T) {
					runCrossProcessScenario(t, db, d.name, d.dsn, sc.sameKey)
				})
			}
		})
	}
}

func runCrossProcessScenario(t *testing.T, db *gorm.DB, dialect string, dsn string, sameKey bool) {
	// Use a unique user per run so repeated executions do not collide.
	userID := 90000 + int(time.Now().UnixNano()%9999)
	seedCrossProcUser(t, db, userID)
	t.Cleanup(func() {
		_ = db.Unscoped().Where("user_id = ?", userID).Delete(&Token{}).Error
		_ = db.Unscoped().Where("user_id = ?", userID).Delete(&TokenCreateIdempotency{}).Error
		_ = db.Unscoped().Where("id = ?", userID).Delete(&User{}).Error
	})

	// TCP barrier: both workers connect, then the parent releases them.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	barrierAddr := ln.Addr().String()

	workDir := t.TempDir()
	resultA := filepath.Join(workDir, "result_a.json")
	resultB := filepath.Join(workDir, "result_b.json")

	baseKeyHash := fmt.Sprintf("crossproc-%s-%d", dialect, time.Now().UnixNano())
	reqHash := "reqhash-crossproc"
	workerEnv := []string{
		"IDEMPOTENCY_WORKER=1",
		"IDEMPOTENCY_DIALECT=" + dialect,
		"IDEMPOTENCY_DSN=" + dsn,
		fmt.Sprintf("IDEMPOTENCY_USER_ID=%d", userID),
		fmt.Sprintf("IDEMPOTENCY_MAX_TOKENS=1"),
		"IDEMPOTENCY_ROUTE=/api/token/",
		"IDEMPOTENCY_REQUEST_HASH=" + reqHash,
		"IDEMPOTENCY_BARRIER=" + barrierAddr,
	}

	// Worker A and worker B may share the same key hash (same_key) or use
	// different ones (diff_key).
	keyA := baseKeyHash + "-a"
	keyB := baseKeyHash + "-b"
	if sameKey {
		keyB = keyA
	}

	startWorker := func(resultFile, keyHash string) *exec.Cmd {
		env := append([]string{}, workerEnv...)
		env = append(env, "IDEMPOTENCY_KEY_HASH="+keyHash)
		env = append(env, "IDEMPOTENCY_RESULT_FILE="+resultFile)
		cmd := exec.Command(os.Args[0], "-test.run=TestCreateTokenIdempotentCrossProcessRaceWorker", "-test.v")
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdout = nil
		cmd.Stderr = nil
		require.NoError(t, cmd.Start())
		return cmd
	}

	// Accept both workers, then release them.
	go func() {
		conns := make([]net.Conn, 0, 2)
		for range 2 {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
		}
		// Tiny grace period so both workers are inside their connect call
		// before we close the barrier.
		time.Sleep(50 * time.Millisecond)
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	cmdA := startWorker(resultA, keyA)
	cmdB := startWorker(resultB, keyB)

	errA := cmdA.Wait()
	errB := cmdB.Wait()
	require.NoError(t, errA, "worker A exited unexpectedly")
	require.NoError(t, errB, "worker B exited unexpectedly")

	resA := readWorkerResult(t, resultA)
	resB := readWorkerResult(t, resultB)

	var liveTokens, records int64
	require.NoError(t, db.Model(&Token{}).Where("user_id = ?", userID).Count(&liveTokens).Error)
	require.NoError(t, db.Model(&TokenCreateIdempotency{}).Where("user_id = ?", userID).Count(&records).Error)

	if sameKey {
		// Both must succeed and return the same token; exactly one token row and
		// one idempotency record must exist.
		assert.Empty(t, resA.Err, "worker A: %s", resA.Err)
		assert.Empty(t, resB.Err, "worker B: %s", resB.Err)
		assert.Positive(t, resA.TokenID)
		assert.Equal(t, resA.TokenID, resB.TokenID, "same key must resolve to the same token")
		assert.EqualValues(t, 1, liveTokens, "exactly one live token")
		assert.EqualValues(t, 1, records, "exactly one idempotency record")
	} else {
		// Different keys, maxTokens=1: exactly one wins, the other hits the
		// limit. No overshoot.
		successes := 0
		for _, res := range []workerResult{resA, resB} {
			if res.Err == "" {
				successes++
				assert.Positive(t, res.TokenID)
			} else {
				assert.Contains(t, res.Err, "user token limit reached", "expected limit error, got %q", res.Err)
			}
		}
		assert.Equal(t, 1, successes, "exactly one of two different-key requests should win")
		assert.EqualValues(t, 1, liveTokens, "must not overshoot the token limit")
		assert.EqualValues(t, 1, records, "only the winning request writes an idempotency record")
	}
}

func readWorkerResult(t *testing.T, path string) workerResult {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "worker result file %s missing", path)
	var res workerResult
	require.NoError(t, common.Unmarshal(raw, &res))
	return res
}

func seedCrossProcUser(t *testing.T, db *gorm.DB, id int) {
	t.Helper()
	user := &User{
		Id:          id,
		Username:    fmt.Sprintf("crossproc-user-%d", id),
		Password:    "placeholder",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AffCode:     fmt.Sprintf("aff%d", id),
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
}

// TestCreateTokenIdempotentCrossProcessRaceWorker runs in the child process. It
// is a no-op (skips) unless IDEMPOTENCY_WORKER=1 is set, which only the parent
// sets when re-executing the test binary.
func TestCreateTokenIdempotentCrossProcessRaceWorker(t *testing.T) {
	if os.Getenv("IDEMPOTENCY_WORKER") != "1" {
		t.Skip("not running as cross-process worker")
	}

	dialect := os.Getenv("IDEMPOTENCY_DIALECT")
	dsn := os.Getenv("IDEMPOTENCY_DSN")
	var dialector gorm.Dialector
	switch dialect {
	case "mysql":
		dialector = mysql.Open(dsn)
		common.SetDatabaseTypes(common.DatabaseTypeMySQL, common.DatabaseTypeMySQL)
	case "postgres":
		dialector = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	default:
		t.Fatalf("unknown dialect %q", dialect)
	}
	common.RedisEnabled = false

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("worker open db: %v", err)
	}
	DB = db
	LOG_DB = db

	userID := 0
	fmt.Sscanf(os.Getenv("IDEMPOTENCY_USER_ID"), "%d", &userID)
	maxTokens := 1
	fmt.Sscanf(os.Getenv("IDEMPOTENCY_MAX_TOKENS"), "%d", &maxTokens)
	now := time.Now().Unix()

	// Wait for the barrier release.
	barrier := os.Getenv("IDEMPOTENCY_BARRIER")
	conn, err := net.Dial("tcp", barrier)
	if err != nil {
		t.Fatalf("worker dial barrier: %v", err)
	}
	one := make([]byte, 1)
	_, _ = conn.Read(one) // blocks until parent closes
	_ = conn.Close()

	token := &Token{
		UserId:         userID,
		Name:           "crossproc",
		Key:            "sk-crossproc-" + os.Getenv("IDEMPOTENCY_KEY_HASH"),
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		RemainQuota:    100,
		UnlimitedQuota: true,
		Group:          "default",
	}
	result, _, err := CreateTokenIdempotent(
		token,
		os.Getenv("IDEMPOTENCY_ROUTE"),
		os.Getenv("IDEMPOTENCY_KEY_HASH"),
		os.Getenv("IDEMPOTENCY_REQUEST_HASH"),
		now, maxTokens,
	)
	res := workerResult{}
	if err != nil {
		res.Err = err.Error()
	} else {
		res.TokenID = result.Id
	}
	out, err := common.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := os.WriteFile(os.Getenv("IDEMPOTENCY_RESULT_FILE"), out, 0o600); err != nil {
		t.Fatalf("write result: %v", err)
	}
}
