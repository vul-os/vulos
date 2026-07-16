package superadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// paystackHTTPClient bounds outbound Paystack refund calls (HTTPTIMEOUT-01).
// http.DefaultClient has no timeout, so a hung Paystack endpoint could pin the
// calling goroutine (and its DB conn) indefinitely; this adds a hard ceiling.
var paystackHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
}

// callPaystackRefund calls the Paystack refund API for txnID.
// The Paystack secret key is read from PAYSTACK_SECRET_KEY env.
// Returns the parsed response body on success.
func callPaystackRefund(ctx context.Context, txnID string) (map[string]any, error) {
	secretKey := os.Getenv("PAYSTACK_SECRET_KEY")
	if secretKey == "" {
		return nil, fmt.Errorf("paystack: PAYSTACK_SECRET_KEY not set")
	}

	body := strings.NewReader(`{"transaction":"` + txnID + `"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.paystack.co/refund", body)
	if err != nil {
		return nil, fmt.Errorf("paystack: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := paystackHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paystack: refund request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("paystack: read response: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("paystack: parse response: %w", err)
	}

	if resp.StatusCode >= 400 {
		msg, _ := result["message"].(string)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return result, fmt.Errorf("paystack: refund failed: %s", msg)
	}

	return result, nil
}
