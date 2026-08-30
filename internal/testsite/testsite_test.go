package testsite_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mostlyserious/shots/internal/testsite"
)

func server(t *testing.T) (*http.Client, string) {
	t.Helper()
	srv := testsite.New()
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client, srv.URL
}

func get(t *testing.T, client *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp, string(body)
}

func TestPages(t *testing.T) {
	client, base := server(t)
	pages := []struct {
		path string
		want string
	}{
		{"/", `id="title"`},
		{"/redirected.html", `id="redirected"`},
		{"/delayed.html", `id="immediate"`},
		{"/console.html", "console.warn"},
		{"/busy.html", "setInterval"},
		{"/promise.html", "shotsPromise"},
		{"/jserror.html", "uncaught on purpose"},
		{"/transparent.html", "background: transparent"},
		{"/geometry.html", `id="box-a"`},
		{"/storage.html", "localStorage"},
		{"/accessibility.html", `aria-pressed="true"`},
		{"/print.html", "@media print"},
	}
	for _, p := range pages {
		resp, body := get(t, client, base+p.path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p.path, resp.StatusCode)
		}
		if !strings.Contains(body, p.want) {
			t.Errorf("GET %s does not contain %q", p.path, p.want)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", p.path, got)
		}
	}
}

func TestIndexIsCanonical(t *testing.T) {
	client, base := server(t)
	resp, _ := get(t, client, base+"/index.html")
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "./" {
		t.Errorf("GET /index.html = %d %q, want a 301 to ./", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestMissingPage(t *testing.T) {
	client, base := server(t)
	resp, _ := get(t, client, base+"/nope.html")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRedirect(t *testing.T) {
	client, base := server(t)

	resp, _ := get(t, client, base+"/redirect")
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/redirected.html" {
		t.Errorf("location = %q, want /redirected.html", got)
	}

	resp, _ = get(t, client, base+"/redirect?to=/index.html&status=301")
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/index.html" {
		t.Errorf("location = %q, want /index.html", got)
	}

	for _, bad := range []string{"?to=https://example.com/", "?status=200", "?status=abc"} {
		resp, _ = get(t, client, base+"/redirect"+bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /redirect%s = %d, want 400", bad, resp.StatusCode)
		}
	}
}

func TestRedirectFollowed(t *testing.T) {
	srv := testsite.New()
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="redirected"`) {
		t.Errorf("followed redirect = %d %q", resp.StatusCode, body)
	}
}

func TestStatus(t *testing.T) {
	client, base := server(t)
	for _, code := range []int{404, 500, 503} {
		resp, body := get(t, client, base+"/status/"+strconv.Itoa(code))
		if resp.StatusCode != code {
			t.Errorf("status = %d, want %d", resp.StatusCode, code)
		}
		if !strings.Contains(body, `id=status`) {
			t.Errorf("body = %q", body)
		}
	}
	for _, bad := range []string{"/status/99", "/status/oops"} {
		resp, _ := get(t, client, base+bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", bad, resp.StatusCode)
		}
	}
}

func TestSlow(t *testing.T) {
	client, base := server(t)

	start := time.Now()
	resp, body := get(t, client, base+"/slow?ms=150")
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("responded after %v, want at least 150ms", elapsed)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "150") {
		t.Errorf("status = %d, body = %q", resp.StatusCode, body)
	}

	resp, _ = get(t, client, base+"/slow?ms=-1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestBasicAuth(t *testing.T) {
	client, base := server(t)

	resp, _ := get(t, client, base+"/basic-auth")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("WWW-Authenticate = %q", got)
	}

	req, err := http.NewRequest(http.MethodGet, base+"/basic-auth", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testsite.BasicAuthUser, testsite.BasicAuthPassword)
	authed, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer authed.Body.Close()
	body, _ := io.ReadAll(authed.Body)
	if authed.StatusCode != http.StatusOK || !strings.Contains(string(body), testsite.BasicAuthUser) {
		t.Errorf("authenticated request = %d %q", authed.StatusCode, body)
	}
}

func TestSetCookie(t *testing.T) {
	client, base := server(t)
	resp, _ := get(t, client, base+"/set-cookie")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/storage.html" {
		t.Errorf("location = %q, want /storage.html", got)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "shots_session" || cookies[0].Value != "granted" {
		t.Fatalf("cookies = %v", cookies)
	}

	resp, _ = get(t, client, base+"/set-cookie?name=token&value=abc")
	if cookies := resp.Cookies(); len(cookies) != 1 || cookies[0].Name != "token" || cookies[0].Value != "abc" {
		t.Errorf("cookies = %v", cookies)
	}
}

func TestReset(t *testing.T) {
	client, base := server(t)
	if _, err := client.Get(base + "/reset"); err == nil {
		t.Error("expected a network failure")
	}
}
