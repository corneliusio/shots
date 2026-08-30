# shots

Take automated screenshots, PDFs, HTML, HAR files and screen recordings with
Chrome, from the command line or from a YAML file.

`shots` is a Chromium-first Go rewrite inspired by
[shot-scraper](https://github.com/simonw/shot-scraper) 1.11. It needs Chrome or
Chromium and nothing else: no Node, no Playwright, no WebDriver server. Python
is needed only by a configuration that contains a `python:` step, and `ffmpeg`
only by `shots video`.

## Install

```bash
go install github.com/mostlyserious/shots/cmd/shots@latest
```

Release archives for macOS, Linux and Windows, with a `SHA256SUMS` manifest, are
attached to each [GitHub release](https://github.com/mostlyserious/shots/releases).

`shots` uses a Chrome it finds on your machine. To get a pinned, known-good
build instead:

```bash
shots install    # downloads Chrome for Testing into the user cache
shots doctor     # reports the browser, cache, ffmpeg and Python shots will use
```

`shots install` verifies a SHA-256 checksum committed to this repository before
extracting, and never calls a system package manager. `shots doctor` exits 1 if
it finds a problem:

```
shots        1.0.0
platform     darwin/arm64
pinned       Chrome 152.0.7977.64 for mac-arm64
browser      /Users/you/Library/Caches/shots/chrome/152.0.7977.64/…/Google Chrome for Testing
cdp          ok, Chrome/152.0.7977.64
cache        /Users/you/Library/Caches/shots (writable)
ffmpeg       /opt/homebrew/bin/ffmpeg
python       /opt/homebrew/bin/python3
```

Chrome is resolved in this order: `--browser-path`, `$SHOTS_BROWSER_PATH`, a
build from `shots install`, Chrome or Chromium on `PATH`, then the usual
platform locations. `--cdp-url` connects to a browser you started yourself
instead of launching one.

## Quick start

```bash
shots example.com                       # example-com.png
shots example.com -o home.png -w 1280 -h 720
shots https://example.com -s '#main' -p 20 -o main.png
shots javascript example.com 'document.title'
shots pdf example.com -o page.pdf
```

### Command names versus URLs

`shot` is the default command, so `shots example.com` is `shots shot
example.com`. **A first argument that names a command always selects that
command**: `shots html` runs the `html` command and complains about the missing
URL. To capture a host that collides with a command name, be explicit —
`shots shot html` — or use a path form, `shots ./html`.

## Commands

Every browser command shares one lifecycle: resolve Chrome, open an isolated
browser context, restore `--auth` state, configure the viewport, navigate, apply
`--skip`/`--fail`, wait, run JavaScript, write the artifact, tear everything
down. So the shared flags (`--auth`, `--timeout`, `--wait`, `--wait-for`,
`--javascript`, `--js-file`, `--interactive`, `--skip`, `--fail`, `--silent`,
`--browser-path`, `--cdp-url`, …) mean the same thing everywhere. Run
`shots <command> --help` for the full list.

### `shots shot URL`

Screenshots. The default width is 1280; omitting `--height` captures the full
page. Output is PNG unless `--quality` is given or the filename ends in `.jpg`
or `.jpeg`. Repeated selectors capture their combined bounding rectangle, and
`--padding` grows it.

```bash
shots shot https://example.com -h 720 --retina -o hero.png
shots shot https://example.com -s '#nav' -s '#footer' -p 16 -o chrome.jpg
shots shot https://example.com -o - > shot.png
```

### `shots multi CONFIG`

Runs a YAML list of shots, shell steps, Python steps and servers, in source
order, against one Chrome process and one browser context — so an entry that
signs in is still signed in for the next.

```yaml
- server: python -m http.server 8000
- url: http://localhost:8000/
  output: home.png
  height: 720
- sh: node scripts/seed.js
- url: http://localhost:8000/pricing
  output: pricing.jpg
  quality: 80
  selectors:
    - .plans
  wait_for: document.querySelectorAll('.plan').length === 3
```

```bash
shots multi shots.yml
shots multi shots.yml -o home.png        # take only this entry's shot
shots multi - < shots.yml
```

Entry keys: `url`, `output`, `width`, `height`, `quality`, `omit_background`,
`wait`, `wait_for`, `javascript`, `js_file`, `padding`, `selector`/`selectors`,
`selector_all`/`selectors_all`, `js_selector`/`js_selectors`,
`js_selector_all`/`js_selectors_all`, plus the step keys `sh`, `python` and
`server`. Anything else is rejected with its field path
(`entries[3].retina: unknown field`) and exit status 2. Relative paths resolve
against the configuration file's directory; a configuration on standard input
resolves against the current directory.

A failed `sh:` or `python:` step stops the run, because everything after it was
written expecting it to have run. A failed shot is reported and the run carries
on, unless `--fail-on-error`. Servers are stopped — process group and all — on
success, failure, timeout and interrupt, unless `--leave-server`.

### `shots javascript URL [SCRIPT]`

Runs JavaScript on the page and prints the result as JSON on stdout. Promises
are awaited, and a script that evaluates to a function is called.

```bash
shots javascript example.com 'document.title'
shots javascript example.com --js-file links.js
shots javascript example.com -r 'document.title'          # unquoted string
echo 'location.href' | shots javascript example.com
```

### `shots html URL`

The rendered document, or the outer HTML of the first element matching
`--selector`.

```bash
shots html example.com -o page.html
shots html example.com -s '#main'
```

### `shots pdf URL`

`Page.printToPDF`, streamed to disk rather than round-tripped through base64.
Paper formats are `Letter`, `Legal`, `Tabloid`, `Ledger` and `A0` to `A6`;
`--width`/`--height` take `in`, `cm`, `mm` or `px`, and a bare number is pixels.

```bash
shots pdf example.com --format A4 --print-background -o page.pdf
shots pdf example.com --media-screen --landscape -o wide.pdf
```

### `shots accessibility URL`

Chrome's accessibility tree, filtered to a stable JSON format: role, name,
value, description, states, properties and ordered children. Ignored nodes and
`InlineTextBox` nodes are dropped, and nothing exposes Chrome's transient node
identifiers.

```bash
shots accessibility example.com -o a11y.json
```

### `shots har URL`

HAR 1.2 built by correlating CDP network events, with response bodies fetched as
each request finishes so navigation cannot evict them.

```bash
shots har example.com -o trace.har
shots har example.com -z -o trace.har.zip
shots har example.com -x                  # also extract the captured resources
```

`shot` and `multi` take `--har`, `--har-zip` and `--har-file` to record
alongside their own output; on `multi` that is one archive for the whole run.

### `shots video STORYBOARD`

Records a scripted browser demonstration to WebM, and optionally MP4. Needs
`ffmpeg`, which is checked before anything is launched or run.

```yaml
url: http://localhost:8000/
output: demo.webm
viewport: {width: 1280, height: 720}
cursor: {visible: true, clicks: true}
scenes:
  - name: Search
    do:
      - pause: 1
      - click: '#search'
      - type: {selector: '#search', text: shots, delay_ms: 90}
      - press: Enter
      - wait_for: '.results'
      - scroll: {y: 600, duration: 1.2}
      - screenshot: results.png
```

```bash
shots video demo.yml --mp4
shots video demo.yml -o /tmp/demo.webm --keep-frames
```

Top-level keys: `output`, `url`, `viewport`, `cursor`, `wait`, `wait_for`,
`wait_for_url`, `javascript`, `sh`, `python`, `server`, `scenes`. A scene takes
`name`, `open`, `wait_for`, `wait_for_url`, `sh`, `python` and `do`. Actions are
`open`, `click`, `fill`, `type`, `press`, `scroll`, `pause`, `wait_for`,
`wait_for_url`, `javascript`/`js`, `screenshot`, `sh` and `python`; each accepts
a scalar shorthand (`click: '#go'`) or a mapping of its own keys
(`click: {selector: '#go', button: right}`).

If encoding fails, the command exits 1 and keeps the captured frames, naming the
directory. `--keep-frames` keeps them after a successful encode too.

### `shots auth URL STATE_FILE`

Opens a headed browser, waits for you to sign in and press Enter, then writes
cookies and per-origin `localStorage` as a Playwright `storage_state` file with
mode 0600.

```bash
shots auth https://example.com/login auth.json
shots shot https://example.com/account -a auth.json -o account.png
```

State files written by `shot-scraper auth` work here unchanged, and vice versa.

### `shots install`

Downloads the pinned Chrome for Testing build into
`<user-cache-dir>/shots/chrome/<version>/<platform>/`, verifying the committed
SHA-256 checksum before extracting. `--force` re-downloads.

```bash
shots install
```

### `shots doctor`

Reports the version, platform, resolved browser, CDP connectivity, cache
permissions, `ffmpeg` and Python. Exits 1 if anything is a problem — including
Linux shared libraries `ldd` reports missing for the browser.

```bash
shots doctor
```

## Output and exit codes

stdout carries only what you asked for: image bytes for `-o -`, a JSON result,
HTML, a HAR. Progress and errors go to stderr, and `--silent` suppresses
progress but never a fatal error. Files are written to a temporary file in the
destination directory and renamed into place, so a failed command leaves no
misleading artifact and no half-written file. Parent directories are never
created for you.

| Status | Meaning |
| --- | --- |
| 0 | Success, including a page that `--skip` skipped |
| 1 | Browser, network, filesystem, process or runtime failure |
| 2 | Invalid arguments or configuration |

## Security

`sh:`, `python:`, `--javascript`, `--js-file` and `gh:` remote scripts are
**trusted user code and are not sandboxed**. They run with your privileges. Only
run configurations and scripts you would run yourself.

`shots` never logs cookies, storage values, passwords or authorization headers.
Authentication state files are written with mode 0600. HAR extraction rejects
absolute paths and directory traversal. A browser is downloaded only when you
run `shots install`, only from the pinned manifest, and only after its checksum
matches.

## Differences from shot-scraper 1.11

`shots` keeps upstream's commands, flags and YAML keys where that is practical.
The deliberate differences:

- **Chromium only.** No Firefox or WebKit, so upstream's `-b/--browser` is gone.
  `--browser-path` picks an executable and `--cdp-url` attaches to a running
  browser.
- **No Playwright selector compatibility.** CSS selectors everywhere;
  `text=` and `:has-text()` are not supported. `--js-selector` covers the rest.
  `wait_for_url` in a storyboard is a glob (`**`, `*`, `?`).
- **`-i` is `--interactive` on every command**, and the script-file flag is
  `--js-file` everywhere, including `shots javascript` — upstream spends `-i` on
  `javascript --input`. `--js-file` accepts a path, `-` for standard input, and
  `gh:user/script.js`.
- **JPEG is chosen by `--quality` or by a `.jpg`/`.jpeg` output extension**, on
  `shot`, on a `multi` entry and on a storyboard `screenshot:` alike.
- **`multi` exits 1 when shots failed**, with a `1 of 2 entries failed` summary.
  Upstream reports each failure and exits 0, which is the wrong default for a
  scheduled job.
- **A step's process group is cleaned up**, so a `sh:` step that backgrounds a
  server does not leak it. `server:` is how a configuration says "keep this
  running", and `--leave-server` is how it survives shots.
- **`ffmpeg` is required for any video**, not only for `--mp4`: here the encoder
  is the whole output path, and the check runs before the storyboard does.
- **`shots video` has no `-o -`.** A WebM is a container ffmpeg seeks around
  while writing; there is nothing useful to stream.
- **Accessibility and HAR formatting are shots' own**, not Playwright's. Both are
  stable and documented above; neither is byte-compatible with upstream.
- **A skipped or failed page writes no HAR**, where upstream's lands anyway.
- **Migration path:** authentication state is Playwright `storage_state` JSON, so
  files from `shot-scraper auth` work unchanged. There is no `shot-scraper`
  compatibility binary.

`COMPAT.md` is the upstream flag matrix read out of the pinned 1.11 commit, and
`DECISIONS.md` records why each difference exists.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
```

Integration tests drive a deterministic local site embedded in
`internal/testsite`; no test touches a public website. `go run ./tools/pin-chrome`
re-pins the Chrome for Testing build and rewrites its checksum manifest.

## License

Apache-2.0. See `LICENSE` and `NOTICE`, which records the upstream attribution.
