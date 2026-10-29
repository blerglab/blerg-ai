package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var boardKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,4}$`)

// SuggestBoardKey proposes a short name; suggestions do not reserve a key.
func SuggestBoardKey(name string) string {
	var letters, initials []rune
	previousLower := false
	boundary := true
	for _, r := range name {
		if r > unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			boundary = true
			previousLower = false
			continue
		}
		if boundary || previousLower && unicode.IsUpper(r) {
			initials = append(initials, unicode.ToUpper(r))
		}
		letters = append(letters, unicode.ToUpper(r))
		boundary = false
		previousLower = unicode.IsLower(r)
	}
	if len(initials) >= 2 {
		letters = initials
	} else {
		var short []rune
		for i, r := range letters {
			if i == 0 || !strings.ContainsRune("AEIOU", r) {
				short = append(short, r)
			}
		}
		if len(short) >= 2 {
			letters = short
		}
	}
	if len(letters) < 2 || letters[0] < 'A' || letters[0] > 'Z' {
		return "BD"
	}
	if len(letters) > 3 && len(initials) < 2 {
		letters = append(letters[:2], letters[len(letters)-1])
	}
	if len(letters) > 5 {
		letters = letters[:5]
	}
	return string(letters)
}

func checkBoardKey(key string) error {
	if !boardKeyPattern.MatchString(key) {
		return &FieldError{Key: "key", Msg: "must be 2–5 uppercase letters or digits, starting with a letter"}
	}
	return nil
}

// claimBoardKey reserves both current keys and rename aliases in one namespace.
// ON CONFLICT waits for concurrent claims; a losing transaction never changes the board.
func claimBoardKey(ctx context.Context, tx pgx.Tx, board, key string) error {
	if err := checkBoardKey(key); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO board_keys(key,board_id) VALUES($1,$2) ON CONFLICT(key) DO NOTHING`, key, board); err != nil {
		return err
	}
	var owner string
	if err := tx.QueryRow(ctx, `SELECT board_id::text FROM board_keys WHERE key=$1`, key).Scan(&owner); err != nil {
		return err
	}
	if owner != board {
		return &FieldError{Key: "key", Msg: "is already used by another board or one of its aliases"}
	}
	_, err := tx.Exec(ctx, `UPDATE boards SET board_key=$2 WHERE id=$1`, board, key)
	return err
}

// BoardIDForKey resolves current keys and old aliases to the same immutable board id.
func BoardIDForKey(ctx context.Context, pool *pgxpool.Pool, key string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT board_id::text FROM board_keys WHERE key=$1`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return id, err
}

// AvailableBoardKey checks both current keys and permanently reserved aliases.
// Suggestions do not reserve a key; the save path still handles concurrent claims.
func AvailableBoardKey(ctx context.Context, pool *pgxpool.Pool, name string) (string, error) {
	base := SuggestBoardKey(name)
	for i := 0; i < 1000; i++ {
		key := boardKeyWithSuffix(base, i)
		var used bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM board_keys WHERE key=$1)`, key).Scan(&used); err != nil {
			return "", err
		}
		if !used {
			return key, nil
		}
	}
	return "", fmt.Errorf("no available board key; choose a key explicitly")
}

func boardKeyWithSuffix(base string, i int) string {
	if i == 0 {
		return base
	}
	suffix := fmt.Sprint(i)
	return base[:min(len(base), 5-len(suffix))] + suffix
}
