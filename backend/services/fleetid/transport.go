// transport.go — the INITIATOR side of gathering a break-glass vouch quorum
// over the network: request a vouch from each of several peer boxes over an
// injectable Transport SEAM, collect signed VouchCerts, stop at threshold.
//
// # Why a seam
//
// Today the operator gathers vouches "out of band" (copy-paste a payload
// hash to other box owners, collect certs back some other way). GatherQuorum
// automates the fan-out/collect loop but the actual wire protocol is an
// interface (Transport) so it is trivially testable (an in-process fake) and
// swappable (HTTPTransport is ONE implementation; a relay/fabric-native
// transport can implement the same interface later without touching
// GatherQuorum or the peer-side handler at all — see the INTEGRATION
// MANIFEST for where such a transport would be wired in).
//
// # What GatherQuorum does NOT do
//
// It does not itself decide whether the resulting bundle is sufficient to
// authorize anything — that is, and remains, vouch.go's VerifyQuorum alone.
// GatherQuorum's own insufficiency check (ErrGatherInsufficient) is a
// networking-layer convenience ("we ran out of peers to ask") and must never
// be treated as a substitute for calling VerifyQuorum before using the
// bundle; every caller in this codebase (devicekey.BreakGlassRotate) already
// re-verifies independently and this package does not change that.
package fleetid

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"vulos/backend/services/peering"
)

// ErrGatherInsufficient is returned by GatherQuorum when every peer has been
// tried (and, where applicable, polling has been exhausted) and the number of
// distinct certs collected is still below the effective threshold. Like
// vouch.go's ErrInsufficientQuorum, the partial GatherResult is still
// returned so a caller can inspect what happened (which peers denied,
// which never approved, which were unreachable) — but the bundle must be
// treated as NOT authorizing anything.
var ErrGatherInsufficient = errors.New("fleetid: insufficient vouches gathered from peers")

// Transport is the injectable SEAM by which the initiator asks ONE peer,
// identified by endpoint (transport-defined — HTTPTransport treats it as a
// base URL; a test fake might treat it as an in-process peer name), to vouch
// for req. Implementations MUST return one of the sentinel errors below
// (wrapped is fine) when the peer did not grant a cert, so GatherQuorum's
// retry/backoff logic can tell "try again later" (ErrVouchPending) apart from
// "do not ask again" (ErrVouchDenied) apart from "something is broken"
// (ErrVouchTransport). Returning (nil, nil) is never valid.
type Transport interface {
	RequestVouch(ctx context.Context, endpoint string, req VouchRequest) (*VouchCert, error)
}

// Sentinel errors an HTTPTransport (or any other Transport implementation)
// wraps into its returned error so GatherQuorum can classify the outcome.
var (
	// ErrVouchDenied means the peer's approval policy explicitly refused (or
	// the request was refused as a self-vouch). Retrying the SAME peer for
	// the SAME request id is pointless; GatherQuorum does not retry on this.
	ErrVouchDenied = errors.New("fleetid: peer denied the vouch request")
	// ErrVouchPending means the peer has not yet decided (typically: no
	// operator has approved it yet). GatherQuorum may retry this peer later
	// within the configured polling window.
	ErrVouchPending = errors.New("fleetid: peer vouch request is pending approval")
	// ErrVouchTransport means the request could not even be delivered/parsed
	// (network error, malformed response, unexpected status code).
	ErrVouchTransport = errors.New("fleetid: vouch transport failure")
)

// HTTPTransport is the default Transport: it POSTs a VouchRequest as JSON to
// endpoint+Path and decodes a vouchWireResponse. endpoint is a base URL (e.g.
// "https://peer-box.example:8443"); Path defaults to DefaultVouchPath.
type HTTPTransport struct {
	// Client is the http.Client used to make requests. If nil, a client with
	// a conservative default timeout is used (callers relying on ctx
	// deadlines instead should set Client explicitly with Timeout: 0).
	Client *http.Client
	// Path overrides DefaultVouchPath. Rarely needed.
	Path string
}

// httpTransportDefaultTimeout bounds an HTTPTransport created with Client ==
// nil. Per-call ctx deadlines (set by GatherQuorum via GatherOptions) still
// apply on top of this and are typically tighter.
const httpTransportDefaultTimeout = 30 * time.Second

func (t HTTPTransport) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: httpTransportDefaultTimeout}
}

func (t HTTPTransport) path() string {
	if t.Path != "" {
		return t.Path
	}
	return DefaultVouchPath
}

// RequestVouch implements Transport over plain HTTP(S) + JSON.
func (t HTTPTransport) RequestVouch(ctx context.Context, endpoint string, req VouchRequest) (*VouchCert, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %v", ErrVouchTransport, err)
	}
	url := strings.TrimSuffix(endpoint, "/") + t.path()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrVouchTransport, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVouchTransport, err)
	}
	defer resp.Body.Close()

	var wire vouchWireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxVouchBodyBytes)).Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: decode response (status %d): %v", ErrVouchTransport, resp.StatusCode, err)
	}

	switch wire.Status {
	case "granted":
		if wire.Cert == nil {
			return nil, fmt.Errorf("%w: peer reported granted with no cert", ErrVouchTransport)
		}
		return wire.Cert, nil
	case "pending":
		return nil, fmt.Errorf("%w: %s", ErrVouchPending, wire.Reason)
	case "denied":
		return nil, fmt.Errorf("%w: %s", ErrVouchDenied, wire.Reason)
	default:
		return nil, fmt.Errorf("%w: unexpected peer status %q (http %d): %s", ErrVouchTransport, wire.Status, resp.StatusCode, wire.Reason)
	}
}

// GatherRequest describes the quorum an initiator wants to assemble. It maps
// 1:1 onto the fields VerifyQuorum itself will check, so a bundle GatherQuorum
// returns is, modulo peer honesty and threshold, exactly what VerifyQuorum
// expects.
type GatherRequest struct {
	Action      string
	SubjectID   string // this box's OWN fleet identity — never a peer's
	PayloadHash []byte
	RequestID   string
	// SubjectKey is the private half of SubjectID's fleet key. REQUIRED: the
	// peer endpoint authenticates every request by verifying a signature made
	// with it (VerifyVouchRequest), so a GatherQuorum with no key would be
	// 401'd by every peer. Since SubjectID is always this box's OWN identity,
	// the initiator always holds it.
	SubjectKey ed25519.PrivateKey
	// Peers is the candidate list of OTHER boxes to ask, in the order they
	// will be tried. Transport-defined format (see Transport's doc comment).
	Peers []string
	// Threshold is the caller's desired threshold. Like VerifyQuorum, it is
	// floored at MinThreshold — GatherQuorum will keep asking peers until it
	// has at least max(Threshold, MinThreshold) distinct certs or runs out.
	Threshold int
}

// GatherOptions tunes how hard GatherQuorum tries before giving up.
type GatherOptions struct {
	// PerAttemptTimeout bounds a single RequestVouch call. Defaults to 10s.
	PerAttemptTimeout time.Duration
	// PollInterval is how long GatherQuorum waits before re-asking the SAME
	// peer after that peer returns ErrVouchPending (an operator may approve
	// shortly after being notified out-of-band). Zero disables retrying —
	// each peer is asked exactly once.
	PollInterval time.Duration
	// PollDeadline bounds the TOTAL time GatherQuorum will spend retrying a
	// single pending peer before moving on. Ignored when PollInterval <= 0.
	PollDeadline time.Duration
}

func (o GatherOptions) perAttemptTimeout() time.Duration {
	if o.PerAttemptTimeout > 0 {
		return o.PerAttemptTimeout
	}
	return 10 * time.Second
}

// PeerOutcome records what happened when GatherQuorum asked one peer, for
// audit/debugging — mirroring the spirit of vouch.go's DroppedVouch (every
// non-counted input gets an attributed reason).
type PeerOutcome struct {
	Endpoint string
	Counted  bool   // true iff this peer's cert was counted in the returned bundle
	Reason   string // "" when Counted; otherwise a human-readable outcome
}

// GatherResult is the outcome of one GatherQuorum call.
type GatherResult struct {
	// Certs is the gathered bundle — distinct by voucher identity, in the
	// order collected. Pass this directly as VerifyQuorum's certs argument
	// (or embed it in a RotationCert/RecoveryCert etc.); GatherQuorum never
	// mutates or reorders beyond dropping duplicates/self-vouches.
	Certs []VouchCert
	// Threshold is the EFFECTIVE threshold GatherQuorum aimed for (always >=
	// MinThreshold), mirroring vouch.go Result.Threshold.
	Threshold int
	// Outcomes lists what happened with every peer that was asked, in the
	// order asked.
	Outcomes []PeerOutcome
}

// GatherQuorum asks each of greq.Peers, in order, to vouch for greq's
// (action, subject, payload hash, request id) over transport, collecting
// distinct signed VouchCerts until it has gathered at least
// max(greq.Threshold, MinThreshold) of them or has tried every peer.
//
// Deduplication is by VoucherVulosID (a peer that somehow appears at two
// endpoints, or double-answers, counts once) and the subject's own identity
// is NEVER counted even if some peer's response were to (incorrectly) claim
// it — this mirrors, defense-in-depth, the self-exclusion VerifyQuorum
// itself enforces; a bug or malicious peer here can waste an initiator's time
// but can never smuggle a self-vouch into the returned bundle.
//
// GatherQuorum returns ErrGatherInsufficient (with the partial GatherResult
// still populated) when it runs out of peers before reaching the effective
// threshold. This is a networking-layer signal ONLY — callers MUST still
// pass the returned bundle through fleetid.VerifyQuorum (directly, or via
// devicekey.BreakGlassRotate, which does so internally) before relying on it
// for anything; GatherQuorum is not, and must never become, an alternative
// authorization chokepoint.
func GatherQuorum(ctx context.Context, transport Transport, greq GatherRequest, opts GatherOptions) (GatherResult, error) {
	if transport == nil {
		return GatherResult{}, errors.New("fleetid: GatherQuorum: nil transport")
	}
	if !validAction(greq.Action) {
		return GatherResult{}, fmt.Errorf("fleetid: GatherQuorum: unknown action %q", greq.Action)
	}
	if greq.SubjectID == "" {
		return GatherResult{}, errors.New("fleetid: GatherQuorum: empty subject id")
	}
	if len(greq.PayloadHash) == 0 {
		return GatherResult{}, errors.New("fleetid: GatherQuorum: empty payload hash")
	}
	if greq.RequestID == "" {
		return GatherResult{}, errors.New("fleetid: GatherQuorum: empty request id")
	}
	if len(greq.SubjectKey) != ed25519.PrivateKeySize {
		return GatherResult{}, errors.New("fleetid: GatherQuorum: SubjectKey is required (peers authenticate the request by its signature)")
	}
	if peering.EncodeVulosID(greq.SubjectKey.Public().(ed25519.PublicKey)) != greq.SubjectID {
		return GatherResult{}, errors.New("fleetid: GatherQuorum: SubjectKey does not match SubjectID")
	}

	effThreshold := greq.Threshold
	if effThreshold < MinThreshold {
		effThreshold = MinThreshold
	}

	wireReq := VouchRequest{
		Action:      greq.Action,
		SubjectID:   greq.SubjectID,
		PayloadHash: base64.RawURLEncoding.EncodeToString(greq.PayloadHash),
		RequestID:   greq.RequestID,
	}

	res := GatherResult{Threshold: effThreshold}
	seen := make(map[string]bool)

	for _, endpoint := range greq.Peers {
		if len(res.Certs) >= effThreshold {
			break
		}

		cert, err := attemptPeer(ctx, transport, endpoint, wireReq, greq.SubjectKey, opts)
		if err != nil {
			res.Outcomes = append(res.Outcomes, PeerOutcome{Endpoint: endpoint, Counted: false, Reason: err.Error()})
			continue
		}

		// Defense in depth: never trust the peer's own framing blindly, even
		// though the wire response already came back "granted".
		if cert.VoucherVulosID == "" {
			res.Outcomes = append(res.Outcomes, PeerOutcome{Endpoint: endpoint, Counted: false, Reason: "malformed cert: empty voucher id"})
			continue
		}
		if cert.VoucherVulosID == greq.SubjectID {
			res.Outcomes = append(res.Outcomes, PeerOutcome{Endpoint: endpoint, Counted: false, Reason: "refused: cert vouches for the subject itself"})
			continue
		}
		if seen[cert.VoucherVulosID] {
			res.Outcomes = append(res.Outcomes, PeerOutcome{Endpoint: endpoint, Counted: false, Reason: "duplicate of an already-counted voucher"})
			continue
		}
		seen[cert.VoucherVulosID] = true
		res.Certs = append(res.Certs, *cert)
		res.Outcomes = append(res.Outcomes, PeerOutcome{Endpoint: endpoint, Counted: true})
	}

	if len(res.Certs) < effThreshold {
		return res, fmt.Errorf("%w: %d gathered < threshold %d", ErrGatherInsufficient, len(res.Certs), effThreshold)
	}
	return res, nil
}

// attemptPeer asks one peer, retrying while it reports ErrVouchPending within
// opts.PollDeadline (if PollInterval > 0). Any other error (denied, transport
// failure, or ctx cancellation) returns immediately.
func attemptPeer(ctx context.Context, transport Transport, endpoint string, wireReq VouchRequest, subjectKey ed25519.PrivateKey, opts GatherOptions) (*VouchCert, error) {
	deadline := time.Now().Add(opts.PollDeadline)
	for {
		// Sign per attempt, not once per gather: the peer enforces a freshness
		// window, and polling a pending peer can outlast it.
		signed := wireReq
		if err := SignVouchRequest(subjectKey, &signed, time.Now()); err != nil {
			return nil, fmt.Errorf("%w: sign request: %v", ErrVouchTransport, err)
		}
		attemptCtx, cancel := context.WithTimeout(ctx, opts.perAttemptTimeout())
		cert, err := transport.RequestVouch(attemptCtx, endpoint, signed)
		cancel()
		if err == nil {
			return cert, nil
		}
		if !errors.Is(err, ErrVouchPending) || opts.PollInterval <= 0 {
			return nil, err
		}
		if time.Now().Add(opts.PollInterval).After(deadline) {
			return nil, err
		}
		timer := time.NewTimer(opts.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
