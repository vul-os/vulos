package files

import (
	"context"
	"testing"
	"time"
)

// fakeResolver is a programmable ShareResolver for ShareByEmail tests.
type fakeResolver struct {
	byEmail map[string]ShareRecipient
	err     error
}

func (f fakeResolver) ResolveRecipient(_ context.Context, email string) (ShareRecipient, error) {
	if f.err != nil {
		return ShareRecipient{}, f.err
	}
	return f.byEmail[email], nil // zero value = not found
}

// captureDeliverer records the last capability delivered.
type captureDeliverer struct {
	calls int
	last  CapabilityDelivery
	to    string
	err   error
}

func (c *captureDeliverer) DeliverCapability(_ context.Context, server string, d CapabilityDelivery) error {
	c.calls++
	c.to = server
	c.last = d
	return c.err
}

func TestShareByEmail_CoCloud_UsesACLPath(t *testing.T) {
	svc, _ := newTestService(t)
	n := seedFile(t, svc, "userOwner", "doc.txt", "hi")

	svc.WithShareResolver(fakeResolver{byEmail: map[string]ShareRecipient{
		"bob@vulos.org": {PrincipalID: "userBob", DisplayName: "bob@vulos.org"},
	}}, nil)

	res, err := svc.ShareByEmail(context.Background(), "userOwner", n.ID, "bob@vulos.org", RoleViewer, "", 0)
	if err != nil {
		t.Fatalf("ShareByEmail: %v", err)
	}
	if res.Mode != "co-cloud" || res.ACL == nil || res.PrincipalID != "userBob" {
		t.Fatalf("unexpected result: %+v", res)
	}
	// Bob can now read per role via the ACL.
	if r, _ := svc.EffectiveRole("userBob", n.ID); r != RoleViewer {
		t.Errorf("EffectiveRole(bob) = %q, want viewer", r)
	}
}

func TestShareByEmail_Remote_MintsBoundCapabilityAndDelivers(t *testing.T) {
	svc, _ := newTestService(t)
	signer := newTestSigner(t)
	deliv := &captureDeliverer{}
	svc.WithPeer(signer, nil, t.TempDir())
	n := seedFile(t, svc, "userOwner", "doc.txt", "hi")

	const recipVula = "k:remote-recipient"
	svc.WithShareResolver(fakeResolver{byEmail: map[string]ShareRecipient{
		"carol@example.com": {VulaID: recipVula, Server: "carol.example.com"},
	}}, deliv)

	res, err := svc.ShareByEmail(context.Background(), "userOwner", n.ID, "carol@example.com", RoleEditor, "https://owner.example", time.Hour)
	if err != nil {
		t.Fatalf("ShareByEmail: %v", err)
	}
	if res.Mode != "remote" || res.Capability == nil || res.Link == "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	// Per-document, role-scoped, bound to the recipient VulaID.
	if res.Capability.NodeID != n.ID {
		t.Errorf("capability NodeID = %q, want %q", res.Capability.NodeID, n.ID)
	}
	if res.Capability.Access != RoleEditor {
		t.Errorf("capability Access = %q, want editor", res.Capability.Access)
	}
	if res.Capability.Recipient != recipVula {
		t.Errorf("capability Recipient = %q, want %q (bound)", res.Capability.Recipient, recipVula)
	}
	// Delivered to the recipient server intake.
	if !res.Delivered || deliv.calls != 1 || deliv.to != "carol.example.com" {
		t.Fatalf("delivery: delivered=%v calls=%d to=%q", res.Delivered, deliv.calls, deliv.to)
	}
	if deliv.last.RecipientVulaID != recipVula || deliv.last.Link != res.Link {
		t.Errorf("delivery payload mismatch: %+v", deliv.last)
	}
}

func TestShareByEmail_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	n := seedFile(t, svc, "userOwner", "doc.txt", "hi")
	svc.WithShareResolver(fakeResolver{byEmail: map[string]ShareRecipient{}}, nil)

	_, err := svc.ShareByEmail(context.Background(), "userOwner", n.ID, "nobody@nowhere", RoleViewer, "", 0)
	if err != ErrRecipientNotFound {
		t.Fatalf("err = %v, want ErrRecipientNotFound", err)
	}
}

func TestShareByEmail_NoResolverWired(t *testing.T) {
	svc, _ := newTestService(t)
	n := seedFile(t, svc, "userOwner", "doc.txt", "hi")
	_, err := svc.ShareByEmail(context.Background(), "userOwner", n.ID, "x@y.z", RoleViewer, "", 0)
	if err != ErrShareResolveUnavailable {
		t.Fatalf("err = %v, want ErrShareResolveUnavailable", err)
	}
}

func TestShareByEmail_NonOwnerForbidden(t *testing.T) {
	svc, _ := newTestService(t)
	n := seedFile(t, svc, "userOwner", "doc.txt", "hi")
	svc.WithShareResolver(fakeResolver{byEmail: map[string]ShareRecipient{
		"bob@vulos.org": {PrincipalID: "userBob"},
	}}, nil)
	_, err := svc.ShareByEmail(context.Background(), "userMallory", n.ID, "bob@vulos.org", RoleViewer, "", 0)
	if err != ErrForbidden {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}
