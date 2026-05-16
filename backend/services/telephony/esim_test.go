// Tests for esim.go — eSIM profile management via lpac.
//
// All tests use a mock ESIMLpacClient so no lpac binary or eUICC hardware is
// required.  Naming convention: TestESIM<Subject> to avoid any collision with
// identifiers in the existing telephony test files.
package telephony

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ----------------------------------------------------------------------------
// mockESIMLpacClient — test double for ESIMLpacClient
// ----------------------------------------------------------------------------

// mockESIMLpacClient is a configurable fake used by all handler tests.
type mockESIMLpacClient struct {
	available  bool
	profiles   []ESIMProfile
	listErr    error
	enableErr  error
	disableErr error
	deleteErr  error
	addErr     error

	// Recorded calls for assertion.
	enabledICCID  string
	disabledICCID string
	deletedICCID  string
	addedCode     string
}

func (m *mockESIMLpacClient) ESIMAvailable() bool { return m.available }

func (m *mockESIMLpacClient) ESIMListProfiles() ([]ESIMProfile, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.profiles, nil
}

func (m *mockESIMLpacClient) ESIMEnableProfile(iccid string) error {
	m.enabledICCID = iccid
	return m.enableErr
}

func (m *mockESIMLpacClient) ESIMDisableProfile(iccid string) error {
	m.disabledICCID = iccid
	return m.disableErr
}

func (m *mockESIMLpacClient) ESIMDeleteProfile(iccid string) error {
	m.deletedICCID = iccid
	return m.deleteErr
}

func (m *mockESIMLpacClient) ESIMAddProfile(code string) error {
	m.addedCode = code
	return m.addErr
}

// ----------------------------------------------------------------------------
// Helper — newESIMTestServer builds a mux wired with the mock client.
// ----------------------------------------------------------------------------

func newESIMTestServer(mock *mockESIMLpacClient) *httptest.Server {
	mux := http.NewServeMux()
	ec := esimNewControllerWith(mock)
	RegisterESIMHandlers(mux, ec)
	return httptest.NewServer(mux)
}

// ----------------------------------------------------------------------------
// TestESIMListProfiles
// ----------------------------------------------------------------------------

func TestESIMListProfilesOK(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{
		available: true,
		profiles: []ESIMProfile{
			{ICCID: "89001012012341234512", Name: "Carrier A", State: "enabled", ProviderName: "Carrier A"},
			{ICCID: "89001012012341234513", Name: "Carrier B", State: "disabled", ProviderName: "Carrier B"},
		},
	}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/telephony/esim/profiles")
	if err != nil {
		t.Fatalf("GET profiles: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var profiles []ESIMProfile
	if err := json.NewDecoder(resp.Body).Decode(&profiles); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("want 2 profiles, got %d", len(profiles))
	}
	if profiles[0].ICCID != "89001012012341234512" {
		t.Errorf("profile[0].ICCID = %q, want 89001012012341234512", profiles[0].ICCID)
	}
	if profiles[1].State != "disabled" {
		t.Errorf("profile[1].State = %q, want disabled", profiles[1].State)
	}
}

func TestESIMListProfilesEmpty(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true, profiles: nil}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/telephony/esim/profiles")
	if err != nil {
		t.Fatalf("GET profiles: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var profiles []ESIMProfile
	if err := json.NewDecoder(resp.Body).Decode(&profiles); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Must be an empty slice, not null.
	if profiles == nil {
		t.Fatal("profiles must be a non-null array")
	}
	if len(profiles) != 0 {
		t.Fatalf("want 0 profiles, got %d", len(profiles))
	}
}

func TestESIMListProfilesMethodNotAllowed(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles", "application/json", nil)
	if err != nil {
		t.Fatalf("POST profiles: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}

func TestESIMListProfilesLpacAbsent(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: false}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/telephony/esim/profiles")
	if err != nil {
		t.Fatalf("GET profiles: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
	esimAssertErrorBody(t, resp)
}

func TestESIMListProfilesBackendError(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{
		available: true,
		listErr:   errors.New("lpac internal error"),
	}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/telephony/esim/profiles")
	if err != nil {
		t.Fatalf("GET profiles: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
	esimAssertErrorBody(t, resp)
}

// ----------------------------------------------------------------------------
// TestESIMEnableProfile
// ----------------------------------------------------------------------------

func TestESIMEnableProfileOK(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/enable", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST enable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mock.enabledICCID != "89001012012341234512" {
		t.Errorf("enabledICCID = %q, want 89001012012341234512", mock.enabledICCID)
	}
	var result esimOKResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.OK {
		t.Error("ok should be true")
	}
}

func TestESIMEnableProfileMethodNotAllowed(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/telephony/esim/profiles/enable", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET enable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}

func TestESIMEnableProfileMissingICCID(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/enable", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST enable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	esimAssertErrorBody(t, resp)
}

func TestESIMEnableProfileLpacAbsent(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: false}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/enable", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST enable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestESIMEnableProfileBackendError(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true, enableErr: errors.New("profile locked")}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/enable", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST enable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestESIMEnableProfileBadJSON(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/enable", "application/json", strings.NewReader(`not-json`))
	if err != nil {
		t.Fatalf("POST enable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// TestESIMDisableProfile
// ----------------------------------------------------------------------------

func TestESIMDisableProfileOK(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/disable", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST disable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mock.disabledICCID != "89001012012341234512" {
		t.Errorf("disabledICCID = %q, want 89001012012341234512", mock.disabledICCID)
	}
}

func TestESIMDisableProfileMethodNotAllowed(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/telephony/esim/profiles/disable", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET disable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}

func TestESIMDisableProfileMissingICCID(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/disable", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST disable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestESIMDisableProfileLpacAbsent(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: false}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/disable", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST disable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestESIMDisableProfileBackendError(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true, disableErr: errors.New("eUICC error")}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/disable", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST disable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// TestESIMDeleteProfile
// ----------------------------------------------------------------------------

func TestESIMDeleteProfileOK(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/delete", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mock.deletedICCID != "89001012012341234512" {
		t.Errorf("deletedICCID = %q, want 89001012012341234512", mock.deletedICCID)
	}
}

func TestESIMDeleteProfileMethodNotAllowed(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/telephony/esim/profiles/delete", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}

func TestESIMDeleteProfileMissingICCID(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/delete", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestESIMDeleteProfileLpacAbsent(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: false}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/delete", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestESIMDeleteProfileBackendError(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true, deleteErr: errors.New("profile not found")}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"iccid":"89001012012341234512"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/delete", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// TestESIMAddProfile
// ----------------------------------------------------------------------------

func TestESIMAddProfileOK(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	code := "LPA:1$example.smdp.io$MATCHINGID123"
	body, _ := json.Marshal(esimAddRequest{ActivationCode: code})
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/add", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mock.addedCode != code {
		t.Errorf("addedCode = %q, want %q", mock.addedCode, code)
	}
}

func TestESIMAddProfileMethodNotAllowed(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/telephony/esim/profiles/add", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}

func TestESIMAddProfileMissingActivationCode(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/add", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	esimAssertErrorBody(t, resp)
}

func TestESIMAddProfileLpacAbsent(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: false}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"activation_code":"LPA:1$example.smdp.io$MATCHINGID"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/add", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
	esimAssertErrorBody(t, resp)
}

func TestESIMAddProfileBackendError(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true, addErr: errors.New("SM-DP+ unreachable")}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	body := `{"activation_code":"LPA:1$example.smdp.io$MATCHINGID"}`
	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/add", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestESIMAddProfileBadJSON(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true}
	srv := newESIMTestServer(mock)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/telephony/esim/profiles/add", "application/json", strings.NewReader(`{bad`))
	if err != nil {
		t.Fatalf("POST add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// TestESIMParseProfileList — unit tests for the lpac output parser
// ----------------------------------------------------------------------------

func TestESIMParseProfileListBareArray(t *testing.T) {
	t.Parallel()
	out := `[
		{"iccid":"89001012012341234512","profileName":"Carrier A","profileState":"enabled","serviceProviderName":"Carrier A"},
		{"iccid":"89001012012341234513","profileName":"Carrier B","profileState":"disabled","serviceProviderName":"Carrier B"}
	]`
	profiles := esimParseProfileList(out)
	if len(profiles) != 2 {
		t.Fatalf("want 2, got %d", len(profiles))
	}
	if profiles[0].ICCID != "89001012012341234512" {
		t.Errorf("ICCID[0] = %q", profiles[0].ICCID)
	}
	if profiles[0].State != "enabled" {
		t.Errorf("State[0] = %q, want enabled", profiles[0].State)
	}
	if profiles[1].State != "disabled" {
		t.Errorf("State[1] = %q, want disabled", profiles[1].State)
	}
}

func TestESIMParseProfileListEnvelope(t *testing.T) {
	t.Parallel()
	out := `{"code":0,"message":"success","payload":[
		{"iccid":"89001012012341234512","profileName":"Test","profileState":"enabled","serviceProviderName":"TestCo"}
	]}`
	profiles := esimParseProfileList(out)
	if len(profiles) != 1 {
		t.Fatalf("want 1, got %d", len(profiles))
	}
	if profiles[0].ICCID != "89001012012341234512" {
		t.Errorf("ICCID = %q", profiles[0].ICCID)
	}
}

func TestESIMParseProfileListEmpty(t *testing.T) {
	t.Parallel()
	profiles := esimParseProfileList("")
	if len(profiles) != 0 {
		t.Fatalf("want 0, got %d", len(profiles))
	}
}

func TestESIMParseProfileListEmptyArray(t *testing.T) {
	t.Parallel()
	profiles := esimParseProfileList(`[]`)
	if len(profiles) != 0 {
		t.Fatalf("want 0, got %d", len(profiles))
	}
}

func TestESIMParseProfileListNickname(t *testing.T) {
	t.Parallel()
	out := `[{"iccid":"89001012012341234512","profileName":"P","nickname":"My SIM","profileState":"disabled","serviceProviderName":"Net"}]`
	profiles := esimParseProfileList(out)
	if len(profiles) != 1 {
		t.Fatalf("want 1, got %d", len(profiles))
	}
	if profiles[0].Nickname != "My SIM" {
		t.Errorf("Nickname = %q, want My SIM", profiles[0].Nickname)
	}
}

// ----------------------------------------------------------------------------
// TestESIMNormaliseProfileState
// ----------------------------------------------------------------------------

func TestESIMNormaliseProfileState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"enabled", "enabled"},
		{"Enabled", "enabled"},
		{"ENABLED", "enabled"},
		{"active", "enabled"},
		{"Active", "enabled"},
		{"1", "enabled"},
		{"disabled", "disabled"},
		{"Disabled", "disabled"},
		{"inactive", "disabled"},
		{"0", "disabled"},
		{"", "disabled"},
	}
	for _, tc := range cases {
		got := esimNormaliseProfileState(tc.in)
		if got != tc.want {
			t.Errorf("esimNormaliseProfileState(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ----------------------------------------------------------------------------
// TestESIMLpacClientAvailability — unit test for the real CLI client
// ----------------------------------------------------------------------------

func TestESIMLpacClientUnavailableListProfiles(t *testing.T) {
	t.Parallel()
	// Create a client that is marked unavailable (simulates lpac not installed).
	c := &esimLpacCLI{avail: false}
	if c.ESIMAvailable() {
		t.Fatal("should be unavailable")
	}
	_, err := c.ESIMListProfiles()
	if err == nil {
		t.Fatal("expected error when unavailable")
	}
	if !strings.Contains(err.Error(), "lpac not installed") {
		t.Errorf("error should mention 'lpac not installed', got: %v", err)
	}
}

func TestESIMLpacClientUnavailableEnable(t *testing.T) {
	t.Parallel()
	c := &esimLpacCLI{avail: false}
	err := c.ESIMEnableProfile("89001012012341234512")
	if err == nil {
		t.Fatal("expected error when unavailable")
	}
}

func TestESIMLpacClientUnavailableDisable(t *testing.T) {
	t.Parallel()
	c := &esimLpacCLI{avail: false}
	err := c.ESIMDisableProfile("89001012012341234512")
	if err == nil {
		t.Fatal("expected error when unavailable")
	}
}

func TestESIMLpacClientUnavailableDelete(t *testing.T) {
	t.Parallel()
	c := &esimLpacCLI{avail: false}
	err := c.ESIMDeleteProfile("89001012012341234512")
	if err == nil {
		t.Fatal("expected error when unavailable")
	}
}

func TestESIMLpacClientUnavailableAdd(t *testing.T) {
	t.Parallel()
	c := &esimLpacCLI{avail: false}
	err := c.ESIMAddProfile("LPA:1$example.smdp.io$ID")
	if err == nil {
		t.Fatal("expected error when unavailable")
	}
}

func TestESIMLpacClientEnableEmptyICCID(t *testing.T) {
	t.Parallel()
	c := &esimLpacCLI{avail: true}
	err := c.ESIMEnableProfile("")
	if err == nil {
		t.Fatal("expected error for empty iccid")
	}
}

func TestESIMLpacClientDisableEmptyICCID(t *testing.T) {
	t.Parallel()
	c := &esimLpacCLI{avail: true}
	err := c.ESIMDisableProfile("")
	if err == nil {
		t.Fatal("expected error for empty iccid")
	}
}

func TestESIMLpacClientDeleteEmptyICCID(t *testing.T) {
	t.Parallel()
	c := &esimLpacCLI{avail: true}
	err := c.ESIMDeleteProfile("")
	if err == nil {
		t.Fatal("expected error for empty iccid")
	}
}

func TestESIMLpacClientAddEmptyCode(t *testing.T) {
	t.Parallel()
	c := &esimLpacCLI{avail: true}
	err := c.ESIMAddProfile("")
	if err == nil {
		t.Fatal("expected error for empty activation_code")
	}
}

// ----------------------------------------------------------------------------
// TestESIMRegisterHandlers — verify all routes are registered
// ----------------------------------------------------------------------------

func TestESIMRegisterHandlers(t *testing.T) {
	t.Parallel()
	mock := &mockESIMLpacClient{available: true, profiles: []ESIMProfile{}}
	mux := http.NewServeMux()
	ec := esimNewControllerWith(mock)
	RegisterESIMHandlers(mux, ec)

	routes := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/telephony/esim/profiles", http.StatusOK},
		{http.MethodPost, "/api/telephony/esim/profiles/enable", http.StatusBadRequest}, // missing body → 400 from json decode
		{http.MethodPost, "/api/telephony/esim/profiles/disable", http.StatusBadRequest},
		{http.MethodPost, "/api/telephony/esim/profiles/delete", http.StatusBadRequest},
		{http.MethodPost, "/api/telephony/esim/profiles/add", http.StatusBadRequest},
	}

	for _, tc := range routes {
		var bodyReader *bytes.Reader
		if tc.method == http.MethodPost {
			bodyReader = bytes.NewReader(nil)
		}
		var req *http.Request
		if bodyReader != nil {
			req, _ = http.NewRequest(tc.method, tc.path, bodyReader)
		} else {
			req, _ = http.NewRequest(tc.method, tc.path, nil)
		}
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != tc.want {
			t.Errorf("%s %s: want %d, got %d", tc.method, tc.path, tc.want, rr.Code)
		}
	}
}

// ----------------------------------------------------------------------------
// TestESIMErrLpacAbsent — message content check
// ----------------------------------------------------------------------------

func TestESIMErrLpacAbsent(t *testing.T) {
	t.Parallel()
	err := esimErrLpacAbsent()
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "lpac not installed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Helper — esimAssertErrorBody checks the response body contains {"error":...}
// ----------------------------------------------------------------------------

func esimAssertErrorBody(t *testing.T, resp *http.Response) {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("response body missing 'error' field: %v", body)
	}
}
