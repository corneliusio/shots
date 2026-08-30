package har

import (
	"encoding/base64"
	"testing"
	"time"
)

// timings is a plausible set of observed phases: 1ms of DNS, 2ms connecting,
// 1ms sending and 6ms waiting for the first header byte.
func timings() Timings {
	t := UnknownTimings()
	t.DNS, t.Connect, t.Send, t.Wait = 1, 2, 1, 6
	return t
}

// record plays one page's worth of events: a redirected document with a body,
// a request that failed, and a binary response.
func record(start time.Time) Log {
	r := NewRecorder("test")
	r.Sent("1", start, Request{Method: "GET", URL: "http://example.com/a?x=1&y=two+words"})
	r.Redirect("1", start.Add(10*time.Millisecond), Response{Status: 302, RedirectURL: "/b"}, timings())
	r.Sent("1", start.Add(10*time.Millisecond), Request{Method: "GET", URL: "http://example.com/b"})
	r.Received("1", Response{Status: 200, HTTPVersion: "http/1.1", Content: Content{MimeType: "text/html"}}, timings())
	r.Finished("1", start.Add(40*time.Millisecond), 512, []byte("<h1>hi</h1>"))

	r.Sent("2", start, Request{Method: "GET", URL: "http://example.com/gone"})
	r.Failed("2", start.Add(5*time.Millisecond), "net::ERR_CONNECTION_RESET")

	r.Sent("3", start, Request{Method: "GET", URL: "http://example.com/logo.png"})
	r.Received("3", Response{Status: 200, Content: Content{MimeType: "image/png"}}, timings())
	r.Finished("3", start.Add(20*time.Millisecond), 8, []byte{0x89, 0x50, 0xff, 0xfe})

	// Events for a request nobody opened must not panic or invent an entry.
	r.Finished("stale", start, 1, []byte("x"))
	r.Failed("stale", start, "gone")
	r.Received("stale", Response{Status: 200}, timings())
	return r.Log()
}

func TestRecorderCorrelatesEvents(t *testing.T) {
	log := record(time.Unix(1700000000, 0).UTC())
	if log.Version != Version || log.Creator.Name != "shots" || log.Creator.Version != "test" {
		t.Errorf("log header = %+v %+v", log.Version, log.Creator)
	}
	if len(log.Entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(log.Entries))
	}
	urls := []string{"http://example.com/a?x=1&y=two+words", "http://example.com/b", "http://example.com/gone", "http://example.com/logo.png"}
	for i, want := range urls {
		if got := log.Entries[i].Request.URL; got != want {
			t.Errorf("entry %d URL = %q, want %q", i, got, want)
		}
	}

	redirect := log.Entries[0]
	if redirect.Response.Status != 302 || redirect.Response.RedirectURL != "/b" {
		t.Errorf("redirect entry = %+v", redirect.Response)
	}
	if redirect.Time <= 0 {
		t.Errorf("redirect entry time = %v, want a duration", redirect.Time)
	}

	document := log.Entries[1]
	if document.Response.Content.Text != "<h1>hi</h1>" || document.Response.Content.Encoding != "" {
		t.Errorf("document content = %+v", document.Response.Content)
	}
	if document.Response.Content.Size != 11 || document.Response.BodySize != 512 {
		t.Errorf("document sizes = %d and %d", document.Response.Content.Size, document.Response.BodySize)
	}
	// The version is only known once the response names the protocol.
	if document.Request.HTTPVersion != "http/1.1" {
		t.Errorf("request httpVersion = %q", document.Request.HTTPVersion)
	}

	failed := log.Entries[2]
	if failed.FailureText != "net::ERR_CONNECTION_RESET" || failed.Response.Status != 0 {
		t.Errorf("failed entry = %+v, %+v", failed.FailureText, failed.Response)
	}

	binary := log.Entries[3]
	if binary.Response.Content.Encoding != "base64" {
		t.Fatalf("binary content encoding = %q, want base64", binary.Response.Content.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(binary.Response.Content.Text)
	if err != nil || string(decoded) != "\x89P\xff\xfe" {
		t.Errorf("binary content = %q (%v)", decoded, err)
	}
}

// HAR 1.2 requires an entry's time to be the sum of its non-negative timings.
func TestEntryTimeIsTheSumOfItsTimings(t *testing.T) {
	for i, entry := range record(time.Unix(1700000000, 0).UTC()).Entries {
		var want float64
		for _, phase := range []float64{entry.Timings.Blocked, entry.Timings.DNS, entry.Timings.Connect, entry.Timings.Send, entry.Timings.Wait, entry.Timings.Receive} {
			if phase > 0 {
				want += phase
			}
		}
		if entry.Time != want {
			t.Errorf("entry %d time = %v, want %v (timings %+v)", i, entry.Time, want, entry.Timings)
		}
		if entry.Timings.Receive < 0 {
			t.Errorf("entry %d receive = %v, want the remaining time", i, entry.Timings.Receive)
		}
	}
}

func TestQueryString(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want []NameValue
	}{
		{"no query", "http://example.com/a", nil},
		{"empty query", "http://example.com/a?", nil},
		{"pairs keep their order", "http://example.com/a?b=2&a=1", []NameValue{{"b", "2"}, {"a", "1"}}},
		{"values are unescaped", "http://example.com/a?q=two+words%21", []NameValue{{"q", "two words!"}}},
		{"a bare name has an empty value", "http://example.com/a?flag", []NameValue{{"flag", ""}}},
		{"the fragment is not part of the query", "http://example.com/a?x=1#y=2", []NameValue{{"x", "1"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := queryString(tt.url)
			if len(got) != len(tt.want) {
				t.Fatalf("queryString(%q) = %+v, want %+v", tt.url, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("queryString(%q)[%d] = %+v, want %+v", tt.url, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// A HAR document must never contain a null where an array belongs.
func TestListsAreNeverNull(t *testing.T) {
	log := record(time.Unix(1700000000, 0).UTC())
	for i, entry := range log.Entries {
		if entry.Request.Headers == nil || entry.Request.Cookies == nil || entry.Request.QueryString == nil {
			t.Errorf("entry %d request has a nil list: %+v", i, entry.Request)
		}
		if entry.Response.Headers == nil || entry.Response.Cookies == nil {
			t.Errorf("entry %d response has a nil list: %+v", i, entry.Response)
		}
	}
}
