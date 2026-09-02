package browser

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/chromedp/cdproto/cdp"
	cdpio "github.com/chromedp/cdproto/io"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// pdfChunk is how much of the PDF stream one IO.read fetches.
const pdfChunk = 256 << 10

// PDFOptions configures [Page.PDF]. The zero value is Chrome's own default:
// portrait Letter paper at scale 1, without background graphics.
//
// Margins are always zero.
type PDFOptions struct {
	// PaperWidth and PaperHeight are in inches. Zero means Chrome's default.
	PaperWidth, PaperHeight float64
	// Landscape swaps the paper's orientation.
	Landscape bool
	// Scale multiplies the rendered page. Zero means 1.
	Scale float64
	// PrintBackground includes background colours and images.
	PrintBackground bool
}

// PDF prints the page and returns the document as a stream, so a large PDF
// reaches the destination without being held in memory. The caller must close
// it, and must not close the page first.
func (p *Page) PDF(ctx context.Context, opts PDFOptions) (io.ReadCloser, error) {
	var handle cdpio.StreamHandle
	err := p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		print := cdppage.PrintToPDF().
			WithTransferMode(cdppage.PrintToPDFTransferModeReturnAsStream).
			WithLandscape(opts.Landscape).
			WithPrintBackground(opts.PrintBackground).
			WithPaperWidth(opts.PaperWidth).
			WithPaperHeight(opts.PaperHeight).
			WithScale(opts.Scale)
		var err error
		_, handle, err = print.Do(c)
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("printing to PDF: %w", err)
	}
	return &stream{ctx: ctx, page: p, handle: handle}, nil
}

// stream reads a CDP IO stream a chunk at a time.
//
// It holds the caller's context because [io.Reader] has nowhere to pass one,
// and cancelling the command has to stop the reads.
type stream struct {
	ctx    context.Context
	page   *Page
	handle cdpio.StreamHandle
	buf    []byte
	eof    bool
}

func (s *stream) Read(out []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.eof {
			return 0, io.EOF
		}
		if err := s.fetch(); err != nil {
			return 0, err
		}
	}
	n := copy(out, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

// fetch appends the next chunk. IO.read's generated Do drops the
// base64Encoded flag, which a PDF stream always sets, so the command is issued
// directly.
func (s *stream) fetch() error {
	return s.page.run(s.ctx, chromedp.ActionFunc(func(c context.Context) error {
		var res cdpio.ReadReturns
		params := cdpio.Read(s.handle).WithSize(pdfChunk)
		if err := cdp.Execute(c, cdpio.CommandRead, params, &res); err != nil {
			return err
		}
		s.eof = res.EOF
		if res.Base64encoded {
			data, err := base64.StdEncoding.DecodeString(res.Data)
			if err != nil {
				return fmt.Errorf("decoding stream chunk: %w", err)
			}
			s.buf = append(s.buf, data...)
			return nil
		}
		s.buf = append(s.buf, res.Data...)
		return nil
	}))
}

// Close releases the stream in the browser. Failing to release it is not worth
// failing an artifact that is already written, so the error is dropped.
func (s *stream) Close() error {
	s.page.run(s.ctx, chromedp.ActionFunc(func(c context.Context) error {
		return cdpio.Close(s.handle).Do(c)
	}))
	return nil
}
