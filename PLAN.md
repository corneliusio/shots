# Implementation plan for `shots`

## 1. Project direction

Build `shots` as a Chromium-first Go rewrite inspired by `shot-scraper` 1.11.

The project will:

- Use Go for the CLI, orchestration, configuration, process management, HAR generation, and artifact handling.
- Use Chrome DevTools Protocol for browser automation, through a Go CDP library selected in Phase 1 (see Section 3).
- Require Chrome or Chromium, but not Node or a WebDriver server.
- Preserve familiar `shot-scraper` commands and flags where practical.
- Document differences where Playwright behavior cannot or should not be reproduced.
- Use `ffmpeg` only for video encoding.

The upstream compatibility reference is `shot-scraper` 1.11 at commit `177f5abb32add7622d5152eb7064a2ce47dd2fbf` (the "Release 1.11" commit).

## 2. Scope

### Goals

- Capture JPEG screenshots by default, with explicit lossless PNG output.
- Support URLs and local HTML files.
- Capture complete pages, fixed viewports, elements, and combined selector regions.
- Execute JavaScript and wait for JavaScript conditions.
- Export HTML, JSON evaluation results, PDFs, accessibility data, and HAR files.
- Capture and restore authentication state, in a format compatible with Playwright's `storage_state`.
- Run multi-shot YAML files.
- Record scripted browser demonstrations from YAML storyboards.
- Ship native macOS, Linux, and Windows binaries.
- Keep stdout clean for binary and machine-readable output.
- Give agents a bounded JSON capture report without requiring separate log parsing.
- Reuse one browser session for explicitly triggered screenshot iteration.
- Clean up Chrome, servers, and child processes after errors or interrupts.

### Non-goals

- Firefox or WebKit support.
- Full Playwright selector compatibility.
- Byte-identical output across Chrome versions or operating systems.
- Exact Playwright accessibility or HAR formatting.
- Sandboxing user-provided shell or JavaScript code.
- Automatic installation of system packages.
- Concurrent batch capture in the first release.
- A web interface, daemon, or plugin system.

## 3. Technology choices

Go packages:

- A CDP library: `github.com/chromedp/chromedp` + `github.com/chromedp/cdproto`, or `github.com/go-rod/rod`. Both will be evaluated against the Phase 1 probe list; the decision is recorded as a Phase 1 exit artifact. Selection criteria: CDP domain coverage (screencast, network body capture, storage), maintenance activity, and API fit. The `browser` package boundary (Section 4) keeps this choice reversible.
- `github.com/alecthomas/kong` for typed commands and flags.
- `go.yaml.in/yaml/v3` for YAML decoding. v3 is the stable line; v4 is still a release candidate (`v4.0.0-rc.6` as of 2026-08) and will be adopted only after a stable release. Migration cost is low because all YAML decoding is confined to the `config` package.

YAML decoding strategy: stock `KnownFields(true)` strict decoding is not sufficient for the multi-shot and storyboard formats, because their entries are union types (a screenshot request *or* `sh:` / `server:`). The `config` package will decode via `yaml.Node`: classify each entry by its discriminating keys, decode into the matching typed struct strictly, and build precise field paths (`entries[3].server`) for error messages. This is planned work, not an afterthought.

Use the standard library for:

- HTTP requests
- URL and filesystem handling
- JSON
- ZIP archives
- Processes and signals
- Structured logging
- Image inspection
- Temporary files and atomic renames
- Synchronization and cancellation

Do not add validation, HTTP, logging, process, or HAR dependencies unless implementation proves they are needed.

## 4. Proposed repository structure

```text
cmd/
  shots/
    main.go

internal/
  app/             application orchestration
  command/         Kong command and flag definitions
  browser/         Chrome discovery, launch and CDP operations
  artifact/        filenames, stdout and atomic file writes
  config/          multi-shot and storyboard YAML models and union decoding
  har/             network-event correlation and HAR generation
  process/         shell, server and ffmpeg lifecycle
  video/           storyboard execution and screencast processing
  testsite/        deterministic browser integration server

testdata/
  pages/
  multi/
  storyboard/
  golden/
    darwin/
    linux/
    windows/
```

The `browser` package will prevent CDP-library types from leaking into command handlers. This gives unit tests a small browser interface to fake, and keeps the chromedp-versus-rod decision reversible.

## 5. Browser management

### Browser discovery

Resolve Chrome in this order:

1. `--browser-path`
2. `SHOTS_BROWSER_PATH`
3. A browser downloaded previously by `shots install`
4. Chrome Headless Shell, Chrome or Chromium found on `PATH`
5. Known platform application locations

If no browser is found, report every location checked and exit with status `1`.

Support `--cdp-url` for connecting to an existing browser. It will be mutually exclusive with browser paths, channels, and launch arguments.

### Browser installation

`shots install` will download a pinned Chrome for Testing Headless Shell build into the user cache:

```text
<user-cache-directory>/shots/chrome-headless-shell/<version>/<platform>/
```

It will:

- Download using `net/http`.
- Extract using `archive/zip`.
- Verify a SHA-256 checksum recorded in a project browser manifest.
- Write into a temporary directory before renaming.
- Never call Homebrew, Nix, apt, or another system package manager.
- Report missing Linux libraries without trying to install them.

Checksum provenance: Chrome for Testing's `known-good-versions` endpoints publish URLs but **no hashes**. The project therefore computes per-platform SHA-256 checksums itself when pinning a Headless Shell version, trusting that initial download, and commits them to the manifest. Every version bump repeats this step for all platforms. A small maintainer script (`go run ./tools/pin-chrome`) will fetch all platform archives, compute hashes, and rewrite the manifest, so bumps stay a one-command chore.

### Diagnostics

Add `shots doctor` to report:

- `shots` version
- Operating system and architecture
- Selected browser path and version
- CDP connectivity
- Cache-directory permissions
- `ffmpeg` availability
- Any version or dependency problems

## 6. Common browser lifecycle

Every browser command will follow the same sequence:

1. Parse and validate arguments.
2. Normalize the URL or local path.
3. Resolve or connect to Chrome.
4. Create an isolated browser context.
5. Restore authentication state if requested. Cookies restore directly; per-origin storage restores by opening a blank page on each origin recorded in the state file and writing its `localStorage` there (the same technique Playwright uses internally), then closing those pages before navigation.
6. Configure viewport, user agent, CSP, media preferences, credentials, and timeouts.
7. Navigate to the page.
8. Apply `--skip` or `--fail` HTTP policy.
9. Run waits and JavaScript.
10. When requested, stabilize fonts, images, visual motion, and paint timing.
11. Produce the requested artifact and structured capture report.
12. Flush HAR or video data.
13. Close the browser context.
14. Close Chrome if `shots` launched it.
15. Clean temporary files and child processes.

`SIGINT` and `SIGTERM` will use the same cleanup path.

Default timeouts: navigation and script execution default to 30 seconds each, matching upstream's inherited Playwright default. `--timeout` overrides both. No operation runs unbounded.

## 7. Command plan

### Command-name versus URL resolution

Because a URL as the first argument is shorthand for `take`, a first argument like `html` or `multi` is ambiguous. The rule: **subcommand names always win**. `shots html` invokes the `html` command (and errors on the missing URL argument). To screenshot a host or file whose name collides with a subcommand, use the explicit form (`shots take html`) or a path form (`shots ./html`). This rule is documented in `--help` and in the CLI reference.

### `shots take URL`

A URL as the first argument is shorthand for `take`, and `shots` with no arguments prints the help:

```bash
shots https://example.com
shots take https://example.com
```

Implement:

- Bare domains receive `http://`.
- Existing paths become absolute `file://` URLs.
- Default width is `1280`.
- Omitted height means full-page capture.
- JPEG quality 80 by default and explicit lossless PNG output.
- Speed-optimized Chrome image encoding.
- Transparent PNG backgrounds.
- Device scale factor and retina shortcut.
- Fixed viewport capture.
- Single or repeated CSS selectors.
- Repeated selectors produce their combined bounding rectangle.
- Selector-all behavior.
- JavaScript-based selectors where practical.
- Padding around selected regions.
- JavaScript before capture.
- Fixed waits and JavaScript-condition waits.
- Request and console logging.
- Reduced-motion emulation.
- Explicit light, dark, or no-preference color-scheme emulation.
- User-agent override.
- Browser arguments.
- Basic HTTP authentication.
- CSP bypass.
- Headed interactive mode.
- HTTP failure and skip policies.
- HAR recording via `--har`, `--har-zip`, and `--har-file`, matching upstream's flags on `shot`.
- Binary output through `-o -`.
- Bounded JSON capture reports with console messages, uncaught page exceptions, failed requests, final URL, status, duration, output, and errors.
- Opt-in stabilization through `--stable`: reduced motion, disabled animations and transitions, hidden carets, font and image readiness, and two paint frames.
- Stdin-triggered `--repeat` capture that reuses one browser, context, and page while keeping the URL and options fixed.
- Accept a rejected TLS certificate with `--insecure`, applied over CDP so it also works through `--cdp-url`.

Automatic filenames will receive `.1`, `.2`, and later suffixes when needed. Explicit output paths may be protected with `--no-clobber`.

### `shots javascript URL [SCRIPT]`

Implement:

- JavaScript from an argument.
- JavaScript from stdin.
- JavaScript from a local file.
- Existing `gh:user/script` convention.
- Promise resolution.
- JSON output by default.
- Raw output where applicable.
- Width and height controls.
- Navigation and execution timeout.
- Clean JavaScript exception reporting.

stdout will contain only the result.

### `shots html URL`

Implement:

- Final rendered document HTML.
- First matching element's outer HTML.
- JavaScript before extraction.
- Fixed wait and timeout.
- stdout or file output.

### `shots pdf URL`

Use CDP `Page.printToPDF` with `transferMode: ReturnAsStream`, so large PDFs stream to disk instead of round-tripping through a base64 CDP message.

Support:

- Standard paper formats.
- Custom width and height.
- Landscape orientation.
- Scale.
- Print backgrounds.
- Screen media instead of print media, via `Emulation.setEmulatedMedia` before printing.
- JavaScript and waits.
- stdout or file output.

### `shots accessibility URL`

Use Chrome's accessibility domain.

Define a stable project JSON format containing:

- Role
- Name
- Value
- Description
- Relevant states
- Relevant properties
- Ordered children

Do not expose transient Chrome node identifiers in stable output.

### `shots har URL`

Build HAR 1.2 by correlating CDP network events.

Support:

- Requests and redirects.
- Response status and headers.
- Timing information.
- Failed requests.
- Request and response bodies when available.
- Plain `.har`.
- Zipped `.har.zip`.
- Resource extraction.

Body capture strategy: `Network.getResponseBody` only succeeds while the target is alive and the body is still in Chrome's buffer. The `har` package therefore:

- Raises `maxTotalBufferSize` and `maxResourceBufferSize` on `Network.enable`.
- Fetches each body **eagerly as its `loadingFinished` event arrives**, not at flush time, so navigation cannot evict them.
- Documents that redirect responses and some cached or streamed responses never have retrievable bodies; those entries carry size metadata without content.

Extracted paths must reject absolute paths and directory traversal.

### `shots auth URL STATE_FILE`

Implement headed authentication:

1. Open the requested page.
2. Allow the user to sign in.
3. Wait for Enter.
4. Export cookies and per-origin storage.
5. Save the state file.

State file format: Playwright's `storage_state` JSON (`cookies` plus `origins[].localStorage`), so state files created by `shot-scraper auth` work with `shots` unchanged, and vice versa. This is the primary migration path for existing users and removes most of the need for a compatibility binary.

State files will use mode `0600` on Unix. Logs must never include cookies, passwords, authorization headers, or storage values.

### `shots multi CONFIG`

The YAML root will be a list. Entries may contain:

- A screenshot request
- `sh:`
- `server:`

Rules:

- Reject unknown fields, with a field path in the error (see Section 3's union-decoding strategy).
- Treat empty YAML as a successful no-op.
- Execute entries in source order.
- Reuse one Chrome process.
- Use a fresh page for each screenshot.
- Stop after a failed shell step.
- Support no-clobber and output filtering.
- Support session-wide HAR recording (`--har`, `--har-zip`, `--har-file`, matching upstream).
- Support session-wide stabilization and one JSON Lines report per selected shot.
- Wait up to 30 seconds for configured servers.
- Stop servers after success, error, timeout, or interrupt.
- Preserve the server only with `--leave-server`.

### `shots video STORYBOARD`

Retain these actions:

- `open`
- `click`
- `fill`
- `type`
- `press`
- `scroll`
- `pause`
- `wait_for`
- `wait_for_url`
- `javascript` and `js`
- `screenshot`
- `sh`

Video flow:

1. Validate the storyboard.
2. Run top-level setup.
3. Start the optional server.
4. Open Chrome and navigate.
5. Inject optional cursor and click effects (in-page, so they work under either capture mode).
6. Capture frames using the mode selected in Phase 1 (see below).
7. Execute scenes and actions in order.
8. Store timestamped frames in a temporary spool.
9. Use `ffmpeg` to encode WebM, using the concat demuxer with per-frame durations derived from timestamps.
10. Optionally encode MP4.
11. Rename completed output atomically.
12. Remove temporary frames after success.

Frame capture modes. `Page.startScreencast` delivers compressed frames at irregular intervals, only when content changes, and drops frames under load — it may not meet the quality bar for demo videos. The alternative is deterministic frame-by-frame capture with `HeadlessExperimental.beginFrame` plus virtual time, which yields full-quality frames at a chosen fps but requires headless mode. Phase 1 evaluates both against the same storyboard; the plan does not commit to screencast. If both survive the spike, screencast becomes the headed-mode path and `beginFrame` the default headless path.

If encoding fails, return status `1` and preserve the frame directory for diagnosis. Add `--keep-frames` for deliberate preservation.

### `shots browser`

Launch Chrome once for other commands to share:

```bash
shots browser > browser.url &
shots take https://example.com --cdp-url "$(cat browser.url)"
```

Implement:

- Launch with the same discovery, `--browser-path` and `--browser-arg` options as every other command.
- Print the DevTools URL, on a loopback port that is free or given by `--port`, as the only line on stdout.
- Stay in the foreground until interrupted or until Chrome exits, then close Chrome.
- No watcher, socket, pool or request protocol: Chrome is the resident process, and every other command attaches with `--cdp-url`.

## 8. Configuration contracts

Use strict YAML decoding via the `yaml.Node` union strategy in Section 3. Unknown fields, invalid types, and invalid action forms return status `2` with a field path.

Validation will cover:

- Positive viewport dimensions
- Non-negative waits
- Positive scale factors
- Valid JPEG quality
- Required scene lists
- Required video output
- Required selectors for typed actions
- Mutually exclusive flags
- Supported selector forms
- Existing input files
- Writable output locations

Configuration supplied through stdin resolves relative paths against the current directory. Configuration read from a file resolves relative paths against the configuration file's directory.

## 9. Process lifecycle

Shell command strings run through the platform shell. Argument arrays execute directly.

Process-group management: `sh:` and `server:` commands routinely spawn grandchildren (a shell running `npm start` that starts node, which starts workers). Killing only the direct child leaks the rest. Therefore:

- On Unix, every managed child starts in its own process group (`Setpgid`), and termination signals the group (negative PID).
- On Windows, every managed child is assigned to a Job Object with kill-on-close, so terminating the job terminates the tree.

Child-process cleanup:

1. Request graceful termination — `SIGTERM` to the process group on Unix. Windows console processes have no graceful-termination equivalent; the job is terminated directly, and this platform difference is documented.
2. Wait up to five seconds (Unix).
3. Force termination if necessary (`SIGKILL` to the group / `TerminateJobObject`).
4. Reap the process.
5. Report cleanup errors without hiding the original failure.

## 10. Output and error rules

- stdout is reserved for requested binary or machine-readable output.
- Progress and errors go to stderr.
- File artifacts are written to a temporary file in the destination directory.
- Completed files are renamed atomically.
- Failed commands do not leave misleading final files.
- Parent directories are not created unless explicitly documented.
- Authentication files receive restricted permissions.
- `--silent` suppresses progress but not fatal errors.

Exit codes:

- `0`: success or an intentional `--skip`
- `1`: browser, network, filesystem, process, or runtime failure
- `2`: invalid arguments or configuration

## 11. Security rules

- Never log credentials, cookies, storage values, or authorization headers.
- Treat `sh:` and arbitrary JavaScript (including `gh:` remote scripts) as trusted user code.
- Clearly document that these features are not sandboxed.
- Prevent path traversal during HAR extraction.
- Require explicit output paths for operations that could overwrite unrelated files.
- Download the managed Chrome Headless Shell only after an explicit `shots install`.
- Verify downloaded browser archives against the committed manifest checksums before extraction.
- Never invoke a system package manager.

## 12. Implementation phases

### Phase 0: bootstrap and reference contract

- Initialize Git and the Go module.
- Add Apache-2.0 licensing and upstream attribution.
- Record the upstream 1.11 command and flag matrix.
- Create the package structure.
- Add deterministic test pages.
- Configure formatting, tests, race detection, and vetting.

Exit criteria:

- `go test ./...` passes.
- `go test -race ./...` passes.
- `go vet ./...` passes.
- Compatibility decisions are documented.

### Phase 1: browser feasibility spike

Prove, against both candidate CDP libraries (chromedp and rod):

- Chrome discovery and launch.
- Remote CDP connection.
- Full-page screenshot.
- Element and combined-region screenshot.
- JavaScript promises.
- PDF generation (streamed).
- Accessibility-tree extraction.
- Network-event collection with eager body capture.
- Storage read/write across origins (auth round-trip).
- Headed interaction.
- Frame capture via CDP screencast **and** via `HeadlessExperimental.beginFrame` with virtual time.

Exit criteria:

- All probes work against the local test server.
- The CDP library is chosen and the decision recorded with rationale.
- The video frame-capture mode (or mode pair) is chosen based on measured quality and frame pacing.
- No Node process runs anywhere in the toolchain or process tree.

### Phase 2: first usable release

Implement:

- Kong command model, including the subcommand-versus-URL resolution rule
- Shared browser lifecycle
- URL normalization
- Atomic artifact writes
- `take`
- `install` (with the checksum-pinning maintainer script)
- `doctor`

Release target: `v0.1.0`

### Phase 3: page extraction

Implement:

- `javascript`
- `html`
- `pdf`
- JavaScript files and stdin
- Console logging
- Request logging

Release target: `v0.2.0`

### Phase 4: authentication and network artifacts

Implement:

- `auth` with Playwright-compatible state files
- State restoration (cookies plus per-origin storage)
- `accessibility`
- `har` command and `--har` flags on `take`
- ZIP and resource extraction

Release target: `v0.3.0`

### Phase 5: batch execution

Implement:

- Strict multi-shot YAML with union decoding and field-path errors
- Shell steps with process-group cleanup
- Server lifecycle
- No-clobber
- Output filtering
- Session HAR
- Signal-safe cleanup

Release target: `v0.4.0`

### Phase 6: video

Implement:

- Storyboard validation
- All planned browser actions
- Cursor and click effects
- Frame collection in the Phase 1-selected mode
- WebM encoding
- Optional MP4 encoding
- Diagnostic frame preservation

Release target: `v0.5.0`

### Phase 7: stabilization and release

- Compare Go behavior with upstream 1.11.
- Document intentional differences.
- Test supported operating systems.
- Finish user and migration documentation.
- Publish checksummed release archives.

Release target: `v1.0.0`

## 13. Testing strategy

### Unit tests

Cover:

- URL normalization and subcommand-collision resolution
- Filename generation
- Collision handling
- Flag validation
- YAML union decoding, field-path errors
- Storyboard normalization
- Browser discovery
- HAR event correlation and body-eviction handling
- Extraction path sanitization
- Authentication serialization (round-trip against a Playwright-produced `storage_state` fixture)
- Exit-code mapping
- Process-cleanup decisions

### Browser integration tests

Use only local deterministic pages for required CI tests:

- Redirects
- HTTP errors
- Delayed content
- Console messages
- JavaScript promises and errors
- Transparent backgrounds
- Full-page screenshots
- Selector geometry
- Authentication cookies and local storage, including multi-origin restore
- Network failures
- Structured success and failure reports, including bounded diagnostics
- Browser-generated console errors such as failed resource loads
- Explicit light and dark color-scheme emulation
- Stable capture after delayed image loading
- Stable multi-shot capture with per-entry JSON Lines reports
- Repeated capture with browser-session state retained
- Accessibility roles and states
- Storyboard interactions

### CLI subprocess tests

Verify:

- stdout and stderr separation
- Binary stdout integrity
- Exit codes
- Signal handling, including grandchild termination of a `server:` that spawns its own children
- Server termination
- Atomic output
- Authentication file permissions
- Missing browser and ffmpeg errors

### Visual tests

Pin Chrome in CI and compare:

- Exact image dimensions
- Exact selector geometry
- Stable reference pixels
- Full-image differences within a documented threshold

Golden images are stored **per operating system** (`testdata/golden/<os>/`) — font rasterization differs across platforms even with an identical Chrome build, so cross-platform goldens would push all the work onto the threshold.

Public websites may be used for manual smoke tests, but not as release-blocking tests.

## 14. Version 1.0 completion criteria

Version 1.0 is complete when:

- All planned commands run without Node anywhere in the runtime process tree.
- Browser discovery works on supported platforms.
- Screenshot behavior has deterministic integration coverage.
- YAML rejects unknown and malformed fields with field-path errors.
- HAR output validates as HAR 1.2.
- Authentication state round-trips cookies and local storage, and reads Playwright `storage_state` files.
- Browser and child process trees terminate after success, failure, timeout, and interrupt.
- Failed commands do not leave final artifacts.
- Video produces playable WebM and MP4 files.
- Every intentional difference from upstream is documented.
- Native release archives and checksums are published.
- `go test ./...`, `go test -race ./...`, and `go vet ./...` pass.

## 15. Resolved and open decisions

Resolved in this revision:

- **Releases ship a single `shots` binary.** No `shot-scraper` compatibility binary through 1.0; Playwright-compatible auth state files (Section 7) provide the practical migration path. Revisit only if users ask.
- **Auth state format**: Playwright `storage_state` JSON.
- **YAML library**: `go.yaml.in/yaml/v3` until v4 ships a stable release.
- **CDP library and video capture mode**: decided empirically in Phase 1, recorded as exit artifacts.

Still open:

1. The final Go module path. Recommended default: an organization-owned path.
