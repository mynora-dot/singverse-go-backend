package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

// IssuePasswordReset is host-only and requires the deployment lock and an
// interactive TTY in the caller. It does not change the password or MFA.
func IssuePasswordReset(ctx context.Context, db *sqlx.DB) (string, time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	a, err := readAccount(ctx, tx)
	if err != nil {
		return "", time.Time{}, err
	}
	now := timeNow()
	expires := now + passwordResetTTL
	if _, err = exec(ctx, tx, "revoke_password_resets"); err != nil {
		return "", time.Time{}, err
	}
	if _, err = exec(ctx, tx, "issue", digest(token), "password_reset", csrf, now, now, expires, a.Revision, false); err != nil {
		return "", time.Time{}, err
	}
	if err = recordEvent(ctx, tx, "password_reset_issued", now); err != nil {
		return "", time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return token, time.UnixMilli(expires).UTC(), nil
}

func (s *Service) resetPassword(ctx context.Context, q dbtx, username, token, password, address string) error {
	if err := s.checkLimit(ctx, q, address); err != nil {
		return err
	}
	row, err := s.lookup(ctx, q, token, "password_reset")
	if err != nil {
		return err
	}
	a, err := readAccount(ctx, q)
	if err != nil {
		return err
	}
	if row == nil || username != a.Username {
		if err = s.failure(ctx, q, address); err != nil {
			return err
		}
		return reject(401, "账号或重置码不正确，重置码也可能已过期或使用")
	}
	if err = ValidatePassword(password); err != nil {
		return reject(400, err.Error())
	}
	hash, err := encodePassword(password)
	if err != nil {
		return err
	}
	changed, err := exec(ctx, q, "password", hash)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("administrator account missing")
	}
	// Preserve enrolled MFA and recovery codes. All tokens, including this reset
	// token and remembered sessions, are revoked in the same transaction.
	if err = s.invalidate(ctx, q); err != nil {
		return err
	}
	if err = s.success(ctx, q, address); err != nil {
		return err
	}
	return recordEvent(ctx, q, "password_reset_succeeded", s.now())
}
