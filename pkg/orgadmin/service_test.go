package orgadmin

import (
	"context"
	"errors"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ──────────────────────────────────────────────────────────────────────────
// Mock readers (table-driven). Each implements one seam; methods return
// whatever the test seeds.
// ──────────────────────────────────────────────────────────────────────────

type mockMembers struct {
	members      []Member
	listErr      error
	inviteErr    error
	inviteResp   InviteResponse
	removeErr    error
	setRoleErr   error
	setNameErr   error
	lastRemoveID string
	lastRole     string
	lastInviteNm string // last name passed to Invite
	lastNameID   string // last memberID passed to SetMemberName
	lastName     string // last name passed to SetMemberName
}

func (m *mockMembers) ListMembers(_ context.Context, _ string) ([]Member, error) {
	return m.members, m.listErr
}
func (m *mockMembers) Invite(_ context.Context, _, email, role, name string) (InviteResponse, error) {
	m.lastInviteNm = name
	if m.inviteErr != nil {
		return InviteResponse{}, m.inviteErr
	}
	r := m.inviteResp
	if r.Email == "" {
		r = InviteResponse{ID: "inv-x", Email: email, Role: role, State: "pending"}
	}
	return r, nil
}
func (m *mockMembers) RemoveMember(_ context.Context, _, id string) error {
	m.lastRemoveID = id
	return m.removeErr
}
func (m *mockMembers) SetRole(_ context.Context, _, _, role string) error {
	m.lastRole = role
	return m.setRoleErr
}
func (m *mockMembers) SetMemberName(_ context.Context, _, memberID, name string) error {
	m.lastNameID = memberID
	m.lastName = name
	return m.setNameErr
}

type mockInstances struct {
	resp InstancesResponse
	err  error
}

func (m *mockInstances) ListInstances(_ context.Context, _ string) (InstancesResponse, error) {
	return m.resp, m.err
}

type mockUsage struct {
	resp UsageResponse
	err  error
}

func (m *mockUsage) Usage(_ context.Context, _ string) (UsageResponse, error) {
	return m.resp, m.err
}

type mockQuota struct {
	resp QuotasResponse
	err  error
}

func (m *mockQuota) Quotas(_ context.Context, _ string) (QuotasResponse, error) {
	return m.resp, m.err
}

type mockBilling struct {
	resp BillingSummaryResponse
	err  error
}

func (m *mockBilling) Summary(_ context.Context, _ string) (BillingSummaryResponse, error) {
	return m.resp, m.err
}

// ──────────────────────────────────────────────────────────────────────────
// Members
// ──────────────────────────────────────────────────────────────────────────

func TestService_ListMembers(t *testing.T) {
	t.Run("nil store → empty, non-nil slice", func(t *testing.T) {
		svc := &Service{}
		got, err := svc.ListMembers(context.Background(), "t1")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("want empty non-nil slice, got %#v", got)
		}
	})
	t.Run("passes through members", func(t *testing.T) {
		svc := &Service{Members: &mockMembers{members: []Member{{ID: "a", Email: "a@x.io", Role: "owner"}}}}
		got, err := svc.ListMembers(context.Background(), "t1")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 1 || got[0].ID != "a" {
			t.Fatalf("unexpected members: %#v", got)
		}
	})
	t.Run("error propagates", func(t *testing.T) {
		svc := &Service{Members: &mockMembers{listErr: errors.New("db down")}}
		if _, err := svc.ListMembers(context.Background(), "t1"); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestService_CreateInvite(t *testing.T) {
	cases := []struct {
		name      string
		email     string
		role      string
		inName    string // display name passed to CreateInvite
		members   *mockMembers
		invite    InviteStore
		wantErr   error
		wantRole  string // expected normalised role passed to backing
		wantState string
		wantName  string // expected name forwarded to the members backing
	}{
		{name: "valid via members", email: "New@X.io", role: "Admin",
			members: &mockMembers{}, wantState: "pending", wantRole: "admin"},
		{name: "name forwarded + trimmed", email: "ada@x.io", role: "member", inName: "  Ada Lovelace  ",
			members: &mockMembers{}, wantState: "pending", wantRole: "member", wantName: "Ada Lovelace"},
		{name: "empty email", email: "", role: "member",
			members: &mockMembers{}, wantErr: ErrInvalidInput},
		{name: "no at sign", email: "nope", role: "member",
			members: &mockMembers{}, wantErr: ErrInvalidInput},
		{name: "bad role", email: "a@x.io", role: "superuser",
			members: &mockMembers{}, wantErr: ErrInvalidRole},
		// PRIV-ESC (M1): an invite must never assign the owner role — otherwise an
		// admin (who holds ActionMemberInvite but is barred from member.role) could
		// invite+accept an owner-role email and escalate to owner.
		{name: "owner role refused", email: "a@x.io", role: "owner",
			members: &mockMembers{}, wantErr: ErrInvalidRole},
		{name: "owner role refused (alias case)", email: "a@x.io", role: "Owner",
			members: &mockMembers{}, wantErr: ErrInvalidRole},
		{name: "members unavailable falls back to invite store", email: "a@x.io", role: "Member",
			members: &mockMembers{inviteErr: ErrInviteStoreUnavailable}, invite: NewMemStore(), wantState: "pending"},
		{name: "no backing at all", email: "a@x.io", role: "member",
			members: nil, invite: nil, wantErr: ErrInviteStoreUnavailable},
		{name: "members hard error propagates", email: "a@x.io", role: "member",
			members: &mockMembers{inviteErr: errors.New("boom")}, wantErr: nil}, // see assertion below
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{Invite: tc.invite}
			if tc.members != nil {
				svc.Members = tc.members
			}
			resp, err := svc.CreateInvite(context.Background(), "t1", tc.email, tc.role, tc.inName)
			if tc.name == "members hard error propagates" {
				if err == nil || errors.Is(err, ErrInviteStoreUnavailable) {
					t.Fatalf("want non-sentinel hard error, got %v", err)
				}
				return
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err = %v", err)
			}
			if tc.wantState != "" && resp.State != tc.wantState {
				t.Fatalf("state = %q, want %q", resp.State, tc.wantState)
			}
			if tc.wantRole != "" && tc.members != nil {
				if resp.Role != tc.wantRole {
					t.Fatalf("role = %q, want %q", resp.Role, tc.wantRole)
				}
			}
			if tc.wantName != "" && tc.members != nil {
				if tc.members.lastInviteNm != tc.wantName {
					t.Fatalf("forwarded name = %q, want %q", tc.members.lastInviteNm, tc.wantName)
				}
			}
		})
	}
}

func TestService_SetMemberName(t *testing.T) {
	t.Run("nil store → not found", func(t *testing.T) {
		svc := &Service{}
		if err := svc.SetMemberName(context.Background(), "t1", "m1", "Ada"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("empty id → invalid", func(t *testing.T) {
		svc := &Service{Members: &mockMembers{}}
		if err := svc.SetMemberName(context.Background(), "t1", "  ", "Ada"); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})
	t.Run("passes id + trimmed name through", func(t *testing.T) {
		m := &mockMembers{}
		svc := &Service{Members: m}
		if err := svc.SetMemberName(context.Background(), "t1", "m1", "  Ada Lovelace  "); err != nil {
			t.Fatalf("err = %v", err)
		}
		if m.lastNameID != "m1" || m.lastName != "Ada Lovelace" {
			t.Fatalf("forwarded id=%q name=%q", m.lastNameID, m.lastName)
		}
	})
	t.Run("empty name clears (allowed)", func(t *testing.T) {
		m := &mockMembers{}
		svc := &Service{Members: m}
		if err := svc.SetMemberName(context.Background(), "t1", "m1", "   "); err != nil {
			t.Fatalf("err = %v", err)
		}
		if m.lastName != "" {
			t.Fatalf("name = %q, want empty (cleared)", m.lastName)
		}
	})
	t.Run("not-found propagates", func(t *testing.T) {
		svc := &Service{Members: &mockMembers{setNameErr: ErrNotFound}}
		if err := svc.SetMemberName(context.Background(), "t1", "m1", "Ada"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestService_RemoveMember(t *testing.T) {
	t.Run("nil store → not found", func(t *testing.T) {
		svc := &Service{}
		if err := svc.RemoveMember(context.Background(), "t1", "m1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("empty id → invalid", func(t *testing.T) {
		svc := &Service{Members: &mockMembers{}}
		if err := svc.RemoveMember(context.Background(), "t1", "  "); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})
	t.Run("passes id through", func(t *testing.T) {
		m := &mockMembers{}
		svc := &Service{Members: m}
		if err := svc.RemoveMember(context.Background(), "t1", "m1"); err != nil {
			t.Fatalf("err = %v", err)
		}
		if m.lastRemoveID != "m1" {
			t.Fatalf("lastRemoveID = %q", m.lastRemoveID)
		}
	})
	t.Run("cross-tenant not-found propagates", func(t *testing.T) {
		svc := &Service{Members: &mockMembers{removeErr: ErrNotFound}}
		if err := svc.RemoveMember(context.Background(), "t1", "m1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestService_ChangeRole(t *testing.T) {
	t.Run("normalises role", func(t *testing.T) {
		m := &mockMembers{}
		svc := &Service{Members: m}
		if err := svc.ChangeRole(context.Background(), "t1", "m1", "Admin"); err != nil {
			t.Fatalf("err = %v", err)
		}
		if m.lastRole != "admin" {
			t.Fatalf("lastRole = %q, want admin", m.lastRole)
		}
	})
	t.Run("bad role rejected before store", func(t *testing.T) {
		m := &mockMembers{}
		svc := &Service{Members: m}
		if err := svc.ChangeRole(context.Background(), "t1", "m1", "wizard"); !errors.Is(err, ErrInvalidRole) {
			t.Fatalf("err = %v, want ErrInvalidRole", err)
		}
		if m.lastRole != "" {
			t.Fatal("store should not have been called")
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────
// Read-through tabs (instances / usage / quotas / billing)
// ──────────────────────────────────────────────────────────────────────────

func TestService_ListInstances(t *testing.T) {
	t.Run("nil store → empty non-nil arrays", func(t *testing.T) {
		svc := &Service{}
		out, err := svc.ListInstances(context.Background(), "t1")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if out.Pooled == nil || out.Dedicated == nil || out.BYO == nil {
			t.Fatalf("arrays must be non-nil: %#v", out)
		}
	})
	t.Run("nil sub-slices normalised", func(t *testing.T) {
		svc := &Service{Instances: &mockInstances{resp: InstancesResponse{Pooled: []PooledInstance{{ID: "p1", Region: "fra"}}}}}
		out, err := svc.ListInstances(context.Background(), "t1")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out.Pooled) != 1 || out.Dedicated == nil || out.BYO == nil {
			t.Fatalf("unexpected: %#v", out)
		}
	})
}

func TestService_Usage(t *testing.T) {
	svc := &Service{UsageR: &mockUsage{resp: UsageResponse{ActiveUsers: 7, GPUHours: GPUHours{L40S: 1.5}}}}
	out, err := svc.Usage(context.Background(), "t1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.ActiveUsers != 7 || out.GPUHours.L40S != 1.5 {
		t.Fatalf("unexpected usage: %#v", out)
	}
	// nil reader → structurally-correct zero.
	if _, err := (&Service{}).Usage(context.Background(), "t1"); err != nil {
		t.Fatalf("nil reader err = %v", err)
	}
}

func TestService_Quotas(t *testing.T) {
	svc := &Service{QuotaR: &mockQuota{resp: QuotasResponse{Tier: "pro"}}}
	out, err := svc.Quotas(context.Background(), "t1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.Tier != "pro" || out.Items == nil {
		t.Fatalf("unexpected: %#v", out)
	}
	// nil reader → free + empty items.
	def, _ := (&Service{}).Quotas(context.Background(), "t1")
	if def.Tier != "free" || def.Items == nil {
		t.Fatalf("default quotas wrong: %#v", def)
	}
}

func TestService_BillingSummary(t *testing.T) {
	svc := &Service{Billing: &mockBilling{resp: BillingSummaryResponse{Rate: 17, WalletZAR: 100}}}
	out, err := svc.BillingSummary(context.Background(), "t1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.Rate != 17 || out.WalletZAR != 100 {
		t.Fatalf("unexpected: %#v", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Backup mode
// ──────────────────────────────────────────────────────────────────────────

func TestService_BackupMode_GetDefault(t *testing.T) {
	svc := &Service{Backup: NewMemStore()}
	out, err := svc.GetBackupMode(context.Background(), "t1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.Mode != BackupModeCentral || out.PerLocation == nil {
		t.Fatalf("default backup mode wrong: %#v", out)
	}
}

func TestService_BackupMode_SetRoundtrip(t *testing.T) {
	svc := &Service{Backup: NewMemStore()}
	req := BackupModeRequest{
		Mode: BackupModeLocal,
		PerLocation: []PerLocationMode{
			{ID: "loc1", Label: "EU", Mode: BackupModeCentral},
		},
	}
	out, err := svc.SetBackupMode(context.Background(), "t1", req)
	if err != nil {
		t.Fatalf("set err = %v", err)
	}
	if out.Mode != BackupModeLocal || len(out.PerLocation) != 1 || out.PerLocation[0].ID != "loc1" {
		t.Fatalf("roundtrip wrong: %#v", out)
	}
	// Re-get confirms persistence.
	got, _ := svc.GetBackupMode(context.Background(), "t1")
	if got.Mode != BackupModeLocal {
		t.Fatalf("persisted mode = %q", got.Mode)
	}
}

func TestService_BackupMode_Invalid(t *testing.T) {
	svc := &Service{Backup: NewMemStore()}
	if _, err := svc.SetBackupMode(context.Background(), "t1", BackupModeRequest{Mode: "weird"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	// Bad per-location mode also rejected.
	bad := BackupModeRequest{Mode: BackupModeCentral, PerLocation: []PerLocationMode{{ID: "l", Mode: "bogus"}}}
	if _, err := svc.SetBackupMode(context.Background(), "t1", bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("per-loc err = %v, want ErrInvalidInput", err)
	}
}

func TestService_BackupMode_NoStore(t *testing.T) {
	// GET with no store → central default (graceful).
	out, err := (&Service{}).GetBackupMode(context.Background(), "t1")
	if err != nil || out.Mode != BackupModeCentral {
		t.Fatalf("nil-store get: out=%#v err=%v", out, err)
	}
	// SET with no store → hard error (so route returns 500, not silent success).
	if _, err := (&Service{}).SetBackupMode(context.Background(), "t1", BackupModeRequest{Mode: BackupModeLocal}); err == nil {
		t.Fatal("want error when no backup store configured")
	}
}

// ──────────────────────────────────────────────────────────────────────────
// SQL store backup-mode roundtrip (pure-Go modernc sqlite).
// ──────────────────────────────────────────────────────────────────────────

func TestSQLStore_BackupModeRoundtrip(t *testing.T) {
	cdb, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := OpenSQLStore(cdb)
	if err != nil {
		cdb.Close()
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// Default for unseen tenant.
	def, err := st.GetBackupMode(ctx, "t-sql")
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	if def.Mode != BackupModeCentral || def.PerLocation == nil {
		t.Fatalf("default wrong: %#v", def)
	}

	// Set then get.
	if err := st.SetBackupMode(ctx, "t-sql", BackupModeRequest{
		Mode:        BackupModeLocal,
		PerLocation: []PerLocationMode{{ID: "l1", Label: "HQ", Hostname: "hq.local", Mode: BackupModeLocal}},
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := st.GetBackupMode(ctx, "t-sql")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Mode != BackupModeLocal || len(got.PerLocation) != 1 || got.PerLocation[0].Hostname != "hq.local" {
		t.Fatalf("roundtrip wrong: %#v", got)
	}

	// Invite roundtrip (standalone store path) — with a name persisted on the row.
	inv, err := st.CreateInvite(ctx, "t-sql", "x@y.io", "member", "Grace Hopper")
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if inv.ID == "" || inv.State != "pending" {
		t.Fatalf("bad invite: %#v", inv)
	}
	// The name is persisted on the row (not surfaced on the ack); confirm the
	// column exists + stored the value.
	var gotName string
	if qerr := st.db.QueryRowContext(ctx, st.db.Rebind(`SELECT display_name FROM org_invites WHERE id = ?`), inv.ID).Scan(&gotName); qerr != nil {
		t.Fatalf("read invite name: %v", qerr)
	}
	if gotName != "Grace Hopper" {
		t.Fatalf("persisted invite name = %q, want %q", gotName, "Grace Hopper")
	}
}
