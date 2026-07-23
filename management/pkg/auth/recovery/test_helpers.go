// test_helpers.go — exported helpers used only in tests for the recovery package.
package recovery

import (
	"context"
	"fmt"
	"time"
)

// BackdateEligibleAt overwrites the recovery_eligible_at for a request.
// Used in tests to simulate the 14-day window having elapsed.
func (s *Store) BackdateEligibleAt(ctx context.Context, requestID string, newTime time.Time) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE recovery_requests SET recovery_eligible_at=?, updated_at=?
		WHERE id=?`),
		newTime.UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339),
		requestID,
	)
	if err != nil {
		return fmt.Errorf("recovery: backdate eligible_at: %w", err)
	}
	return nil
}

// CancelAllPending cancels all pending/reviewing requests for an account.
// Used in tests to allow a fresh submission.
func (s *Store) CancelAllPending(ctx context.Context, accountID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE recovery_requests SET status='cancelled', cancelled_at=?, updated_at=?
		WHERE account_id=? AND status IN ('pending','reviewing')`),
		now, now, accountID,
	)
	return err
}
