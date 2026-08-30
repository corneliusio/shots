// Package har records network traffic as an HTTP Archive (HAR) 1.2 log.
//
// A [Recorder] correlates the events a browser reports for one request — sent,
// redirected, answered, finished, failed — into one entry. Nothing here knows
// about the DevTools Protocol: the browser package translates its events into
// these calls, which keeps the archive format testable without a browser.
package har

import (
	"encoding/base64"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Version is the HAR specification this package writes.
const Version = "1.2"

// unknown is the value HAR 1.2 reserves for a size or a duration the recorder
// could not observe.
const unknown = -1

// Log is a HAR log: the creator, and one entry per request.
type Log struct {
	Version string   `json:"version"`
	Creator Creator  `json:"creator"`
	Entries []*Entry `json:"entries"`
}

// Creator names the program that produced a log.
type Creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// NameValue is a header, a query-string parameter or a cookie. HAR cookies
// carry optional attributes beyond these two; shots does not fill them in, so
// the type is shared.
type NameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Entry is one request and its response.
type Entry struct {
	StartedDateTime time.Time `json:"startedDateTime"`
	// Time is the whole exchange in milliseconds, and by specification the sum
	// of the non-negative Timings.
	Time            float64  `json:"time"`
	Request         Request  `json:"request"`
	Response        Response `json:"response"`
	Cache           Cache    `json:"cache"`
	Timings         Timings  `json:"timings"`
	ServerIPAddress string   `json:"serverIPAddress,omitempty"`
	// FailureText is the browser's error for a request that never completed.
	// HAR has no field for one; a leading underscore is the specification's
	// own extension mechanism.
	FailureText string `json:"_failureText,omitempty"`
}

// Cache is always empty: a recorder reports what crossed the network, and
// Chrome does not say what it took from its cache instead.
type Cache struct{}

// Request is one HTTP request.
type Request struct {
	Method      string      `json:"method"`
	URL         string      `json:"url"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []NameValue `json:"cookies"`
	Headers     []NameValue `json:"headers"`
	QueryString []NameValue `json:"queryString"`
	PostData    *PostData   `json:"postData,omitempty"`
	HeadersSize int64       `json:"headersSize"`
	BodySize    int64       `json:"bodySize"`
}

// PostData is a request body.
type PostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

// Response is one HTTP response. Status is zero for a request that failed
// before any response arrived.
type Response struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []NameValue `json:"cookies"`
	Headers     []NameValue `json:"headers"`
	Content     Content     `json:"content"`
	RedirectURL string      `json:"redirectURL"`
	HeadersSize int64       `json:"headersSize"`
	BodySize    int64       `json:"bodySize"`
}

// Content is a response body. Text is empty for the many responses whose body
// the browser can no longer produce; Size still reports what is known.
type Content struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	// Encoding is "base64" when Text is not the body itself.
	Encoding string `json:"encoding,omitempty"`
}

// Timings are the phases of an exchange, in milliseconds. Send, Wait and
// Receive are required by the specification; -1 means the phase was not
// observed.
type Timings struct {
	Blocked float64 `json:"blocked"`
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	SSL     float64 `json:"ssl"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

// UnknownTimings is the value for an exchange whose phases were not observed.
func UnknownTimings() Timings {
	return Timings{Blocked: unknown, DNS: unknown, Connect: unknown, SSL: unknown, Send: unknown, Wait: unknown, Receive: unknown}
}

// Recorder correlates request events into entries. Its methods are safe for
// concurrent use, because response bodies arrive on goroutines of their own.
type Recorder struct {
	creator Creator

	mu      sync.Mutex
	entries []*Entry
	// open holds the entry each in-flight request id is still writing to.
	open map[string]*Entry
}

// NewRecorder returns a recorder that records shots' own version as the
// archive's creator.
func NewRecorder(version string) *Recorder {
	return &Recorder{creator: Creator{Name: "shots", Version: version}, open: map[string]*Entry{}}
}

// Sent opens an entry for request id, which the browser sent at time at.
func (r *Recorder) Sent(id string, at time.Time, req Request) {
	req.Cookies = fill(req.Cookies)
	req.Headers = fill(req.Headers)
	req.QueryString = queryString(req.URL)
	entry := &Entry{
		StartedDateTime: at,
		Time:            unknown,
		Request:         req,
		Response:        Response{Content: Content{}, Cookies: []NameValue{}, Headers: []NameValue{}, HeadersSize: unknown, BodySize: unknown},
		Timings:         UnknownTimings(),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	r.open[id] = entry
}

// Received attaches a response to the entry for id.
func (r *Recorder) Received(id string, resp Response, timings Timings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.open[id]; entry != nil {
		attach(entry, resp, timings)
	}
}

// Redirect attaches a redirect response and closes the entry: the browser
// reuses the request id for the request the redirect points at, which the
// next [Recorder.Sent] opens an entry of its own for.
//
// A redirect response body is never retrievable, so the entry carries the
// status, the headers and the target only.
func (r *Recorder) Redirect(id string, at time.Time, resp Response, timings Timings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.open[id]
	if entry == nil {
		return
	}
	attach(entry, resp, timings)
	closeEntry(entry, at)
	delete(r.open, id)
}

// Finished closes the entry for id with the bytes Chrome received on the wire
// and the response body, which is nil when the body could not be fetched.
func (r *Recorder) Finished(id string, at time.Time, transferred int64, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.open[id]
	if entry == nil {
		return
	}
	entry.Response.BodySize = transferred
	if body != nil {
		entry.Response.Content.Size = int64(len(body))
		// A body that is not text has to survive a JSON round trip, and Go's
		// encoder replaces invalid UTF-8 with U+FFFD.
		if utf8.Valid(body) {
			entry.Response.Content.Text = string(body)
		} else {
			entry.Response.Content.Text = base64.StdEncoding.EncodeToString(body)
			entry.Response.Content.Encoding = "base64"
		}
	}
	closeEntry(entry, at)
	delete(r.open, id)
}

// Failed closes the entry for id with the browser's error text.
func (r *Recorder) Failed(id string, at time.Time, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.open[id]
	if entry == nil {
		return
	}
	entry.FailureText = text
	closeEntry(entry, at)
	delete(r.open, id)
}

// Log returns the log recorded so far.
//
// Requests still in flight are closed at the moment the log is taken: a page
// routinely leaves a response whose body nobody read hanging, and an entry
// without a duration is not a valid HAR entry.
func (r *Recorder) Log() Log {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for id, entry := range r.open {
		closeEntry(entry, now)
		delete(r.open, id)
	}
	entries := make([]*Entry, len(r.entries))
	copy(entries, r.entries)
	return Log{Version: Version, Creator: r.creator, Entries: entries}
}

func attach(entry *Entry, resp Response, timings Timings) {
	resp.Cookies = fill(resp.Cookies)
	resp.Headers = fill(resp.Headers)
	// The version is only known once the response names the protocol.
	if entry.Request.HTTPVersion == "" {
		entry.Request.HTTPVersion = resp.HTTPVersion
	}
	entry.Response = resp
	entry.Timings = timings
}

// closeEntry finishes an entry's timing. The browser reports the phases up to
// the last header byte; what remains of the elapsed time is the body, so
// Receive is the remainder and Time is the sum the specification requires.
func closeEntry(entry *Entry, at time.Time) {
	elapsed := at.Sub(entry.StartedDateTime).Seconds() * 1000
	phases := []float64{entry.Timings.Blocked, entry.Timings.DNS, entry.Timings.Connect, entry.Timings.Send, entry.Timings.Wait}
	var total float64
	for _, phase := range phases {
		if phase > 0 {
			total += phase
		}
	}
	entry.Timings.Receive = max(0, elapsed-total)
	entry.Time = total + entry.Timings.Receive
}

// fill replaces a nil slice, which JSON would write as null, with an empty one.
func fill(values []NameValue) []NameValue {
	if values == nil {
		return []NameValue{}
	}
	return values
}

// queryString splits a URL's query in the order it was written, which
// url.Values cannot preserve.
func queryString(rawURL string) []NameValue {
	out := []NameValue{}
	_, query, ok := strings.Cut(rawURL, "?")
	if !ok {
		return out
	}
	query, _, _ = strings.Cut(query, "#")
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		out = append(out, NameValue{Name: unescape(name), Value: unescape(value)})
	}
	return out
}

func unescape(s string) string {
	if decoded, err := url.QueryUnescape(s); err == nil {
		return decoded
	}
	return s
}
