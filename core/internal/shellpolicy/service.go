// Package shellpolicy owns the durable install-wide owner-shell switch.
package shellpolicy

import (
	"context"
	"sync"
	"time"

	wire "github.com/blerglab/blerg-ai/contracts/shellpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service never caches an enabled decision. Every read requires the policy authority.
// The notification channel accelerates local changes; readers must also refresh across replicas.
type Service struct {
	pool    *pgxpool.Pool
	mu      sync.Mutex
	changed chan struct{}
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool, changed: make(chan struct{})} }
func (s *Service) Read(ctx context.Context) (wire.ShellPolicy, error) {
	p := wire.ShellPolicy{Version: 1}
	err := s.pool.QueryRow(ctx, `SELECT revision, enabled FROM shell_policy WHERE singleton`).Scan(&p.Revision, &p.Enabled)
	return p, err
}
func (s *Service) Set(ctx context.Context, actor string, enabled bool) (wire.ShellPolicy, error) {
	p := wire.ShellPolicy{Version: 1}
	err := s.pool.QueryRow(ctx, `UPDATE shell_policy SET enabled=$1, revision=revision+1, updated_by=$2, updated_at=now() WHERE singleton RETURNING revision, enabled`, enabled, actor).Scan(&p.Revision, &p.Enabled)
	if err == nil {
		s.mu.Lock()
		close(s.changed)
		s.changed = make(chan struct{})
		s.mu.Unlock()
	}
	return p, err
}
func (s *Service) Changed() <-chan struct{} { s.mu.Lock(); defer s.mu.Unlock(); return s.changed }

// Listen observes committed policy AND identity changes across core replicas. The empty
// notification is a wakeup only; state must be read again. Cancellation joins and releases
// the dedicated connection so a stopped reader cannot leak a pool slot.
func (s *Service) Listen(ctx context.Context) (<-chan struct{}, func(), error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err = conn.Exec(ctx, "LISTEN blerg_shell_authority"); err != nil {
		conn.Release()
		return nil, nil, err
	}
	watch, cancel := context.WithCancel(ctx)
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		// Close the dedicated connection rather than returning a listening connection to the pool.
		defer conn.Release()
		defer func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer stop()
			_ = conn.Conn().Close(cleanup)
		}()
		for {
			if _, err := conn.Conn().WaitForNotification(watch); err != nil {
				return
			}
			select {
			case events <- struct{}{}:
			default:
			}
		}
	}()
	return events, func() { cancel(); <-done }, nil
}

// SetForLogin serializes policy writes before taking the established account session fence.
// Fresh statements after each lock wait observe committed role and login revocations.
// Account and session row locks retain that authority through commit, including direct updates.
func (s *Service) SetForLogin(ctx context.Context, actor, login string, enabled bool) (wire.ShellPolicy, error) {
	p := wire.ShellPolicy{Version: 1}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return p, err
	}
	// No account/session lock is held while waiting for the install-wide policy row.
	if _, err = tx.Exec(ctx, `SELECT singleton FROM shell_policy WHERE singleton FOR UPDATE`); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:' || $1::text, 0))`, actor); err != nil {
		return p, err
	}
	var live bool
	if err = tx.QueryRow(ctx, `SELECT role='admin' AND disabled_at IS NULL FROM accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&live); err != nil {
		return p, err
	}
	if !live {
		return p, pgx.ErrNoRows
	}
	rows, err := tx.Query(ctx, `SELECT revoked_at IS NULL AND expires_at>clock_timestamp() FROM human_sessions WHERE account_id=$1 AND chain_id=$2 FOR UPDATE`, actor, login)
	if err != nil {
		return p, err
	}
	live = false
	for rows.Next() {
		var valid bool
		if err = rows.Scan(&valid); err != nil {
			rows.Close()
			return p, err
		}
		live = live || valid
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if !live {
		return p, pgx.ErrNoRows
	}
	err = tx.QueryRow(ctx, `UPDATE shell_policy SET enabled=$1,revision=revision+1,updated_by=$2,updated_at=now()
 WHERE singleton AND EXISTS (SELECT 1 FROM accounts a JOIN human_sessions h ON h.account_id=a.id
 WHERE a.id=$2 AND a.role='admin' AND a.disabled_at IS NULL AND h.chain_id=$3 AND h.revoked_at IS NULL AND h.expires_at>now())
 RETURNING revision,enabled`, enabled, actor, login).Scan(&p.Revision, &p.Enabled)
	if err != nil {
		return p, err
	}
	err = tx.Commit(ctx)
	return p, err
}
