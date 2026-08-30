package command

import (
	"bytes"
	"context"
	"strings"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
	"github.com/mostlyserious/shots/internal/har"
)

type harCmd struct {
	URL string `arg:"" help:"URL, bare domain, or path to a local HTML file."`

	Zip     bool   `short:"z" help:"Write a zipped .har.zip file."`
	Extract bool   `short:"x" help:"Also extract the captured resources into a directory beside the HAR file."`
	Output  string `short:"o" placeholder:"FILE" help:"Write to this file, or - for standard output."`

	pageFlags
}

func (c *harCmd) Run(ctx context.Context, e *env) error {
	if err := c.validate(); err != nil {
		return err
	}
	target, err := NormalizeURL(c.URL)
	if err != nil {
		return err
	}
	output, zip, err := c.output(target)
	if err != nil {
		return err
	}
	script, err := c.script(ctx, e)
	if err != nil {
		return err
	}

	recorder := har.NewRecorder(Version)
	page, closer, err := c.open(ctx, e, target, script, browser.PageOptions{HAR: recorder})
	if err != nil {
		return err
	}
	defer closer()

	page.FlushHAR()
	if err := c.writeHAR(e, recorder, output, zip); err != nil {
		return err
	}
	if !c.Extract {
		return nil
	}
	dir := strings.TrimSuffix(strings.TrimSuffix(output, ".zip"), ".har")
	written, err := har.Extract(recorder.Log(), dir)
	if err != nil {
		return err
	}
	e.progress(c.Silent, "Extracted %d resources to %q", written, dir)
	return nil
}

func (c *harCmd) validate() error {
	if err := c.pageFlags.validate(); err != nil {
		return err
	}
	if c.Extract && c.Output == artifact.Stdout {
		return usagef("--extract cannot be combined with --output -")
	}
	return nil
}

// output resolves the destination and whether it is zipped. An automatic name
// is derived from the URL; an explicit one is used as given, except that
// --extract treats a name without a HAR extension as the base path both the
// file and the directory are built from, as upstream does. A .zip suffix zips
// the file too, so naming one is enough.
func (c *harCmd) output(target string) (string, bool, error) {
	zip := c.Zip || isZipPath(c.Output)
	ext := "har"
	if zip {
		ext = "har.zip"
	}
	if c.Extract && c.Output != "" && !strings.HasSuffix(c.Output, ".har") && !isZipPath(c.Output) {
		return c.Output + "." + ext, zip, nil
	}
	path, err := artifact.Path(c.Output, target, ext)
	return path, zip, err
}

// harFlags record a HAR file alongside a command's own output. Upstream has
// them on multi; PLAN.md Section 7 puts them on shot too.
type harFlags struct {
	HAR     bool   `name:"har" help:"Record a HAR file named trace.har."`
	HARZip  bool   `name:"har-zip" help:"Record a zipped HAR file named trace.har.zip."`
	HARFile string `name:"har-file" placeholder:"FILE" help:"Record a HAR file to this path; a .zip suffix zips it."`
}

// path resolves where the HAR goes, and whether it is zipped. An empty path
// means no HAR was asked for.
func (f harFlags) path() (string, bool, error) {
	zip := f.HARZip || isZipPath(f.HARFile)
	switch {
	case f.HARFile != "":
		return f.HARFile, zip, nil
	case !f.HAR && !f.HARZip:
		return "", false, nil
	}
	ext := "har"
	if zip {
		ext = "har.zip"
	}
	// Upstream names the automatic file after the URL "trace".
	path, err := artifact.Unique("trace", ext)
	return path, zip, err
}

// writeHAR writes a recorded log. Every page it recorded must have been flushed
// first, so that the bodies still in flight are in it.
func (f sessionFlags) writeHAR(e *env, recorder *har.Recorder, path string, zip bool) error {
	write := har.Write
	if zip {
		write = har.WriteZip
	}
	var buf bytes.Buffer
	if err := write(&buf, recorder.Log()); err != nil {
		return err
	}
	if err := artifact.WriteStream(path, &buf, artifact.Options{Stdout: e.stdout}); err != nil {
		return err
	}
	if path != artifact.Stdout {
		e.progress(f.Silent, "Wrote to HAR file: %q", path)
	}
	return nil
}

func isZipPath(path string) bool { return strings.HasSuffix(path, ".zip") }
