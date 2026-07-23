package superadmin

import (
	"context"
	"errors"
)

// RefundFunc submits a refund for a settled transaction to the payment
// processor and returns the processor's parsed response.
type RefundFunc func(ctx context.Context, txnID string) (map[string]any, error)

// ErrRefundUnavailable is returned by the default RefundProcessor when no
// payment processor is wired — i.e. any self-host / OSS deployment. Refunds are
// a commercial capability; there is nothing to refund against without a
// processor, so the admin console surfaces this cleanly instead of erroring.
var ErrRefundUnavailable = errors.New("superadmin: refunds unavailable in this deployment (no payment processor configured)")

// RefundProcessor is the seam for issuing refunds. The OSS control plane ships a
// no-op default; a commercial distributor injects a real processor (e.g.
// Paystack) at startup:
//
//	superadmin.RefundProcessor = mypaystack.Refund
//
// This keeps all payment-processor code out of the OSS module — the control
// plane only knows the abstract "issue a refund for this txn" capability.
var RefundProcessor RefundFunc = func(ctx context.Context, txnID string) (map[string]any, error) {
	return nil, ErrRefundUnavailable
}
