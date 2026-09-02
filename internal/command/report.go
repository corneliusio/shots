package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
)

const (
	shotReportSchema = "shots.capture.v1"
	reportItemLimit  = 100
)

// shotReport is the bounded, machine-readable result of one capture.
type shotReport struct {
	Schema         string            `json:"schema"`
	OK             bool              `json:"ok"`
	RequestedURL   string            `json:"requested_url"`
	FinalURL       string            `json:"final_url,omitempty"`
	Status         int               `json:"status"`
	Output         string            `json:"output,omitempty"`
	DurationMS     int64             `json:"duration_ms"`
	Console        []browser.Message `json:"console"`
	PageErrors     []string          `json:"page_errors"`
	FailedRequests []browser.Request `json:"failed_requests"`
	Omitted        reportOmitted     `json:"omitted"`
	Error          string            `json:"error,omitempty"`
}

type reportOmitted struct {
	Console        int `json:"console"`
	PageErrors     int `json:"page_errors"`
	FailedRequests int `json:"failed_requests"`
}

// shotDiagnostics accepts callbacks from Chrome's event goroutine and keeps a
// bounded snapshot for the current capture.
type shotDiagnostics struct {
	enabled bool

	mu             sync.Mutex
	console        []browser.Message
	pageErrors     []string
	failedRequests []browser.Request
	omitted        reportOmitted
}

func newShotDiagnostics(enabled bool) *shotDiagnostics {
	return &shotDiagnostics{enabled: enabled}
}

func (d *shotDiagnostics) reset() {
	if !d.enabled {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.console = nil
	d.pageErrors = nil
	d.failedRequests = nil
	d.omitted = reportOmitted{}
}

// attach composes report collection with callbacks such as --log-requests.
func (d *shotDiagnostics) attach(opts *browser.PageOptions) {
	if !d.enabled {
		return
	}
	console := opts.OnConsole
	opts.OnConsole = func(message browser.Message) {
		if console != nil {
			console(message)
		}
		d.addConsole(message)
	}
	exception := opts.OnException
	opts.OnException = func(message string) {
		if exception != nil {
			exception(message)
		}
		d.addPageError(message)
	}
	request := opts.OnRequest
	opts.OnRequest = func(item browser.Request) {
		if request != nil {
			request(item)
		}
		if item.Error != "" || item.Status >= 400 {
			d.addFailedRequest(item)
		}
	}
}

func (d *shotDiagnostics) addConsole(message browser.Message) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.console) < reportItemLimit {
		d.console = append(d.console, message)
		return
	}
	d.omitted.Console++
}

func (d *shotDiagnostics) addPageError(message string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pageErrors) < reportItemLimit {
		d.pageErrors = append(d.pageErrors, message)
		return
	}
	d.omitted.PageErrors++
}

func (d *shotDiagnostics) addFailedRequest(request browser.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.failedRequests) < reportItemLimit {
		d.failedRequests = append(d.failedRequests, request)
		return
	}
	d.omitted.FailedRequests++
}

func (d *shotDiagnostics) snapshot(target, finalURL, output string, status int, started time.Time, captureErr error) shotReport {
	d.mu.Lock()
	defer d.mu.Unlock()
	report := shotReport{
		Schema:         shotReportSchema,
		OK:             captureErr == nil || errors.Is(captureErr, errSkipped),
		RequestedURL:   target,
		FinalURL:       finalURL,
		Status:         status,
		Output:         output,
		DurationMS:     time.Since(started).Milliseconds(),
		Console:        append([]browser.Message{}, d.console...),
		PageErrors:     append([]string{}, d.pageErrors...),
		FailedRequests: append([]browser.Request{}, d.failedRequests...),
		Omitted:        d.omitted,
	}
	if captureErr != nil && !errors.Is(captureErr, errSkipped) {
		report.Error = captureErr.Error()
	}
	return report
}

func writeShotReport(e *env, path string, report shotReport) error {
	return writeShotReports(e, path, []shotReport{report})
}

// writeShotReports writes one JSON object per line. One-shot reports contain a
// single line; multi and repeat reports keep the same stream-friendly format.
func writeShotReports(e *env, path string, reports []shotReport) error {
	if path == "" {
		return nil
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, report := range reports {
		if err := encoder.Encode(report); err != nil {
			return err
		}
	}
	return artifact.Write(path, data.Bytes(), artifact.Options{Stdout: e.stdout})
}
