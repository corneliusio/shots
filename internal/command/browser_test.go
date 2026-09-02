package command

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestBrowserSharesOneChrome(t *testing.T) {
	srv := site(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		defer pw.Close()
		done <- Main(ctx, []string{"browser"}, strings.NewReader(""), pw, &stderr)
	}()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		code := <-done
		t.Fatalf("reading the URL: %v (exit %d, stderr:\n%s)", err, code, stderr.String())
	}
	url := strings.TrimSpace(line)
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("url = %q, want a loopback http URL", url)
	}

	for range 2 {
		out := shot(t, srv.URL+"/geometry.html", "--cdp-url", url, "-s", "#box-a")
		if w, h := bounds(t, out); w != 100 || h != 50 {
			t.Errorf("got %dx%d, want 100x50", w, h)
		}
	}

	cancel()
	if code := <-done; code != exitOK {
		t.Errorf("exit %d after interruption, want %d, stderr:\n%s", code, exitOK, stderr.String())
	}
}
