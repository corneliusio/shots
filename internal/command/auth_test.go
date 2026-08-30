package command

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mostlyserious/shots/internal/browser"
)

// headless keeps the sign-in browser off the screen during tests. shots runs
// it headed, and a later --browser-arg wins over that.
var headless = []string{"--browser-arg", "headless=new"}

// authenticate signs in to target and returns the state file it wrote.
func authenticate(t *testing.T, target string) (string, result) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "auth.json")
	args := append([]string{"auth", target, state}, headless...)
	// The Enter the command waits for.
	got := runStdin(t, "\n", args...)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	return state, got
}

func readState(t *testing.T, path string) browser.State {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state browser.State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return state
}

func TestAuthExportsCookiesAndStorage(t *testing.T) {
	srv := site(t)
	path, got := authenticate(t, srv.URL+"/storage.html?set=hello&cookie=granted")
	state := readState(t, path)

	if len(state.Cookies) != 1 {
		t.Fatalf("exported %d cookies, want 1: %+v", len(state.Cookies), state.Cookies)
	}
	if cookie := state.Cookies[0]; cookie.Name != "shots_session" || cookie.Value != "granted" || cookie.Path != "/" {
		t.Errorf("cookie = %+v", cookie)
	}
	if len(state.Origins) != 1 || state.Origins[0].Origin != srv.URL {
		t.Fatalf("exported origins = %+v, want %s", state.Origins, srv.URL)
	}
	if items := state.Origins[0].LocalStorage; len(items) != 1 || items[0] != (browser.Item{Name: "shots", Value: "hello"}) {
		t.Errorf("local storage = %+v", items)
	}

	// PLAN.md Section 11: credentials never reach a log.
	if strings.Contains(got.stderr, "granted") || strings.Contains(got.stderr, "hello") {
		t.Errorf("stderr leaks a credential:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "Hit <enter>") {
		t.Errorf("stderr does not prompt for Enter:\n%s", got.stderr)
	}
}

// A sign-in usually passes through an identity provider that leaves a token in
// its own storage before handing over to the application, so the export covers
// every origin the page visited, not just the one it ended on.
func TestAuthExportsEveryVisitedOrigin(t *testing.T) {
	identity, app := site(t), site(t)
	start := identity.URL + "/storage.html?set=token&then=" + url.QueryEscape(app.URL+"/storage.html?set=session")
	path, _ := authenticate(t, start)
	state := readState(t, path)

	want := map[string]string{identity.URL: "token", app.URL: "session"}
	if len(state.Origins) != len(want) {
		t.Fatalf("exported origins = %+v, want %v", state.Origins, want)
	}
	for _, origin := range state.Origins {
		items := origin.LocalStorage
		if len(items) != 1 || items[0] != (browser.Item{Name: "shots", Value: want[origin.Origin]}) {
			t.Errorf("%s local storage = %+v, want %q", origin.Origin, items, want[origin.Origin])
		}
	}
}

func TestAuthFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	srv := site(t)
	path, _ := authenticate(t, srv.URL+"/storage.html?set=hello")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != stateMode {
		t.Errorf("state file mode = %04o, want %04o", mode, stateMode)
	}
}

// The state a sign-in produced restores into a later command, which is the
// whole point of the file.
func TestAuthStateRestores(t *testing.T) {
	srv := site(t)
	path, _ := authenticate(t, srv.URL+"/storage.html?set=hello&cookie=granted")

	got := run(t, "javascript", srv.URL+"/storage.html", "localStorage.getItem('shots')", "-a", path)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	if strings.TrimSpace(string(got.stdout)) != `"hello"` {
		t.Errorf("restored local storage = %s", got.stdout)
	}

	got = run(t, "javascript", srv.URL+"/storage.html", "document.cookie", "-a", path)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	if !strings.Contains(string(got.stdout), "shots_session=granted") {
		t.Errorf("restored cookies = %s", got.stdout)
	}
}

// PLAN.md Section 6 restores local storage by opening a page per recorded
// origin, so a state file covering several origins restores all of them —
// including origins the command never navigates to.
func TestAuthStateRestoresEveryOrigin(t *testing.T) {
	first, second := site(t), site(t)
	state := browser.State{
		Cookies: []browser.Cookie{{Name: "shots_session", Value: "granted", Domain: "127.0.0.1", Path: "/", Expires: -1}},
		Origins: []browser.Origin{
			{Origin: first.URL, LocalStorage: []browser.Item{{Name: "shots", Value: "one"}}},
			{Origin: second.URL, LocalStorage: []browser.Item{{Name: "shots", Value: "two"}}},
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		url  string
		want string
	}{
		{first.URL, `"one"`},
		{second.URL, `"two"`},
	} {
		got := run(t, "javascript", tt.url+"/storage.html", "localStorage.getItem('shots')", "-a", path)
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if strings.TrimSpace(string(got.stdout)) != tt.want {
			t.Errorf("%s local storage = %s, want %s", tt.url, got.stdout, tt.want)
		}
	}
}

func TestAuthMissingStateFile(t *testing.T) {
	got := run(t, "shot", "example.com", "-a", filepath.Join(t.TempDir(), "absent.json"))
	if got.code != exitFailure || !strings.Contains(got.stderr, "absent.json") {
		t.Errorf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
}
