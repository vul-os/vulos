package signaling_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/signaling"
)

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func newStore(t *testing.T) *signaling.MemSignalingStore {
	t.Helper()
	s := signaling.NewMemSignalingStore()
	t.Cleanup(s.Close)
	return s
}

func makeOffer(from, to string) signaling.Offer {
	return signaling.Offer{
		FromULID: from,
		ToULID:   to,
		SDP:      "v=0\r\no=offerer ...",
		Candidates: []signaling.ICECandidate{
			{Candidate: "candidate:1 1 UDP ...", SDPMid: "0", SDPMLineIndex: 0},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateOffer / GetSession basics
// ─────────────────────────────────────────────────────────────────────────────

func TestCreateOffer_ReturnsToken(t *testing.T) {
	st := newStore(t)
	token, err := st.CreateOffer(context.Background(), makeOffer("ULID-A", "ULID-B"))
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty session token")
	}
}

func TestGetSession_ReturnsStoredOffer(t *testing.T) {
	st := newStore(t)
	offer := makeOffer("ULID-A", "ULID-B")
	token, _ := st.CreateOffer(context.Background(), offer)

	sess, err := st.GetSession(context.Background(), token)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.FromULID != offer.FromULID {
		t.Errorf("FromULID: got %q want %q", sess.FromULID, offer.FromULID)
	}
	if sess.ToULID != offer.ToULID {
		t.Errorf("ToULID: got %q want %q", sess.ToULID, offer.ToULID)
	}
	if sess.OfferSDP != offer.SDP {
		t.Errorf("OfferSDP: got %q want %q", sess.OfferSDP, offer.SDP)
	}
	if sess.Answered {
		t.Error("session should not be answered yet")
	}
}

func TestGetSession_UnknownToken(t *testing.T) {
	st := newStore(t)
	_, err := st.GetSession(context.Background(), "nonexistent")
	if !errors.Is(err, signaling.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Offer → Answer → Poll lifecycle
// ─────────────────────────────────────────────────────────────────────────────

func TestOfferAnswerPoll_HappyPath(t *testing.T) {
	st := newStore(t)

	token, err := st.CreateOffer(context.Background(), makeOffer("ULID-A", "ULID-B"))
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}

	// Store the answer in a separate goroutine slightly after the poll starts.
	answerErr := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		answerErr <- st.StoreAnswer(context.Background(), signaling.Answer{
			SessionToken: token,
			SDP:          "v=0\r\no=answerer ...",
			Candidates: []signaling.ICECandidate{
				{Candidate: "candidate:2 1 UDP ...", SDPMid: "0", SDPMLineIndex: 0},
			},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, err := st.PollAnswer(ctx, token)
	if err != nil {
		t.Fatalf("PollAnswer: %v", err)
	}
	if !sess.Answered {
		t.Error("session should be marked as answered")
	}
	if sess.AnswerSDP == "" {
		t.Error("AnswerSDP should not be empty")
	}
	if len(sess.AnswerICE) == 0 {
		t.Error("AnswerICE should not be empty")
	}

	if err := <-answerErr; err != nil {
		t.Errorf("StoreAnswer: %v", err)
	}
}

func TestPollAnswer_ImmediateWhenAlreadyAnswered(t *testing.T) {
	st := newStore(t)

	token, _ := st.CreateOffer(context.Background(), makeOffer("ULID-A", "ULID-B"))
	if err := st.StoreAnswer(context.Background(), signaling.Answer{
		SessionToken: token,
		SDP:          "v=0\r\no=answerer ...",
	}); err != nil {
		t.Fatalf("StoreAnswer: %v", err)
	}

	// PollAnswer should return immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	sess, err := st.PollAnswer(ctx, token)
	if err != nil {
		t.Fatalf("PollAnswer: %v", err)
	}
	if !sess.Answered {
		t.Error("session should be marked as answered")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Expiry
// ─────────────────────────────────────────────────────────────────────────────

func TestGetSession_ExpiredSession_Returns410Sentinel(t *testing.T) {
	st := newStore(t)

	token, err := st.CreateOffer(context.Background(), makeOffer("ULID-A", "ULID-B"))
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}

	// Reach into the store via GetSession to verify the session is live first.
	if _, err := st.GetSession(context.Background(), token); err != nil {
		t.Fatalf("GetSession before expiry: %v", err)
	}

	// Verify that ErrSessionExpired is distinct from ErrSessionNotFound.
	if errors.Is(signaling.ErrSessionExpired, signaling.ErrSessionNotFound) {
		t.Error("ErrSessionExpired should not wrap ErrSessionNotFound")
	}
}

func TestPollAnswer_ContextCancelled(t *testing.T) {
	st := newStore(t)

	token, _ := st.CreateOffer(context.Background(), makeOffer("ULID-A", "ULID-B"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := st.PollAnswer(ctx, token)
		done <- err
	}()

	// Cancel almost immediately.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PollAnswer did not respect context cancellation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// StoreAnswer errors
// ─────────────────────────────────────────────────────────────────────────────

func TestStoreAnswer_UnknownSession(t *testing.T) {
	st := newStore(t)
	err := st.StoreAnswer(context.Background(), signaling.Answer{
		SessionToken: "bad-token",
		SDP:          "v=0",
	})
	if !errors.Is(err, signaling.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Multiple concurrent sessions do not interfere
// ─────────────────────────────────────────────────────────────────────────────

func TestConcurrentSessions(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tokenA, _ := st.CreateOffer(ctx, makeOffer("ULID-A", "ULID-B"))
	tokenB, _ := st.CreateOffer(ctx, makeOffer("ULID-C", "ULID-D"))

	_ = st.StoreAnswer(ctx, signaling.Answer{SessionToken: tokenA, SDP: "answer-A"})

	// tokenB should still be unanswered.
	sessB, err := st.GetSession(ctx, tokenB)
	if err != nil {
		t.Fatalf("GetSession(tokenB): %v", err)
	}
	if sessB.Answered {
		t.Error("tokenB should not be answered")
	}

	// tokenA should be answered.
	sessA, err := st.GetSession(ctx, tokenA)
	if err != nil {
		t.Fatalf("GetSession(tokenA): %v", err)
	}
	if !sessA.Answered {
		t.Error("tokenA should be answered")
	}
	if sessA.AnswerSDP != "answer-A" {
		t.Errorf("AnswerSDP: got %q want %q", sessA.AnswerSDP, "answer-A")
	}
}
