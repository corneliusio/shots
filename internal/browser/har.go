package browser

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/mostlyserious/shots/internal/har"
)

// HAR buffer sizes, raised well above Chrome's defaults so a page's bodies are
// still in the buffer when [Page.captureBody] asks for them.
const (
	maxTotalBufferSize    = 200 << 20
	maxResourceBufferSize = 50 << 20
)

// captureBody records a finished request, fetching its body on a goroutine of
// its own: the listener runs on the connection's read goroutine, and asking
// Chrome for a body from there would deadlock.
//
// The fetch is eager rather than deferred to the flush, because
// Network.getResponseBody only answers while the body is still in Chrome's
// buffer, which a later navigation empties.
func (p *Page) captureBody(rec *har.Recorder, ev *network.EventLoadingFinished) {
	id, at, transferred := string(ev.RequestID), time.Now(), int64(ev.EncodedDataLength)
	p.mu.Lock()
	if p.harFlushed {
		p.mu.Unlock()
		rec.Finished(id, at, transferred, nil)
		return
	}
	// Counted under the same lock that closes the flush, so every fetch
	// started is one FlushHAR waits for.
	p.bodies.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.bodies.Done()
		rec.Finished(id, at, transferred, p.responseBody(ev.RequestID))
	}()
}

// responseBody returns the body Chrome still holds, or nil. Redirects, cached
// responses and bodies Chrome has evicted have none, which is not an error the
// caller can act on.
func (p *Page) responseBody(id network.RequestID) []byte {
	var body []byte
	p.run(context.Background(), chromedp.ActionFunc(func(c context.Context) error {
		var err error
		body, err = network.GetResponseBody(id).Do(c)
		return err
	}))
	return body
}

// FlushHAR waits for the response bodies still in flight, so a log written
// afterwards holds every body Chrome could produce. Requests that finish later
// are still recorded, without a body.
func (p *Page) FlushHAR() {
	p.mu.Lock()
	p.harFlushed = true
	p.mu.Unlock()
	p.bodies.Wait()
}

func harRequest(req *network.Request) har.Request {
	out := har.Request{
		Method:      req.Method,
		URL:         req.URL,
		Headers:     harHeaders(req.Headers),
		HeadersSize: -1,
		BodySize:    -1,
	}
	if body := postData(req); body != nil {
		out.BodySize = int64(len(body))
		// HAR has nowhere to put a body that is not text; its size is still
		// reported.
		if utf8.Valid(body) {
			out.PostData = &har.PostData{MimeType: headerValue(req.Headers, "content-type"), Text: string(body)}
		}
	}
	return out
}

// postData reassembles a request body, which Chrome reports as base64 chunks
// and omits entirely when it is too large.
func postData(req *network.Request) []byte {
	if !req.HasPostData {
		return nil
	}
	var body []byte
	for _, entry := range req.PostDataEntries {
		decoded, err := base64.StdEncoding.DecodeString(entry.Bytes)
		if err != nil {
			return nil
		}
		body = append(body, decoded...)
	}
	return body
}

func harResponse(resp *network.Response) har.Response {
	return har.Response{
		Status:      int(resp.Status),
		StatusText:  resp.StatusText,
		HTTPVersion: resp.Protocol,
		Headers:     harHeaders(resp.Headers),
		Content:     har.Content{MimeType: resp.MimeType},
		RedirectURL: headerValue(resp.Headers, "location"),
		HeadersSize: -1,
		BodySize:    -1,
	}
}

// harTimings converts Chrome's resource timing, whose numbers are milliseconds
// relative to one baseline, into the HAR phases. Blocked is left unobserved,
// and Receive is the remainder the recorder computes when the entry closes.
func harTimings(t *network.ResourceTiming) har.Timings {
	out := har.UnknownTimings()
	if t == nil {
		return out
	}
	out.DNS = phase(t.DNSStart, t.DNSEnd)
	out.Connect = phase(t.ConnectStart, t.ConnectEnd)
	out.SSL = phase(t.SslStart, t.SslEnd)
	out.Send = phase(t.SendStart, t.SendEnd)
	out.Wait = phase(t.SendEnd, t.ReceiveHeadersEnd)
	return out
}

// phase is the length of one timing phase, or -1 when Chrome did not record
// it: an offset is negative when the phase did not happen.
func phase(start, end float64) float64 {
	if start < 0 || end < start {
		return -1
	}
	return end - start
}

// harHeaders converts Chrome's header map into the ordered pairs HAR wants.
// Chrome joins repeated headers with newlines, which are split apart again,
// and the result is sorted so two runs of the same page produce the same file.
func harHeaders(headers network.Headers) []har.NameValue {
	out := []har.NameValue{}
	for name, value := range headers {
		for _, line := range strings.Split(headerString(value), "\n") {
			out = append(out, har.NameValue{Name: name, Value: line})
		}
	}
	slices.SortFunc(out, func(a, b har.NameValue) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Value, b.Value)
	})
	return out
}

// headerValue returns a header's value, matched without regard to case.
func headerValue(headers network.Headers, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return headerString(value)
		}
	}
	return ""
}

// headerString renders a header value, which CDP delivers as untyped JSON.
func headerString(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}
