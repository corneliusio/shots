package browser

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// testdata/storage_state.json is in the shape Playwright's storage_state and
// shot-scraper auth produce. Decoding it and encoding it again has to give the
// same bytes back, or a state file would lose credentials as it passed
// through shots.
func TestStateRoundTripsPlaywrightFormat(t *testing.T) {
	want, err := os.ReadFile("testdata/storage_state.json")
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(want, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 2 || len(state.Origins) != 2 {
		t.Fatalf("decoded %d cookies and %d origins", len(state.Cookies), len(state.Origins))
	}
	session := state.Cookies[0]
	if session.Name != "session" || session.Value != "s3cr3t" || !session.HTTPOnly || !session.Secure || session.SameSite != "Lax" {
		t.Errorf("first cookie = %+v", session)
	}
	if state.Cookies[1].Expires != -1 {
		t.Errorf("session cookie expiry = %v, want -1", state.Cookies[1].Expires)
	}
	if origin := state.Origins[1]; origin.Origin != "https://other.example.com" || origin.LocalStorage[0] != (Item{Name: "seen", Value: "true"}) {
		t.Errorf("second origin = %+v", origin)
	}

	got, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(append(got, '\n')) != string(want) {
		t.Errorf("re-encoded state differs:\n%s", got)
	}
}

// Playwright grows fields; a state file from a newer one still restores what
// shots understands.
func TestStateIgnoresUnknownFields(t *testing.T) {
	const file = `{"cookies":[{"name":"a","value":"b","domain":"x","path":"/","expires":-1,"httpOnly":false,"secure":false,"partitionKey":"y"}],
		"origins":[{"origin":"https://x","localStorage":[{"name":"k","value":"v"}],"sessionStorage":[{"name":"s","value":"t"}]}]}`
	var state State
	if err := json.Unmarshal([]byte(file), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 1 || len(state.Origins) != 1 || state.Origins[0].LocalStorage[0].Value != "v" {
		t.Errorf("state = %+v", state)
	}
}

func TestRestoreCookies(t *testing.T) {
	params := restoreCookies([]Cookie{
		{Name: "session", Value: "s", Domain: "example.com", Path: "/", Expires: 1767225600, HTTPOnly: true, Secure: true, SameSite: "Lax"},
		{Name: "temporary", Value: "t", Domain: "example.com", Path: "/", Expires: -1},
	})
	if len(params) != 2 {
		t.Fatalf("got %d cookies", len(params))
	}
	if params[0].Expires == nil || !time.Time(*params[0].Expires).Equal(time.Unix(1767225600, 0)) {
		t.Errorf("expiry = %v, want 1767225600", params[0].Expires)
	}
	if string(params[0].SameSite) != "Lax" || !params[0].HTTPOnly || !params[0].Secure {
		t.Errorf("cookie = %+v", params[0])
	}
	// A session cookie has no expiry at all, rather than one in the past.
	if params[1].Expires != nil {
		t.Errorf("session cookie expiry = %v, want none", params[1].Expires)
	}
	if params[1].SameSite != "" {
		t.Errorf("sameSite = %q, want it left unset", params[1].SameSite)
	}
}
