package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/jackc/pgx/v5"

	"github.com/blerglab/blerg-ai/contracts/engineaccess"
)

// NewServiceWithAdapters is an explicit startup registry seam. Production uses
// NewService's fixed provider configuration; tests can register isolated providers.
func NewServiceWithAdapters(st db.Store, backend keybackend.Backend, adapters ...Adapter) *Service {
	s := &Service{st: st, backend: backend, adapters: map[string]Adapter{}}
	for _, a := range adapters {
		s.adapters[a.Engine()+"/"+a.Kind()] = a
	}
	return s
}

func (s *Service) adapter(engine, kind string) (Adapter, error) {
	a := s.adapters[engine+"/"+kind]
	if a == nil || !engineaccess.SupportsMode(a.Engine(), a.AccessMode()) {
		return nil, ErrNotManaged
	}
	return a, nil
}
func (s *Service) deviceAdapter(engine string) (Adapter, error) {
	var match Adapter
	for _, a := range s.adapters {
		if a.Engine() == engine && engineaccess.SupportsMode(a.Engine(), a.AccessMode()) {
			if match != nil {
				return nil, ErrNotManaged
			}
			match = a
		}
	}
	if match == nil {
		return nil, ErrNotManaged
	}
	return match, nil
}

func (s *Service) accountTx(ctx context.Context, account string, fn func(pgx.Tx) error) error {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	var id string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE id=$1 FOR UPDATE`, account).Scan(&id); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func lifecycleEvent(ctx context.Context, tx pgx.Tx, account, engine, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO credential_lifecycle_events(account_id,engine,action) VALUES($1,$2,$3)`, account, engine, action)
	return err
}
func (s *Service) encryptJSON(ctx context.Context, v any) ([]byte, string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, "", err
	}
	return s.backend.Encrypt(ctx, raw)
}
func (s *Service) decryptJSON(ctx context.Context, raw []byte, key string, v any) error {
	plain, err := s.backend.Decrypt(ctx, raw, key)
	if err != nil {
		return err
	}
	if json.Unmarshal(plain, v) != nil {
		return ErrReconnectRequired
	}
	return nil
}

// StartLogin never reads a workstation credential. A distinct OAuth ceremony
// creates a distinct refresh-token family under the broker's sole ownership.
func (s *Service) StartLogin(ctx context.Context, account, engine, sid string) (LoginStatus, error) {
	adapter, err := s.deviceAdapter(engine)
	if err != nil {
		return LoginStatus{}, err
	}
	var status LoginStatus
	existing := false
	err = s.accountTx(ctx, account, func(tx pgx.Tx) error {
		// A crashed startup has no public code to resume, and an abandoned
		// exchange may already have consumed its private grant. Start a new
		// ceremony rather than returning an unusable or ambiguous attempt. A
		// dead browser cannot finish its ceremony or block a newly signed-in one.
		tag, err := tx.Exec(ctx, `UPDATE credential_login_attempts SET state='reconnect_required',ciphertext=NULL,key_id=NULL,claim=NULL,claim_deadline=NULL
   WHERE account_id=$1 AND engine=$2 AND state IN ('starting','pending','polling')
   AND ((state IN ('starting','polling') AND (claim_deadline IS NULL OR claim_deadline<=clock_timestamp()))
     OR NOT EXISTS(SELECT 1 FROM human_sessions h WHERE h.chain_id=credential_login_attempts.session_id
       AND h.account_id=credential_login_attempts.account_id AND h.revoked_at IS NULL AND h.expires_at>clock_timestamp()))`, account, engine)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if err = lifecycleEvent(ctx, tx, account, engine, "login_ambiguous"); err != nil {
				return err
			}
		}
		// Reopening the ceremony in the same browser resumes its one durable
		// attempt; another browser cannot learn or replace its authorization code.
		err = tx.QueryRow(ctx, `SELECT attempt_id::text,state,user_code,verification_url,interval_seconds,expires_at
   FROM credential_login_attempts WHERE account_id=$1 AND engine=$2 AND session_id=$3
   AND expires_at>now() AND state IN ('starting','pending','polling')`, account, engine, sid).
			Scan(&status.AttemptID, &status.State, &status.UserCode, &status.VerificationURL, &status.IntervalSeconds, &status.ExpiresAt)
		if err == nil {
			if status.State == "starting" {
				return ErrRefreshPending
			}
			status.State = "pending"
			existing = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		err = tx.QueryRow(ctx, `INSERT INTO credential_login_attempts(account_id,engine,session_id,claim_deadline)
   VALUES($1,$2,$3,now()+interval '30 seconds') ON CONFLICT(account_id,engine) DO UPDATE SET
   attempt_id=gen_random_uuid(),session_id=EXCLUDED.session_id,state='starting',ciphertext=NULL,key_id=NULL,
   user_code='',verification_url='',expires_at=now()+interval '15 minutes',claim=NULL,
   claim_deadline=now()+interval '30 seconds',next_poll_at=now()
   WHERE credential_login_attempts.expires_at<=now() OR credential_login_attempts.state IN ('connected','reconnect_required')
   RETURNING attempt_id::text,expires_at`, account, engine, sid).Scan(&status.AttemptID, &status.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRefreshPending
		}
		if err != nil {
			return err
		}
		return lifecycleEvent(ctx, tx, account, engine, "login_started")
	})
	if err != nil {
		return LoginStatus{}, err
	}
	if existing {
		return status, nil
	}
	grant, providerErr := adapter.StartDevice(ctx)
	if providerErr != nil {
		s.failLogin(ctx, account, engine, status.AttemptID, "", false)
		return LoginStatus{}, ErrReconnectRequired
	}
	ciphertext, key, err := s.encryptJSON(ctx, grant)
	if err != nil {
		s.failLogin(ctx, account, engine, status.AttemptID, "", false)
		return LoginStatus{}, ErrReconnectRequired
	}
	err = s.accountTx(ctx, account, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE credential_login_attempts SET state='pending',ciphertext=$4,key_id=$5,
   user_code=$6,verification_url=$7,interval_seconds=$8,expires_at=$9,next_poll_at=now(),claim_deadline=NULL
   WHERE account_id=$1 AND engine=$2 AND attempt_id=$3 AND state='starting' AND claim_deadline>now()`, account, engine, status.AttemptID, ciphertext, key, grant.UserCode, grant.VerificationURL, grant.IntervalSeconds, grant.ExpiresAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return LoginStatus{}, err
	}
	return LoginStatus{AttemptID: status.AttemptID, State: "pending", UserCode: grant.UserCode, VerificationURL: grant.VerificationURL, IntervalSeconds: grant.IntervalSeconds, ExpiresAt: grant.ExpiresAt}, nil
}

func (s *Service) failLogin(ctx context.Context, account, engine, attempt, claim string, claimed bool) {
	// A canceled request must not undo a durable ambiguity fence. If this best-
	// effort update fails, the persisted lease expires into reconnect_required.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = s.accountTx(cleanupCtx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(cleanupCtx, `UPDATE credential_login_attempts SET state='reconnect_required',ciphertext=NULL,key_id=NULL,claim=NULL,claim_deadline=NULL
   WHERE account_id=$1 AND engine=$2 AND attempt_id=$3 AND ($4::boolean=false OR claim::text=$5)`, account, engine, attempt, claimed, claim)
		if err != nil {
			return err
		}
		return lifecycleEvent(cleanupCtx, tx, account, engine, "login_reconnect_required")
	})
}

func (s *Service) PollLogin(ctx context.Context, account, engine, sid, attempt string) (LoginStatus, error) {
	adapter, err := s.deviceAdapter(engine)
	if err != nil {
		return LoginStatus{}, err
	}
	var status LoginStatus
	var ciphertext []byte
	var key, claim string
	var poll bool
	err = s.accountTx(ctx, account, func(tx pgx.Tx) error {
		var next time.Time
		var deadline *time.Time
		err := tx.QueryRow(ctx, `SELECT attempt_id::text,state,user_code,verification_url,interval_seconds,expires_at,next_poll_at,claim_deadline
   FROM credential_login_attempts WHERE account_id=$1 AND engine=$2 AND session_id=$3 AND attempt_id=$4`, account, engine, sid, attempt).
			Scan(&status.AttemptID, &status.State, &status.UserCode, &status.VerificationURL, &status.IntervalSeconds, &status.ExpiresAt, &next, &deadline)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status.State == "connected" || status.State == "reconnect_required" {
			return nil
		}
		if !status.ExpiresAt.After(time.Now()) || (deadline != nil && !deadline.After(time.Now())) {
			status.State = "reconnect_required"
			_, err = tx.Exec(ctx, `UPDATE credential_login_attempts SET state='reconnect_required',ciphertext=NULL,key_id=NULL,claim=NULL,claim_deadline=NULL WHERE attempt_id=$1`, attempt)
			if err != nil {
				return err
			}
			return lifecycleEvent(ctx, tx, account, engine, "login_ambiguous")
		}
		if status.State != "pending" || next.After(time.Now()) {
			status.State = "pending"
			return nil
		}
		err = tx.QueryRow(ctx, `UPDATE credential_login_attempts SET state='polling',claim=gen_random_uuid(),claim_deadline=now()+interval '30 seconds'
   WHERE attempt_id=$1 RETURNING ciphertext,key_id,claim::text`, attempt).Scan(&ciphertext, &key, &claim)
		poll = err == nil
		status.State = "pending"
		return err
	})
	if err != nil {
		return LoginStatus{}, err
	}
	if !poll {
		return status, nil
	}
	var grant DeviceGrant
	if err = s.decryptJSON(ctx, ciphertext, key, &grant); err != nil {
		s.failLogin(ctx, account, engine, attempt, claim, true)
		return LoginStatus{}, ErrReconnectRequired
	}
	token, providerErr := adapter.PollDevice(ctx, grant)
	if errors.Is(providerErr, ErrLoginPending) {
		err = s.accountTx(ctx, account, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE credential_login_attempts SET state='pending',claim=NULL,claim_deadline=NULL,next_poll_at=now()+interval '1 second'*interval_seconds WHERE attempt_id=$1 AND claim::text=$2 AND claim_deadline>now()`, attempt, claim)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrNotFound
			}
			return nil
		})
		return status, err
	}
	if providerErr != nil {
		s.failLogin(ctx, account, engine, attempt, claim, true)
		return LoginStatus{}, ErrReconnectRequired
	}
	ciphertext, key, err = s.encryptJSON(ctx, token)
	if err != nil {
		s.failLogin(ctx, account, engine, attempt, claim, true)
		return LoginStatus{}, ErrReconnectRequired
	}
	err = s.accountTx(ctx, account, func(tx pgx.Tx) error {
		if err := lockLoginPrincipal(ctx, tx, account, sid); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE credential_login_attempts SET state='connected',ciphertext=NULL,key_id=NULL,claim=NULL,claim_deadline=NULL WHERE attempt_id=$1 AND claim::text=$2 AND claim_deadline>now()`, attempt, claim)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `INSERT INTO user_credentials(account_id,engine,ciphertext,key_id,kind,state,expires_at) VALUES($1,$2,$3,$4,$5,'usable',$6)
   ON CONFLICT(account_id,engine) DO UPDATE SET ciphertext=EXCLUDED.ciphertext,key_id=EXCLUDED.key_id,kind=EXCLUDED.kind,state='usable',expires_at=EXCLUDED.expires_at,
   generation=gen_random_uuid(),refresh_claim=NULL,claim_deadline=NULL,updated_at=now()`, account, engine, ciphertext, key, adapter.Kind(), token.ExpiresAt)
		if err != nil {
			return err
		}
		return lifecycleEvent(ctx, tx, account, engine, "login_connected")
	})
	if err != nil {
		return LoginStatus{}, err
	}
	status.State = "connected"
	return status, nil
}

// The account row is already locked by accountTx. Hold the live rotation-chain
// row through the vault commit too, so revocation cannot win during provider I/O
// and then be followed by an unauthorized credential replacement.
func lockLoginPrincipal(ctx context.Context, tx pgx.Tx, account, sid string) error {
	var live string
	err := tx.QueryRow(ctx, `SELECT h.id::text FROM human_sessions h JOIN accounts a ON a.id=h.account_id
   WHERE h.chain_id=$1 AND h.account_id=$2 AND h.revoked_at IS NULL AND h.expires_at>clock_timestamp()
   AND a.disabled_at IS NULL ORDER BY h.created_at DESC LIMIT 1 FOR SHARE OF h`, sid, account).Scan(&live)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *Service) CancelLogin(ctx context.Context, account, engine, sid, attempt string) error {
	return s.accountTx(ctx, account, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM credential_login_attempts WHERE account_id=$1 AND engine=$2 AND session_id=$3 AND attempt_id=$4`, account, engine, sid, attempt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return lifecycleEvent(ctx, tx, account, engine, "login_canceled")
	})
}

// Obtain returns cached access material or claims exactly one refresh across
// replicas. Once provider I/O may have begun, an abandoned claim is ambiguous:
// expiration requires reconnect instead of spending a possibly consumed token.
//
// A forced refresh is honored at most once per forcedRefreshInterval for an
// account's engine; a forced call inside that window gets the cached access.
// A failed refresh refuses its caller only: the account's other sessions keep
// the still-valid access until it expires (see failRefresh).
func (s *Service) Obtain(ctx context.Context, account, engine string, force bool, previous string) (Access, error) {
	var token ManagedToken
	var adapter Adapter
	var generation, kind, claim, key string
	var ciphertext []byte
	var refresh, recent bool
	var expiry *time.Time
	var outcome error
	err := s.accountTx(ctx, account, func(tx pgx.Tx) error {
		var state string
		var deadline *time.Time
		err := tx.QueryRow(ctx, `SELECT kind,state,generation::text,ciphertext,key_id,expires_at,claim_deadline,
   EXISTS(SELECT 1 FROM credential_lifecycle_events e WHERE e.account_id=c.account_id AND e.engine=c.engine
     AND e.action='refresh_claimed' AND e.created_at>now()-make_interval(secs=>$3))
   FROM user_credentials c WHERE account_id=$1 AND engine=$2`, account, engine, forcedRefreshInterval.Seconds()).
			Scan(&kind, &state, &generation, &ciphertext, &key, &expiry, &deadline, &recent)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if kind == "opaque" {
			return ErrNotManaged
		}
		if adapter, err = s.adapter(engine, kind); err != nil {
			return ErrReconnectRequired
		}
		if state == "reconnect_required" {
			return ErrReconnectRequired
		}
		if state == "refresh_pending" {
			if deadline != nil && deadline.After(time.Now()) {
				return ErrRefreshPending
			}
			_, err = tx.Exec(ctx, `UPDATE user_credentials SET state='reconnect_required',refresh_claim=NULL,claim_deadline=NULL WHERE account_id=$1 AND engine=$2 AND generation::text=$3`, account, engine, generation)
			if err != nil {
				return err
			}
			outcome = ErrReconnectRequired
			return lifecycleEvent(ctx, tx, account, engine, "refresh_ambiguous")
		}
		if state != "usable" || expiry == nil {
			return ErrReconnectRequired
		}
		// A force request names the generation that failed. A later generation is
		// already the response to another caller's refresh and must not rotate again.
		// A refresh claimed within the throttle window serves the cached access.
		if expiry.After(time.Now().Add(time.Minute)) && (!force || previous != generation || recent) {
			return nil
		}
		err = tx.QueryRow(ctx, `UPDATE user_credentials SET state='refresh_pending',refresh_claim=gen_random_uuid(),claim_deadline=now()+interval '30 seconds'
   WHERE account_id=$1 AND engine=$2 AND generation::text=$3 RETURNING refresh_claim::text`, account, engine, generation).Scan(&claim)
		if err != nil {
			return err
		}
		refresh = true
		return lifecycleEvent(ctx, tx, account, engine, "refresh_claimed")
	})
	if err != nil {
		return Access{}, err
	}
	if outcome != nil {
		return Access{}, outcome
	}
	if err = s.decryptJSON(ctx, ciphertext, key, &token); err != nil {
		if refresh {
			s.failRefresh(ctx, account, engine, generation, claim, nil, time.Time{})
		}
		return Access{}, ErrReconnectRequired
	}
	if !refresh {
		return managedAccess(adapter, token, generation), nil
	}
	// An earlier failed refresh spent the refresh authority: never present a
	// possibly consumed refresh token again.
	if token.RefreshToken == "" {
		s.failRefresh(ctx, account, engine, generation, claim, &token, *expiry)
		return Access{}, ErrReconnectRequired
	}
	previousToken := token
	token, err = adapter.Refresh(ctx, previousToken)
	if err != nil {
		s.failRefresh(ctx, account, engine, generation, claim, &previousToken, *expiry)
		return Access{}, ErrReconnectRequired
	}
	ciphertext, key, err = s.encryptJSON(ctx, token)
	if err != nil {
		// The rotated refresh token is lost and the previous one is consumed.
		s.failRefresh(ctx, account, engine, generation, claim, &previousToken, *expiry)
		return Access{}, ErrReconnectRequired
	}
	err = s.accountTx(ctx, account, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE user_credentials SET ciphertext=$5,key_id=$6,state='usable',expires_at=$7,
   generation=gen_random_uuid(),refresh_claim=NULL,claim_deadline=NULL,updated_at=now()
   WHERE account_id=$1 AND engine=$2 AND generation::text=$3 AND refresh_claim::text=$4 AND claim_deadline>now()
   RETURNING generation::text`, account, engine, generation, claim, ciphertext, key, token.ExpiresAt).Scan(&generation)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReconnectRequired
		}
		if err != nil {
			return err
		}
		return lifecycleEvent(ctx, tx, account, engine, "refreshed")
	})
	if err != nil {
		return Access{}, err
	}
	return managedAccess(adapter, token, generation), nil
}
func managedAccess(adapter Adapter, token ManagedToken, generation string) Access {
	return Access{Mode: adapter.AccessMode(), AccessToken: token.AccessToken, AccountID: token.AccountID, PlanType: token.PlanType, Generation: generation, ExpiresAt: token.ExpiresAt}
}

// forcedRefreshInterval bounds forced provider refreshes per account and engine.
const forcedRefreshInterval = time.Minute

// failRefresh releases a refresh claim that did not produce new material. When
// the current access is still valid for more than a minute, the credential stays
// usable for every session of the account, with its refresh authority removed
// (it may already be consumed, so it is never presented again); it requires a
// reconnect once that access expires. Otherwise it requires a reconnect now.
func (s *Service) failRefresh(ctx context.Context, account, engine, generation, claim string, current *ManagedToken, expiry time.Time) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	var ciphertext []byte
	var key string
	keep := current != nil && expiry.After(time.Now().Add(time.Minute))
	if keep {
		spent := *current
		spent.RefreshToken = ""
		var err error
		if ciphertext, key, err = s.encryptJSON(cleanupCtx, spent); err != nil {
			keep = false
		}
	}
	_ = s.accountTx(cleanupCtx, account, func(tx pgx.Tx) error {
		if keep {
			tag, err := tx.Exec(cleanupCtx, `UPDATE user_credentials SET state='usable',ciphertext=$5,key_id=$6,refresh_claim=NULL,claim_deadline=NULL WHERE account_id=$1 AND engine=$2 AND generation::text=$3 AND refresh_claim::text=$4`, account, engine, generation, claim, ciphertext, key)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			return lifecycleEvent(cleanupCtx, tx, account, engine, "refresh_failed")
		}
		tag, err := tx.Exec(cleanupCtx, `UPDATE user_credentials SET state='reconnect_required',refresh_claim=NULL,claim_deadline=NULL WHERE account_id=$1 AND engine=$2 AND generation::text=$3 AND refresh_claim::text=$4`, account, engine, generation, claim)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return lifecycleEvent(cleanupCtx, tx, account, engine, "refresh_reconnect_required")
	})
}

// ManagedCapability describes a registered login adapter, without provider secrets.
type ManagedCapability struct {
	Engine             string `json:"engine"`
	Kind               string `json:"kind"`
	Login              bool   `json:"login"`
	VerificationOrigin string `json:"verification_origin"`
}

func (s *Service) ManagedCapabilities() []ManagedCapability {
	out := make([]ManagedCapability, 0, len(s.adapters))
	for _, a := range s.adapters {
		if !engineaccess.SupportsMode(a.Engine(), a.AccessMode()) {
			continue
		}
		out = append(out, ManagedCapability{Engine: a.Engine(), Kind: a.Kind(), Login: true, VerificationOrigin: a.VerificationOrigin()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Engine+"/"+out[i].Kind < out[j].Engine+"/"+out[j].Kind })
	return out
}
