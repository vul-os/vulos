// cloud_enroll_adapter.go — UNIFIED-SIGNIN glue: adapts the cloudenroll
// Manager to the auth.CloudEnrollment seam so /api/auth/cloud/login can
// resolve this box's device identity and /api/auth/cloud/enroll/{start,status}
// can drive the RFC 8628 owner-approval grant. Wired in main.go next to the
// existing enroller.Load() block.
package main

import (
	"context"

	"vulos/backend/services/auth"
	"vulos/backend/services/cloudenroll"
)

type cloudEnrollAdapter struct{ m *cloudenroll.Manager }

func (a cloudEnrollAdapter) Identity() (ulid, account string, err error) {
	return a.m.Identity()
}

func (a cloudEnrollAdapter) Begin(ctx context.Context) (userCode, verificationURI string, err error) {
	return a.m.Begin(ctx)
}

func (a cloudEnrollAdapter) Status() auth.CloudEnrollStatus {
	s := a.m.Status()
	return auth.CloudEnrollStatus{
		State:           s.State,
		UserCode:        s.UserCode,
		VerificationURI: s.VerificationURI,
		ULID:            s.ULID,
		Error:           s.Error,
	}
}

// compile-time assertion
var _ auth.CloudEnrollment = cloudEnrollAdapter{}
