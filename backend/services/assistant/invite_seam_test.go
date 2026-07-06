package assistant

import (
	"encoding/json"
	"testing"
)

// TestMessageInviteWireSeam guards the assistant↔lilmail invite seam: the
// MessageInvite JSON tags MUST match lilmail's models.CalendarInvite wire shape
// (`myPartStat`, `allDay`). A drift here silently makes every REQUEST look
// unanswered (RSVP never populates → AwaitsRSVP always true).
func TestMessageInviteWireSeam(t *testing.T) {
	// Exact field names lilmail emits on GET /v1/messages/:uid → email.invite.
	const wire = `{"method":"REQUEST","uid":"evt-1","summary":"Standup",` +
		`"start":"2026-07-06T15:00:00Z","end":"2026-07-06T15:30:00Z",` +
		`"location":"Room 1","allDay":false,"organizer":"boss@x.test",` +
		`"myPartStat":"ACCEPTED"}`
	var iv MessageInvite
	if err := json.Unmarshal([]byte(wire), &iv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if iv.RSVP != "ACCEPTED" {
		t.Fatalf("RSVP (myPartStat) = %q, want ACCEPTED — JSON tag mismatch with lilmail", iv.RSVP)
	}
	if iv.AwaitsRSVP() {
		t.Fatal("AwaitsRSVP=true for an ACCEPTED invite — over-reporting pending (seam bug)")
	}
	if iv.Organizer != "boss@x.test" || iv.UID != "evt-1" {
		t.Fatalf("organizer/uid did not populate: %+v", iv)
	}
	// A genuinely-pending invite (raw uppercase from iCal) must still be caught.
	var pending MessageInvite
	_ = json.Unmarshal([]byte(`{"method":"REQUEST","myPartStat":"NEEDS-ACTION"}`), &pending)
	if !pending.AwaitsRSVP() {
		t.Fatal("AwaitsRSVP=false for NEEDS-ACTION — case/tag handling broken")
	}
}
