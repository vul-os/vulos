package abuse

import "context"

// ContentClassifier is the pluggable seam for body-content scoring (spam /
// phishing / toxic content models, etc). The default implementation
// (NoopClassifier) always returns score=0; a real model can be plugged in
// without changing call sites.
//
// Classify must be safe for concurrent use. Score convention:
//
//	score in [0,1] — 1.0 = highest confidence the content is abusive.
//
// Errors are surfaced to the caller; the detector treats an error as
// "no signal" and continues.
type ContentClassifier interface {
	Classify(ctx context.Context, body []byte) (score float64, err error)
}

// NoopClassifier is the zero-config default. It returns (0, nil) for every
// input — the system flags nothing until a real classifier is plugged in.
type NoopClassifier struct{}

// Classify implements ContentClassifier.
func (NoopClassifier) Classify(ctx context.Context, body []byte) (float64, error) {
	return 0, nil
}

// CSAMHashMatcher is the pluggable seam for PhotoDNA-style hash matching
// against a known-CSAM hash set. A real implementation must be supplied by the
// T&S team (see /docs/RISKS-HUMAN-TEAM.md §2) before any user-content storage
// product is operated; legal reporting to authorities (NCMEC / SAPS / FPB) is
// the responsibility of the human/legal team and is NEVER performed by this
// package.
//
// Match must be safe for concurrent use.
type CSAMHashMatcher interface {
	Match(ctx context.Context, hash []byte) (matched bool, err error)
}

// NoopCSAMMatcher is the safe default: it never matches. Production deploys
// MUST replace it with a real hash-matcher implementation.
type NoopCSAMMatcher struct{}

// Match implements CSAMHashMatcher.
func (NoopCSAMMatcher) Match(ctx context.Context, hash []byte) (bool, error) {
	return false, nil
}
