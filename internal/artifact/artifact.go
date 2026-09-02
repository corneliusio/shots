// Package artifact writes command output: automatic filenames, collision
// suffixes, atomic file writes, and standard output.
package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Stdout is the output path that means "write to standard output".
const Stdout = "-"

// maxSuffix bounds the collision suffixes tried by [Unique].
const maxSuffix = 1000

// ErrExists reports that an output file already exists and must not be
// overwritten because [Options].NoClobber was set.
var ErrExists = errors.New("output file exists")

// Options configures a write.
type Options struct {
	// NoClobber makes writing to an existing file fail with [ErrExists]
	// instead of replacing it.
	NoClobber bool
	// Mode is the permission mode of the finished file. Zero means 0644.
	// It is applied regardless of the process umask.
	Mode fs.FileMode
	// Stdout receives the data when the path is [Stdout]. Nil means os.Stdout.
	Stdout io.Writer
}

// FilenameFor derives an automatic output filename from a URL and an extension
// such as "png" or "har.zip": take the host and path, fold ".", "/" and "-" to
// "-", drop every character outside [A-Za-z0-9_-], and append the extension.
// Query strings and fragments are excluded, and the result never contains a
// path separator. An empty stem becomes "shot".
func FilenameFor(rawURL, ext string) string {
	return join(Stem(rawURL), ext)
}

// Stem derives the extension-less part of an automatic filename. It never
// contains a path separator, so it is also the safe way to name a file after a
// URL a page chose, as HAR resource extraction does.
func Stem(rawURL string) string {
	base := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		base = u.Host + u.Path
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r == '-', r == '.', r == '/':
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "shot"
	}
	return name
}

func join(stem, ext string) string {
	if ext == "" {
		return stem
	}
	return stem + "." + ext
}

// Unique returns "stem.ext", or "stem.1.ext", "stem.2.ext" and so on, for the
// first variant that does not yet exist. The extension is given separately
// rather than derived from the name, so multi-part extensions such as "har.zip"
// keep the suffix in front of the whole extension.
//
// Nothing is reserved on disk, so a concurrent writer can still win the race
// between this call and the write that follows it.
func Unique(stem, ext string) (string, error) {
	for suffix := 0; suffix <= maxSuffix; suffix++ {
		candidate := stem
		if suffix > 0 {
			candidate = fmt.Sprintf("%s.%d", stem, suffix)
		}
		candidate = join(candidate, ext)
		switch _, err := os.Lstat(candidate); {
		case errors.Is(err, fs.ErrNotExist):
			return candidate, nil
		case err != nil:
			return "", err
		}
	}
	return "", fmt.Errorf("%s: more than %d files with this name", join(stem, ext), maxSuffix)
}

// Path resolves the destination for an artifact. An explicit output path is
// used as given; an empty one becomes a collision-free automatic filename
// derived from rawURL.
func Path(output, rawURL, ext string) (string, error) {
	if output != "" {
		return output, nil
	}
	return Unique(Stem(rawURL), ext)
}

// Write writes data to path. See [WriteStream].
func Write(path string, data []byte, opts Options) error {
	return WriteStream(path, bytes.NewReader(data), opts)
}

// WriteStream copies r to path, or to standard output when path is [Stdout].
//
// Files are written through a temporary file in the destination directory and
// renamed into place, so a failed write leaves neither a partial file nor a
// stale temporary file behind. Parent directories are never created.
func WriteStream(path string, r io.Reader, opts Options) error {
	if path == Stdout {
		w := opts.Stdout
		if w == nil {
			w = os.Stdout
		}
		_, err := io.Copy(w, r)
		return err
	}
	if opts.NoClobber {
		switch _, err := os.Lstat(path); {
		case err == nil:
			return fmt.Errorf("%s: %w", path, ErrExists)
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	mode := opts.Mode
	if mode == 0 {
		mode = 0o644
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".shots-*")
	if err != nil {
		parent := filepath.Dir(path)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("writing %s: parent directory %s does not exist: %w", path, parent, fs.ErrNotExist)
		}
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	// Removes the temporary file on every failure path; after a successful
	// rename there is nothing left to remove.
	defer os.Remove(tmp.Name())
	if err := writeTemp(tmp, r, mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func writeTemp(f *os.File, r io.Reader, mode fs.FileMode) error {
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}
