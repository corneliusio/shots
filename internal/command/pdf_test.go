package command

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// mediaBox matches the page boxes Chrome writes uncompressed into every PDF.
// The numbers are PostScript points, 72 to the inch.
var mediaBox = regexp.MustCompile(`/MediaBox \[0 0 ([\d.]+) ([\d.]+)\]`)

// paperOf returns the size of a PDF's first page, in points, and its page
// count.
func paperOf(t *testing.T, data []byte) (width, height float64, pages int) {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatalf("not a PDF: %q", data[:min(16, len(data))])
	}
	boxes := mediaBox.FindAllStringSubmatch(string(data), -1)
	if len(boxes) == 0 {
		t.Fatalf("no page box in %d bytes of PDF", len(data))
	}
	width, _ = strconv.ParseFloat(boxes[0][1], 64)
	height, _ = strconv.ParseFloat(boxes[0][2], 64)
	return width, height, len(boxes)
}

func pdf(t *testing.T, args ...string) []byte {
	t.Helper()
	got := run(t, append([]string{"pdf"}, append(args, "-o", "-")...)...)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	return got.stdout
}

func TestPDFPaper(t *testing.T) {
	srv := site(t)
	tests := []struct {
		name          string
		args          []string
		width, height float64
	}{
		{"the default is Letter", nil, 612, 792},
		{"landscape turns the paper", []string{"--landscape"}, 792, 612},
		{"a format sets the size", []string{"--format", "A5"}, 420, 595.4},
		{"a format is case-insensitive", []string{"--format", "legal"}, 612, 1008},
		{"inches are inches", []string{"--width", "10in", "--height", "5in"}, 720, 360},
		{"millimetres convert", []string{"--width", "210mm", "--height", "297mm"}, 595.3, 841.9},
		{"a bare number is CSS pixels", []string{"--width", "800", "--height", "600"}, 600, 450},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := pdf(t, append([]string{srv.URL + "/index.html"}, tt.args...)...)
			w, h, _ := paperOf(t, data)
			// Chrome rounds paper sizes, so a point of slack is expected.
			if math.Abs(w-tt.width) > 1 || math.Abs(h-tt.height) > 1 {
				t.Errorf("page is %gx%g points, want %gx%g", w, h, tt.width, tt.height)
			}
		})
	}
}

func TestPDFMedia(t *testing.T) {
	srv := site(t)
	// print.html hides its tall slabs in print media, so the page count says
	// which media the PDF was rendered with.
	_, _, printed := paperOf(t, pdf(t, srv.URL+"/print.html"))
	_, _, screen := paperOf(t, pdf(t, srv.URL+"/print.html", "--media-screen"))
	if printed != 1 {
		t.Errorf("print media produced %d pages, want 1", printed)
	}
	if screen <= printed {
		t.Errorf("screen media produced %d pages, print media %d; want more", screen, printed)
	}
}

func TestPDFOutput(t *testing.T) {
	srv := site(t)

	t.Run("standard output carries the PDF alone", func(t *testing.T) {
		got := run(t, "pdf", srv.URL+"/index.html", "-o", "-")
		if got.code != exitOK || got.stderr != "" {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if _, _, pages := paperOf(t, got.stdout); pages != 1 {
			t.Errorf("got %d pages, want 1", pages)
		}
		if !bytes.HasSuffix(bytes.TrimSpace(got.stdout), []byte("%%EOF")) {
			t.Error("the PDF is truncated")
		}
	})

	t.Run("a file is written and reported", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "page.pdf")
		got := run(t, "pdf", srv.URL+"/index.html", "-o", out)
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		paperOf(t, data)
		if len(got.stdout) > 0 || !strings.Contains(got.stderr, out) {
			t.Errorf("stdout = %q, stderr = %q", got.stdout, got.stderr)
		}
	})

	t.Run("an omitted output is named after the URL", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if got := run(t, "pdf", srv.URL+"/index.html"); got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		matches, err := filepath.Glob("*-index-html.pdf")
		if err != nil || len(matches) != 1 {
			names, _ := filepath.Glob("*")
			t.Fatalf("want one *-index-html.pdf, found %v", names)
		}
	})

	t.Run("a background is only printed when asked for", func(t *testing.T) {
		plain := pdf(t, srv.URL+"/transparent.html")
		painted := pdf(t, srv.URL+"/transparent.html", "--print-background")
		if len(painted) <= len(plain) {
			t.Errorf("PDF with backgrounds is %d bytes, without %d; want larger", len(painted), len(plain))
		}
	})
}

func TestPDFUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		says string
	}{
		{"an unknown format", []string{"--format", "postcard"}, "--format must be one of"},
		{"a format with a width", []string{"--format", "a4", "--width", "3in"}, "cannot be combined"},
		{"a width without a number", []string{"--width", "wide"}, "--width must be a positive length"},
		{"a negative height", []string{"--height=-2in"}, "--height must be a positive length"},
		{"a scale out of range", []string{"--scale", "3"}, "--scale must be between"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(t, append([]string{"pdf", "example.com"}, tt.args...)...)
			if got.code != exitUsage {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, tt.says) {
				t.Errorf("stderr = %q, want it to mention %q", got.stderr, tt.says)
			}
		})
	}
}
