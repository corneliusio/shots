# Decisions

Deviations from PLAN.md, and choices the plan left open. Newest stage last.

## Stage 1 — Foundation

**Test pages live in `internal/testsite/pages/`, not `testdata/pages/`.**
PLAN.md Section 4 puts them under the repository-root `testdata/`. They are
embedded with `go:embed` instead, which cannot reach outside its own package
directory. Embedding means a test in any package gets the same site without
knowing where the repository root is, and without a relative-path dance in
every test binary. `testdata/golden/<os>/` was expected to stay at the root,
because golden images are compared by tests rather than served; Stage 7 below
drops it and says why.

**`config` validates values and resolves paths; it does not touch the
filesystem.** Section 8 lists "existing input files" and "writable output
locations" among the validations. Those answers go stale between validation and
use, and would make configuration parsing hit the disk, so they are enforced
where the file is actually opened or written (`artifact`, and the browser
lifecycle). `config` does own the Section 8 rule that relative paths resolve
against the configuration file's directory: `Multi.BaseDir` plus a resolved
`Shot.Output`.

**Storyboard models are deferred to the video stage.** Nothing in Stage 1
consumes them, and the storyboard-specific validations ("required scene lists",
"required video output", "required selectors for typed actions") are cheaper to
write against a real executor than to guess at now.

**The singular and plural selector keys are aliases of one field.** Upstream
accepts `selector` and `selectors` (and the three other pairs) with identical
meaning, so they decode into one `StringList` and accumulate in source order,
rather than into eight struct fields that every consumer would have to
concatenate. The strict decoder supports this with an `alias` struct tag; each
spelling is still reported by the name the file actually used.

**A multi-shot entry is one struct, not a union.** PLAN.md Section 3 describes
entries as a union type ("a screenshot request *or* `sh:` / `python:` /
`server:`"), but upstream executes each entry as independent `if`s — `sh`, then
`python`, then `server`, then a screenshot when `url` is present — and its
documentation leads with exactly that combination (`sh:` writing a file,
`output:` and `url:` capturing it). Decoding a union would reject those files.
One `Entry` struct with optional step fields matches the execution order,
deletes the entry interface and the per-step structs, and keeps what Section 3
was really buying: `yaml.Node` decoding with strict unknown-field rejection and
`entries[3].server` field paths. An entry with no step must still name a `url`,
so a plain typo is not silently a no-op; shot keys alongside a step-only entry
are ignored, as upstream ignores them.

**A configuration file holds exactly one YAML document.** A second document is
rejected with "expected a single YAML document" instead of being dropped, which
is what `yaml.Unmarshal` would do and what upstream's `yaml.safe_load` also
refuses. A leading `---` is still fine.

**YAML merge keys (`<<:`) are rejected with a message that names them.**
Per-key strict decoding bypasses the library's merge handling, and silently
half-supporting merges would be worse than saying so. Anchoring and aliasing a
whole entry still works.

**Automatic filenames match upstream except for the empty stem and trailing
hyphens.** `artifact.FilenameFor` reproduces shot-scraper's `filename_for_url`
(COMPAT.md): fold `netloc + path` on `.` and `/` to `-`, drop every character
outside `[A-Za-z0-9_-]`, trim hyphens, append the extension. Queries and
fragments are excluded because they are outside `path`. Two differences, both
in upstream's favour of the uglier name: a URL with no usable host or path
leaves upstream with an empty stem and a hidden file named `.png`, where `shots`
uses `shot.png`; and upstream right-trims hyphens *before* dropping disallowed
characters, so a path ending in punctuation keeps a trailing hyphen
(`example-com-a-.png`), where `shots` trims both ends afterwards. Neither is
worth reproducing. Collision suffixes are applied to the
stem and extension the name was built from, not re-derived from the finished
path, so a multi-part extension stays whole: `trace.har.zip` collides to
`trace.1.har.zip`, as upstream does.

**Cross-validating `quality` against the output file extension is not
implemented.** Upstream's exact rule for "JPEG quality with a non-JPEG output"
is ambiguous enough that guessing would create a difference that has to be
documented later. Only the 1-100 range is checked.

**The Phase 0 flag matrix lives in `COMPAT.md`.** Read out of the pinned
commit's `cli.py`: every command, its arguments and its flags (including the
ones applied through Click's shared-decorator helpers), the multi-shot YAML key
set, and the handful of upstream helper functions whose exact behaviour `shots`
has to reproduce. It is a lookup table, refreshed only when the pinned commit
moves.

**An entry's keys are exactly upstream's; nothing else is accepted.** An earlier
revision of `Entry` also carried `retina`, `scale_factor`, `timeout`, `skip` and
`fail`. Upstream never reads those from a shot dict — they are command-level
flags that apply to the whole run — so they are now unknown fields, which is the
whole point of strict decoding: a config that sets `retina: true` says so out
loud instead of being silently ignored. Their validations move to the flags in
Phase 2. The `javascript` + `js_file` exclusion stays, because upstream's
`_resolve_javascript` is called per entry.

## Stage 2 — Browser, `shot`, `install`, `doctor`

**The CDP library is `chromedp` + `cdproto`.** Both candidates were spiked
against the local Chrome 152 with the same five probes — launch and connect,
full-page screenshot, element clip, `await`ed promise, network events, and
screencast frames — and both passed all five with equivalent output
(full-page PNG 40,684 vs 40,688 bytes; 3 requests and 3 responses each;
31 screencast frames each in 1.5s). The probes did not separate them, so the
decision rests on three things that did:

- *Maintenance.* chromedp released v0.16.0 on 2026-07-14. rod's newest release
  is v0.116.2, from 2024-07-12 — two years without one.
- *Dependency shape.* rod pulls in `ysmood/leakless`, which carries gzipped
  supervisor binaries for five platforms inside its Go source and extracts and
  executes one at runtime, and `ysmood/fetchup`, its browser downloader. Both
  fight PLAN.md Section 11: shots downloads a browser only on an explicit
  `shots install`, against a committed checksum, and runs nothing else.
  chromedp's tree is `cdproto`, `gobwas/ws` and `go-json-experiment/json`.
- *API fit.* chromedp is `context.Context`-native, so cancellation, deadlines
  and the SIGINT path are ordinary Go. rod carries its own `Timeout`/`Context`
  wrappers and a parallel `Must*` API that panics, which is the opposite of the
  house style.

Two chromedp quirks are worked around inside `internal/browser`, both
documented at the call site:

- `chromedp.WithNewBrowserContext` cannot open a page in a fresh browser
  context on Chrome 152: `Target.createTarget` answers "Failed to open new tab -
  no browser is open" unless `newWindow` is set, which chromedp does not set.
  `browser.newTarget` issues `createBrowserContext` and `createTarget` itself
  and attaches with `WithTargetID`.
- chromedp binds the browser process and the page target to the context of
  their *first* `Run`. Wrapping that first call in a `context.WithTimeout` kills
  Chrome when the timeout fires — even on success, at the enclosing function's
  `defer cancel()`. `browser.start` therefore runs the first `Run` on the
  context itself and applies the timeout by waiting.

**The video frame-capture mode is not decided here.** PLAN.md Phase 1 pairs it
with the library choice, but nothing in Phase 2 captures frames, and choosing
between screencast and `HeadlessExperimental.beginFrame` needs a real
storyboard to measure. The one datum from the spike: `Page.startScreencast`
delivered ~20 frames per second from a page animating at 20Hz, under both
libraries.

**Selector regions are a CDP clip rectangle, not upstream's wrapper `div`.**
`shot-scraper` computes the combined bounding box in the page, appends an
absolutely positioned `div` over it, waits 300ms, and screenshots that element,
because Playwright can only capture a locator. `Page.captureScreenshot` takes a
clip directly, so `internal/browser/region.js` returns the padded rectangle and
Go passes it through. The geometry is upstream's, arithmetic included; the
wrapper element, its random id and the 300ms settle are gone, and the page is
never mutated.

**`-b/--browser` is replaced by `--browser-path` and `--cdp-url`.** Upstream's
`-b` chooses between `chromium`, `firefox`, `webkit`, `chrome` and
`chrome-beta`. PLAN.md Section 2 rules out every value but Chromium, which
leaves a flag with one legal value. PLAN.md Section 5's discovery order and
`--cdp-url` cover what remains, so those are the flags shots has.

**`--js-file` is not on `shot` yet.** PLAN.md Section 7's `shot` list says
"JavaScript before capture"; Phase 3 lists "JavaScript files and stdin"
separately, together with the `gh:` convention. Shipping a `--js-file` that
reads local paths but not `-` or `gh:` would be a worse promise than not
shipping it, so `--javascript` is the whole story until Phase 3.

**The default command is resolved before kong sees the arguments.** kong's
`default:"withargs"` implements PLAN.md Section 7's rule natively, including
"subcommand names always win", but it makes the root node resolve to `shot`, so
`shots --help` prints `shot`'s usage and never lists `install` or `doctor`.
`command.resolve` prepends `shot` when the first argument is neither a
subcommand name nor a root flag, which keeps the rule, keeps the help, and is
directly unit-testable as PLAN.md Section 13 asks.

**shots defines its own `--help` flag.** kong's built-in one claims `-h`, which
upstream spends on `--height`. `--height` wins; help is `--help` only.

**Basic authentication answers a challenge; it does not send a header.**
`--auth-username` and `--auth-password` enable the CDP `Fetch` domain with
`handleAuthRequests` and reply to `Fetch.authRequired` with the credentials,
matching Playwright's `http_credentials`. Setting an `Authorization` header
instead would leak the credentials to every origin the page touches.
Chrome reports an unanswered challenge as the navigation error
`ERR_INVALID_AUTH_CREDENTIALS` rather than returning the response, so
`Page.Navigate` prefers a main-document status it already observed over the
navigation error, and `--skip` and `--fail` see the 401.

**`--log-requests` records what Chrome received, not what Playwright decoded.**
Upstream logs on Playwright's `response` event with `size = len(response.body())`
and Playwright's `timing` object. shots logs one JSON object per request when it
finishes, with `size` set to Chrome's `encodedDataLength` — the bytes on the
wire — and `size: null` for a request that failed. Timing waits for the `har`
package, which is where the timing model belongs.

**`--log-console` prints `type: text`.** Upstream echoes Playwright's
`ConsoleMessage` repr. There is no value in reproducing another library's
`__repr__`.

**Interactive mode still applies `--skip` and `--fail`.** Upstream skips the
HTTP check whenever the page was opened for interaction. That looks like a
consequence of reusing `take_shot` with `use_existing_page`, not a decision, and
silently ignoring `--fail` because `-i` was passed would be surprising.

**`--quality 0` means "not set", as upstream's `if quality:` does.** Only 1-100
and negative values are checked; a zero cannot be distinguished from an absent
flag without making the flag a pointer, and upstream treats them the same.

**JPEG is selected by `--quality` or by a `.jpg`/`.jpeg` output extension.**
Upstream sets `type=jpeg` only for `--quality`, but then hands the path to
Playwright, which infers the format from the extension. Both paths are
reproduced explicitly.

**`NormalizeURL` also passes through `file:`, `about:` and `data:`.** Upstream
prepends `http://` to anything that is not already `http://` or `https://`, so
`shot-scraper file:///tmp/x.html` fetches `http://file:///tmp/x.html`. The
passthrough list is deliberately short: a URL is not parsed to find its scheme,
because `example.com:8080/x` parses as one.

**Selector flags do not split on commas.** kong splits repeated string flags on
commas by default, which would break `-s '#a, #b'` — a single, legal CSS
selector list. Every repeated flag uses `sep:"none"`, so repetition is the only
way to pass more than one value, as upstream requires.

**The browser manifest carries the executable path per platform.**
`internal/browser/chrome.json` records `url`, `sha256` and `binary` for each of
the five Chrome for Testing platforms. Keeping `binary` in the manifest rather
than in a Go table means a version bump that moves the executable is a data
change, and `tools/pin-chrome` verifies the path exists in the archive it just
hashed, so the manifest cannot claim a path the archive does not have.

**`$SHOTS_BROWSER_PATH` is read in exactly one place: `browser.Discover`.** An
earlier revision also put `env:"SHOTS_BROWSER_PATH"` on `--browser-path`, which
folded the variable into the explicit-path argument before discovery ran. That
made two bugs and a dead branch: exporting the variable made `--cdp-url`
impossible ("--cdp-url cannot be combined with --browser-path"), a variable
pointing at a since-deleted browser hard-failed instead of falling through to
steps 3-5 of PLAN.md Section 5's order, and `Discover`'s own step 2 became
unreachable from the CLI. The flag help names the variable instead.

**Mutually exclusive flags are checked where the flags live.** `--cdp-url`
against `--browser-path`, `--browser-arg`, `--interactive` and `--devtools` is a
Section 8 validation, and PLAN.md Section 10 gives that class exit status 2, so
the check returns `usagef` from `shotCmd.validate` and `doctorCmd.Run`, like
every other flag conflict. It used to live in `browser.Options.validate`, which
returned a plain error (status 1) and hardcoded CLI flag spellings inside the
browser package. `doctor` repeats the one condition it can violate rather than
embedding `browserFlags`, whose other seven flags it does not accept.

**Missing Linux shared libraries are reported by running `ldd`.**
`browser.MissingLibraries` returns the libraries `ldd` marks "not found", and
nothing at all off Linux or where `ldd` is absent. `shots install` fails with
them once extraction succeeds, and `doctor` reports them as a problem: a Chrome
for Testing archive carries no system dependencies, so both commands can
otherwise report success on a bare container and leave the first launch to fail
with an opaque CDP timeout. Only `ldd` is consulted; a library present but too
old is left to Chrome to complain about.

**`doctor` writes its report to stdout and exits 1 on any problem.** The report
is what the command was asked for, which is what PLAN.md Section 10 reserves
stdout for. A missing `ffmpeg` or Python is reported with what it would have
been needed for and is not a problem: neither is required for `shot`.

**One lock guards both output streams, so callbacks can just write.** CDP event
callbacks run on the connection's read goroutine while the main goroutine writes
progress lines, prompts and image data, and the two streams are frequently the
same file or, in tests, the same buffer. `env` therefore wraps stdout and stderr
in a `syncWriter` pair over a single mutex, and every writer -- kong, `progress`,
`artifact.Write`, `--log-console`, `--log-requests -` -- goes through it. That
replaced a per-callback mutex in `requestLog`, which serialized the request log
only against itself and left `--log-console` and the image on stdout unguarded.
`requestLog` now keeps only the file closer: `json.Encoder` emits a record per
`Write`, and an `*os.File` rejects a write that arrives after `Close` rather than
racing it, so its `bufio.Writer` and flush-under-lock went away too.

## Stage 3 — `javascript`, `html`, `pdf`, `accessibility`

**One `pageFlags` struct carries the shared lifecycle, so every page command
takes the same options.** PLAN.md Section 6 describes one sequence, and five
commands now run it: connect, open a page in a fresh context, navigate, apply
`--skip` and `--fail`, prompt when headed, wait, run script, wait for a
condition. `pageFlags.open` is that sequence, `pageFlags.emit` is the write and
the progress line, and `shot` lost its copy of all of it. The price is that
every page command accepts flags upstream spreads unevenly: `--wait` and
`--wait-for` on `javascript` and `accessibility`, `--silent` on `javascript`,
`-i/--interactive` and `--devtools` everywhere. A shared lifecycle with one flag
set is smaller than four almost-identical ones, and none of the additions is
surprising — waiting before extracting HTML is the same idea as waiting before a
screenshot.

**A before script and an evaluated script are two entry points, not one.**
Merging them into a single `Page.Eval` made the shared lifecycle ask Chrome to
serialize a value nobody reads, so `-j "…; window"` — or any script ending in a
DOM node or a cyclic object — failed the whole command on `shot`, `html`, `pdf`
and `accessibility`. `Page.Exec` now runs the same two steps without
`returnByValue`, `Page.Eval` keeps it for `shots javascript`, whose result is
the artifact, and both are two lines over one unexported `evaluate`. There are
two steps because there have to be: the first evaluation cannot ask for the
value by value, or a source that evaluates to a function would come back as a
description instead of a handle to call. Whatever comes back with a handle — a
function to call, an object to serialize — is resolved by a second call, with
promises awaited at both steps.

**`-i` is `--interactive`, and the script file flag is `--js-file` on every
command.** Upstream spends `-i` on `javascript --input`, which is that command's
script file, while every other command spells the same idea `--js-file`. One
spelling everywhere is worth more than the shorthand, so `--js-file` reads the
script and `-i` keeps the meaning it has on `shot`.

**`shots javascript` takes its script from the argument, then `--javascript` or
`--js-file`, then standard input.** Upstream reads the positional argument or
`--input`, whose default is `-`. Rather than a second file flag with a default
that hides standard input, the fallback is explicit: nothing on the command line
means the script is read from standard input, and giving both an argument and
`--javascript` is a usage error rather than a silent preference.

**`--js-file` is now on every page command, with the `gh:` convention.**
Stage 2 deferred it because a `--js-file` without `-` and `gh:` would be a worse
promise than none. `gh:user/script.js` expands to
`raw.githubusercontent.com/user/shot-scraper-scripts/main/script.js`, and
`gh:user/repo/path/script.js` to that repository, with `.js` appended when it is
missing — upstream's rule. Remote scripts are user-trusted code that nothing
sandboxes, as PLAN.md Section 11 says.

**Results print as re-indented JSON, not re-encoded JSON.** `json.Indent` over
Chrome's own bytes keeps object keys in the order the page produced them;
decoding into a map would sort them alphabetically. `undefined` prints as
`null`, matching upstream's `json.dumps(None)`. `--raw` unquotes a string result
and leaves everything else as JSON, where upstream would print a Python `repr`.
Text artifacts — the JSON result, HTML, the accessibility tree — end in a
newline, so they are text files whether they went to a terminal or to disk.

**`shots html` emits Playwright's `page.content()`, and a selector gives outer
HTML.** The document is the doctype, when there is one, then
`document.documentElement.outerHTML`, with a newline between them. PLAN.md
Section 7 asks for the "first matching element's outer HTML", so a selector that
matches nothing is an error naming the selector, not an empty file.

**PDF paper is Playwright's table and Playwright's units.** CDP takes inches
only, so the format names (`Letter` through `A6`) are inch pairs here, and
`--width`/`--height` accept `in`, `cm`, `mm` and `px`, with a bare number
meaning pixels as it does in CSS. Margins are always zero, which is Playwright's
default and therefore what upstream's PDFs look like; no margin flag exists
until somebody asks for one. `--format` together with `--width` or `--height` is
a usage error, as upstream's is.

**The PDF stream is read with a direct `IO.read`.** `PrintToPDF` runs with
`transferMode: ReturnAsStream` as PLAN.md Section 7 requires, and
`browser.stream` reads it a chunk at a time into `artifact.WriteStream`, so a
large PDF never sits in memory. cdproto's generated `IO.read` helper drops the
`base64Encoded` flag from the response, which a PDF stream always sets, so the
command is issued through `cdp.Execute` and the flag is honoured.

**`--media-screen` is emulated for the whole page load, not just for printing.**
`Emulation.setEmulatedMedia` is part of configuring the page, next to
`--reduced-motion`, because a second call replaces the first: one command
carries the media type and the media features together. A page therefore lays
out in screen media from the first paint, rather than switching underneath the
printer.

**The accessibility format drops what is not stable.** Chrome's tree is filtered
into role, name, value, description, states, properties and ordered children:

- Nodes Chrome marks ignored are dropped and their children take their place, so
  an `aria-hidden` wrapper does not shape the output.
- `InlineTextBox` nodes are dropped. They repeat their parent's text, split by
  rendered line, so they change with the viewport — the opposite of stable.
- A property that is on becomes a state (`focusable`, `pressed`, `checked`), one
  that is off is left out entirely, and anything else keeps its value, so
  `aria-checked="mixed"` stays a property with the value `"mixed"`.
- Properties whose values are node references (`labelledby`, `controls`,
  `owns`, …) are dropped: their values are exactly the transient Chrome node
  identifiers PLAN.md Section 7 forbids. What they contribute is already in the
  resolved `name` and `description`.

**`--no-clobber` stays on `shot`.** PLAN.md Section 7 attaches it to
screenshots, whose automatic filenames make an accidental overwrite plausible.
The new commands write to standard output by default, and `pdf` picks a
collision-free automatic name, so there is nothing to protect that the user did
not name explicitly.

## Stage 4 — `har`, `auth` and state restore

**`browser` imports `har`, not the other way round.** The recorder needs values
a CDP event carries, and PLAN.md Section 4 forbids CDP types in command
handlers. Defining a second, neutral event vocabulary in `har` and translating
CDP into it inside `browser` would have meant two shapes for the same data, so
`har` owns the HAR 1.2 model and the correlation, and `browser` converts CDP
events straight into `har.Request`, `har.Response` and `har.Timings`. The
DevTools Protocol still stops at the package boundary, and the archive format
is testable without a browser.

**Bodies are fetched eagerly, and the flush waits for them.**
`Network.getResponseBody` only answers while the body is still in Chrome's
buffer, so `loadingFinished` starts the fetch immediately, on a goroutine
(the listener runs on the connection's read goroutine and would deadlock).
`Network.enable` raises `maxTotalBufferSize` to 200MB and
`maxResourceBufferSize` to 50MB when a HAR is being recorded, and `FlushHAR`
waits for the fetches in flight. Redirect responses, some cached responses and
bodies Chrome has already evicted have none; those entries keep their status,
headers and sizes, and no `content.text`.

**An entry's `receive` is the remainder, and `blocked` is never observed.**
Chrome's `ResourceTiming` measures up to the last response header; the phases
before it map directly (`dns`, `connect`, `ssl`, `send`, `wait`), and what is
left of the elapsed time is the body, which is exactly what `receive` means.
`time` is then the sum of the non-negative timings, as the specification
requires. `blocked` has no CDP equivalent that is worth guessing at, so it
stays -1.

**Requests still in flight are closed when the log is taken.** A page that
calls `fetch()` and never reads the response body leaves the request open for
as long as it likes, and an entry with no duration is not a valid HAR entry.
`Recorder.Log` closes whatever is open at that moment, which is also what makes
the call idempotent.

**HAR cookie arrays stay empty, and `pages` is not written.** Both are optional
in HAR 1.2. The `Cookie` and `Set-Cookie` headers are already in `headers`, so
parsing them into a second representation would add code and duplicate
credentials rather than information; a `pages` array would need a page timeline
`shots` does not model. Every required array is written as `[]`, never `null`.

**A `.har.zip` holds one `har.har` member with the bodies inline.** Playwright
stores each body as a separate zip member and points at it with a `_file`
reference; shots' archive is the same bytes the plain `.har` would have, just
compressed. Upstream's extractor reads `content.text` as well as `_file`, so
its archives stay readable there, and shots' own extractor has one code path
instead of two. Nothing in shots writes or reads `_file`, so nothing implements
it.

**Extracted names are checked even though they cannot escape.**
`artifact.Stem` already folds a URL to `[A-Za-z0-9_-]`, so a `..` or a `/` in a
request path cannot survive into a filename. `safeName` rejects one anyway,
before the name is joined to the extraction directory: PLAN.md Section 11 asks
for the guard, the name comes from a URL the page chose, and a future change to
the naming rule should fail the test rather than write outside the directory.

**Resource naming drops upstream's second content-type table.**
`filename_for_har_entry` compares the URL's extension against a table mapping
extensions back to a single canonical content type, so a `.js` file served as
`text/javascript` (rather than `application/javascript`) is named
`site-app-js.js`. shots compares the extensions themselves, giving
`site-app.js`, and keeps upstream's behaviour everywhere else: a mismatched
extension stays in the stem, an unknown type falls back to the URL's extension,
and neither leaves `.bin`.

**Authentication state is `browser.State`, in Playwright's `storage_state`
shape.** Cookies come from `Storage.getCookies` for the page's browser context.
`sameSite` is omitted when Chrome reports none — Playwright's own type makes it
optional — and a session cookie keeps Playwright's `-1` expiry, which restores
as no expiry at all. A round-trip against a `storage_state` fixture is
byte-for-byte, so a state file loses nothing passing through shots.

**Storage is exported for every origin the page visited, not just the ones it
still frames.** Playwright accumulates visited origins as navigations commit,
and a sign-in routinely leaves its token on an identity provider before
redirecting to the application, so an export limited to the final page writes a
state file that does not restore the session. `Page` collects the security
origin of every `Page.frameNavigated`, which covers redirect hops and
cross-origin iframes alike. `DOMStorage.getDOMStorageItems` answers only for an
origin that still has a live frame ("Frame not found for the given storage id"),
so each origin is read the same way it is written: through a page opened on it.

**Export and restore share one intercepted blank page per origin.** PLAN.md
Section 6 prescribes the page per origin for restore, and export needs the same
thing, so `Browser.onOrigin` opens it, answers its request with
`Fetch.fulfillRequest` instead of fetching it, runs one script and closes it.
Neither direction touches the network, both work for an origin that is offline,
and the pages live in the browser context the command will use, closed before it
navigates. Values are passed to the page as a JSON literal, never spliced into
source, and never logged.

**`-a/--auth` takes a path, not `-`.** Upstream's is a Click file argument, so
`-a -` reads the state from standard input — the same stream `--js-file -` and
the `--interactive` prompt read. `shots auth` still writes to `-`.

**`shots auth` reports nothing but the prompt.** Upstream prints no progress
line, and adding one would have meant a `--silent` flag upstream does not have
on this command. The file's mode is 0600, and neither the prompt nor any error
carries a cookie, a storage value or a password.

**The auth tests run the sign-in browser headless with `--browser-arg
headless=new`.** `shots auth` is headed by definition, which no CI runner can
do. A later `--browser-arg` overrides the flag shots sets, so the tests drive
the real command — prompt, export, permissions and all — without a display.

**A skipped or failed page writes no HAR.** Upstream records the archive
through Playwright's browser context, so it lands even when `--skip` stopped
the run before the screenshot. In shots the HAR is written by the command that
asked for it, after the artifact it belongs to, which keeps PLAN.md Section
10's rule that a failed command leaves no files behind. A run that ends early
therefore leaves the previous HAR — if any — untouched instead of replacing it
with a partial one.

**Taking a recorder's log stops it.** `Recorder.Log` hands out the recorder's
own `*Entry` values, and the page keeps reporting events while the caller
encodes them: `FlushHAR` only waits for the body fetches it started, and Chrome
delivers later events on the connection's read goroutine. Rather than deep-copy
every entry, `Log` sets a `closed` flag that drops a later `Sent`, and the
closing methods already act only on an entry they find in `open`, which `Log`
empties. `Log` stays repeatable, which the `har -x` path needs.

**A HAR command resolves zip once, from the flag or the extension.** `har`
decided plain-versus-zipped from the output filename alone, so `-z -o out.har`
wrote an uncompressed file while `--har-zip` on `shot` honoured the flag.
`harCmd.output` now returns the destination and whether it is zipped, the way
`harFlags.path` already did, and the two entry points agree.

## Stage 5 — `multi`, steps and process lifecycle

**Server readiness is a TCP dial of the page the server was started for.**
PLAN.md Section 7 asks for a 30-second readiness wait, but a configuration
never names a port, and upstream simply starts the process and moves on.
`serverAddress` takes the host and port of the first `http`/`https` URL at or
after the `server:` entry — the canonical file is `- server: …` followed by
`- url: http://localhost:8000/` — and `Server.Ready` dials it until it answers,
fails immediately if the server exits first, and gives up after 30 seconds. A
configuration with no URL after its server gets no wait: there is nothing to
dial, and inventing a fixed sleep would be worse than the honest nothing.

The search runs each URL through `NormalizeURL` first, so `url: localhost:8000/`
is waited for exactly like `url: http://localhost:8000/`; whether a server is
waited for must not depend on a spelling shots itself normalizes away. It also
stops at the next `server:` entry, because the URLs beyond it belong to a server
that has not been started yet — `start everything, then shoot` is a normal
layout, and scanning past would make the first server wait 30 seconds for a port
only the second one will ever open.

**A failed step stops the batch; a failed shot is reported and the batch carries
on.** That split is PLAN.md Section 7's ("stop after a failed shell or Python
step") and upstream's: everything after a step was written expecting it to have
run, while one unreachable page among fifty is not a reason to abandon the other
forty-nine. `--fail-on-error` stops at the first failing shot, as upstream's
does. One difference from upstream: a run with failed shots exits 1 with a
`1 of 2 entries failed` summary, where upstream reports each failure and then
exits 0. A batch that quietly succeeds while writing nothing is the wrong
default for a scheduled job.

**Steps run in the configuration file's directory, and their output goes to
standard error.** Upstream's `subprocess.run` inherits the caller's directory
and streams. Resolving against the configuration instead makes a step consistent
with every other path in the file — `output:` and `js_file:` already resolve
there — so `shots multi site/shots.yml` behaves the same from any directory. A
configuration read from standard input keeps the current directory, per PLAN.md
Section 8. Sending a step's own standard output to shots' standard error is
PLAN.md Section 10: stdout carries artifacts, and a `sh:` step's chatter is
progress.

**A batch's HAR is written after the last entry, not after each one.** One
recorder spans every page, so `--har` is a session archive as PLAN.md Section 7
requires. It is written when the batch reaches the end, including a batch with
failed shots in it, and not at all when a failed step aborted the run — the same
rule Stage 4 applied to a single page: an archive describes a run that
finished. An entry with no `output` still loads its page while a HAR is being
recorded, and writes no image; that is what upstream's injected `skip_shot` key
does.

**`--output` filters resolve the way the configuration's own paths do.**
`Multi.Resolve` puts a filter through the same base directory as `output:`, so
`-o one.png` matches `output: one.png` wherever the configuration lives, rather
than matching only when it happens to sit in the current directory.

**Python resolution is an ordered candidate list, like browser discovery.**
`$SHOTS_PYTHON`, then `python3`, then `python`, each accepted only if
`exec.LookPath` finds it; a variable pointing at something that is no longer
there falls through instead of failing the run, exactly as `$SHOTS_BROWSER_PATH`
does in `browser.Discover`. The error naming all three candidates can only
appear while running a `python:` step, so a machine without Python runs
everything else unaffected.

**`pageFlags` split into `sessionFlags` and `pageFlags`.** The options that
describe a whole run — which browser, which authentication state, `--timeout`,
`--skip`/`--fail`, `--log-console`, `--silent` — are `sessionFlags`, which
`multi` embeds; the ones that describe one page — `--javascript`, `--js-file`,
`--wait`, `--wait-for`, `-i`, `--devtools` — stay in `pageFlags`, because a
multi entry carries its own. `multi` therefore accepts `--bypass-csp`, which
upstream has only on the page commands; one browser-flag set everywhere is worth
more than reproducing that gap.

**Every managed child gets its own process group, and cleanup is one path.**
`process.Server` is the whole lifecycle: `Start` puts the child in a new process
group (a job object on Windows), `Stop` signals the group, waits five seconds,
kills the group, and reaps. `Run` is `Start` plus a wait, so a `sh:` step that is
still running when the run is interrupted is torn down by the same code that
stops a server. Signalling the group rather than the process is the point: a
`server:` running `sh -c 'npm start'` is a shell whose children are the actual
server, and a direct kill would leave them running — which the interrupt test
asserts by watching a grandchild's process id.

**A step's group is stopped when the step succeeds, too.** `sh: something &`
leaves the backgrounded process in the step's group, and returning without
signalling it is exactly the leak the groups exist to prevent. `Run` therefore
waits for the child, then runs the same `Stop` the cancelled path runs. Upstream
leaves such a process running; a batch tool that quietly accumulates orphans is
the worse behaviour, and `server:` is how a configuration says "keep this
running".

**`--leave-server` decides where the server writes, and detaches it.** Not
signalling the group is not enough on either platform. On Windows the job object
carries `KILL_ON_JOB_CLOSE`, and the handle is closed by the OS when shots
exits, so `Server.Detach` clears the job's limit flags before releasing the
handle; on Unix the group does outlive shots, but the *output* does not. Every
managed child writes to a pipe shots owns (see below), whose read end closes at
exit, so the first line a left-behind server logs kills it with `SIGPIPE` —
which is every server worth leaving: `npm run dev`, `python -m http.server`,
anything with a request log. A server started under `--leave-server` therefore
gets shots' own standard error *file* as its output, which os/exec hands to the
child as a descriptor of its own; `process.Cmd.Output` treats any `*os.File`
that way, and everything else as a writer to copy through a pipe. `Detach` is
still a no-op on Unix. The Windows half is compiled and vetted here but
exercised only by CI's Windows runner, and the Unix half is covered by a
subprocess test whose server logs every request and is asked for two pages after
shots has exited.

**Windows terminates the job outright; there is no graceful phase there.**
PLAN.md Section 9 anticipates this: console processes have no SIGTERM
equivalent, so `terminate` and `kill` are both `TerminateJobObject`, the
five-second grace never elapses, and kill-on-close means an abrupt end to shots
takes the tree with it. The implementation needs `golang.org/x/sys/windows`,
which was already in the module graph through chromedp and is now a direct
dependency; it is compiled and vetted for Windows here but exercised only by
CI's Windows runner.

**The child's output pipe belongs to `process`, not to `os/exec`.** A child's
output goes through a pipe that anything it starts inherits, and `os/exec`'s
`Wait` waits for every copy of the write end to close — so a step that
backgrounds a process would not be reaped until that process exited, five
seconds later with `WaitDelay` and never without. `Start` therefore hands the
child an `os.Pipe` it owns and copies from it in a goroutine, which leaves
`Wait` waiting for nothing but the process. Two signals come out of that: the
child has exited, and the group has released the pipe. Cleanup waits for the
second, bounded by `Grace`, which is what makes the grace period cover the group
rather than only the direct child.

**The CLI subprocess tests build the real binary, and the test binary plays the
children.** Signal handling lives in `cmd/shots/main.go`; a test that installed
its own would test the copy, so `go build` runs once per package test run and
the tests drive the built binary. In the other direction, the processes a
configuration starts are the test binary itself in a `-child` mode — print,
write, exit, serve — which keeps the fixtures free of shell scripts and of
anything that has to exist on the machine. Anything about what survives shots'
exit has to be a subprocess test: in-process, the pipe a child inherited stays
open for as long as the test binary does, so the bug the test is looking for
cannot happen.

**A multi run is one browser context, not one per entry.** `batch` creates one
context for the whole run — when it does is the Stage 7 entry's story — and
opens a fresh page in it per shot — PLAN.md Section 7's "one Chrome process, a fresh
page for each screenshot", and upstream's shape, where one context spans the run
and `new_page()` is called per shot. It matters twice: a configuration that
signs in or writes `localStorage` in one entry and captures the result in the
next works, as it does upstream; and `--auth` is restored once per run rather
than once per entry, which had been opening and closing a blank page for every
origin in the state file before every single shot. `browser.Context` is the
addition that makes it sayable — `Browser.NewContext` creates the context and
restores state into it, `Context.NewPage` opens a page in it, `Page.Close` now
closes only the page — and the single-page commands say the same three steps in
`pageFlags.open`.

**`doctor` asks `process.Python()` rather than repeating the order.** The report
used its own copy of PLAN.md Section 9's `$SHOTS_PYTHON` → `python3` → `python`
rule, and the copies had already drifted: with `$SHOTS_PYTHON` pointing at a
moved interpreter, `doctor` printed "python: not found" while `multi` ran
`python3` happily. One resolver, asked by both, is what makes the diagnostic
worth reading. `checkTool` stays for `ffmpeg`, which has no resolution order.

## Stage 6 — `video` and storyboards

**The frame-capture mode is `Page.startScreencast`, because the alternative no
longer exists.** PLAN.md Phase 1 asked for both candidates to be measured
against a real storyboard. Against Chrome 152:

- `HeadlessExperimental.beginFrame` answers `'HeadlessExperimental.beginFrame'
  wasn't found (-32601)`. The domain went away with the old headless mode, and
  there is no virtual-time frame stepping to fall back on. The plan's preferred
  default is not available at all, so the "screencast for headed, beginFrame for
  headless" split has nothing to split.
- `Page.startScreencast` delivered 179 PNG frames in 3.0s (59.6 fps, longest gap
  20ms) from a page animating at 60Hz, and one frame in 3s from a page that did
  not change — it sends a frame when the surface changes, which is exactly what
  a spool with per-frame durations wants.
- A third option, polling `Page.captureScreenshot`, was measured because it is
  the one that gives exact dimensions for free: 51 frames in 3.0s at 1280x720,
  17 fps. That is below the rate a demo video needs, and every frame is a
  synchronous paint the page under test has to wait for.

So screencast it is, in headless, which is the only mode `video` runs in.

**The window is what a screencast captures, so the window is what gets sized.**
Frames come out at the rendered surface's size, not the emulated viewport's:
`--window-size=1280,720` with `Emulation.setDeviceMetricsOverride(1280, 720)`
still gave 1280x577 frames on macOS, because the window is 143px taller than its
content there. `Page.Fit` therefore measures `innerWidth`/`innerHeight` and grows
the window by the difference through `Browser.setWindowBounds`; one adjustment
reaches 1280x720 exactly, and the video needs no scaling or cropping.

`Fit` reports the size it reached rather than failing when it cannot: Chrome
enforces a minimum window width (500 on macOS), so a storyboard asking for a
320-wide viewport gets 500 and a line on stderr saying so. Recording at the size
the window actually is beats refusing to record.

**`internal/video` is the spool and the encoder; the storyboard runs in
`internal/command/video.go`.** PLAN.md Section 4 gives `video/` "storyboard
execution and screencast processing". Execution needs `NormalizeURL`, which
lives in `command`, and `command` imports `video` — so the split as written is a
cycle unless URL normalization moves to a third package for one caller. Putting
the run loop next to `multi`'s, which is the same shape (steps, a server, a
browser held across the run), keeps one place to look for batch orchestration,
and leaves `video` a package that turns timestamped frames into a file and can
be tested without a browser at all.

**A storyboard action is one struct with a `Kind`, driven by a table.** Thirteen
action types as a Go union would be thirteen structs, an interface, and a type
switch that ends up being a switch on the name anyway. `config.Action` carries
the union of the fields, and `actionSpecs` maps each action name to the keys its
mapping form accepts and the key its scalar shorthand fills — so `click: "#go"`
and `click: {selector: "#go", button: right}` decode through one path, and
`click: {key: Enter}` is rejected as an unknown field because `key` is not in
`click`'s list. `decodeStruct` grew one variadic argument to do it.

**Nested values report their own field path, and the parent roots it.**
`scenes[0].do[2].click.selector` is composed rather than threaded: `Scene`,
`Action`, `Viewport` and `Cursor` are `yaml.Unmarshaler`s that report paths
relative to themselves, `Scenes` and `Actions` are named slice types that prefix
`[i]`, and `decodeField` re-roots any `*config.Error` that comes back onto the
key it was decoding. yaml.v3 returns a custom unmarshaler's error verbatim, which
is what makes this work.

**`cursor: false` decodes to a cursor with nothing turned on.** A
`yaml.Unmarshaler` cannot set its own pointer to nil, and upstream's condition is
`if cursor and (visible or clicks)` — so `Cursor.Enabled()` is that condition,
and absent, `false` and `{visible: false, clicks: false}` all mean the same
thing without a second representation.

**One ffmpeg run writes both files, from the frames.** Upstream converts the
recorded WebM to MP4 with a second ffmpeg pass. Reading the spool once and
encoding two outputs from it costs one process, one decode, and no second
generation of loss. The WebM is VP9 (`-crf 32 -b:v 0`), the MP4 is H.264
(`-crf 23 -preset veryfast -movflags +faststart`), both `yuv420p` at 30fps.

`-xerror` is on, because ffmpeg's default is to skip a frame it cannot read and
exit 0: a video missing half its frames has to fail, not ship. Both outputs are
written to a temporary file in the destination directory and renamed, so a
failed encode leaves neither.

A viewport with an odd dimension is padded to the next even one rather than
rescaled — `yuv420p` has no other option, and a pixel of padding keeps every
frame's own pixels where they are.

**ffmpeg is required for any video, not only for `--mp4`.** Upstream gets its
WebM from Playwright and needs ffmpeg only for the MP4 conversion, so its
documented behaviour is "the WebM is still created but the command exits
non-zero". Here the encoder is the whole output path, so `video.RequireFFmpeg`
runs in `videoCmd.Run` beside the `output:` checks — before the setup steps, the
server, Chrome and every scene, because a run that cannot encode must not first
execute a storyboard's side effects and throw minutes of recording away. That is
the only lookup; `Encode` does not repeat it. `doctor` already names ffmpeg too.

**Frames are spooled beside the output and removed on success.** The spool is
`shots-frames-*` in the video's own directory, so it is on the same filesystem
as the output and obvious when it is left behind. It survives a failed encode
(with the path in the error) and `--keep-frames`, which is a shots flag upstream
does not have; PLAN.md Section 7 asks for both.

**Selectors are CSS, and `wait_for_url` is a glob.** Upstream passes `wait_for`
to a Playwright locator, so `text=Quick start` and `h1:has-text("Installation")`
work there. PLAN.md Section 2 rules out Playwright selector compatibility, so
`wait_for`, `click`, `type`, `fill`, `press` and `scroll: {to:}` take a CSS
selector and nothing else. `wait_for_url` compiles a glob to a regular
expression — `**` for anything, `*` and `?` stopping at a slash — which is the
part of Playwright's URL matching a storyboard actually uses.

**Actions dispatch real input events, not synthetic DOM ones.** A click resolves
the element's centre in the page, dispatches `mouseMoved` there and only then
presses, so the injected cursor travels to the element instead of appearing on
top of it. `type` sends one key event per rune through chromedp's keyboard
tables, `delay_ms` apart, because a video of text appearing all at once is not a
video of typing. `press` accepts every DOM key name Chrome knows plus modifier
combinations (`Shift+Tab`, `ControlOrMeta+A`, which is Meta on macOS and Control
elsewhere); a held modifier suppresses the character event, because Control+A
selects rather than inserting an "a". `fill` goes through the native `value`
setter and then fires `input` and `change`, which is what a framework watching
the property waits for.

**`shots video` has no `--output -`.** A WebM is a container ffmpeg seeks
around while writing; there is nothing useful to stream. `-o -` is a usage
error, and `--output` otherwise resolves against the current directory while
the storyboard's own `output:` resolves against the storyboard, as every other
path in a configuration file does.

## Stage 7 — conformance sweep, documentation and release

**A multi run acquires its browser once, before the first entry.** `batch.page`
used to connect lazily and retry per entry, which had three consequences: a
`NewContext` failure left `b.bctx` nil, so the next entry called
`browser.Connect` again and overwrote `b.browser` — orphaning the previous
Chrome and its temporary profile, since `batch.close` only closes the handle it
still has, and `main` ends in `os.Exit` so no deferred allocator cancel runs
either; one unreachable browser cost one `--timeout` per entry (25 minutes for a
50-entry configuration at the 30s default) and was reported as "50 of 50 entries
failed" rather than as the one real cause; and it contradicted PLAN.md Section
7's "reuse one Chrome process". `batch.start` now runs before the loop, and only
when `slices.ContainsFunc(entries, b.shoots)` finds a shot the `--output` filter
kept — so Stage 5's "an empty or step-only configuration never launches a
browser" still holds, verified by running a `sh:`-only configuration and
counting `chromedp-runner` processes. A launch or context failure ends the
command immediately with the real error, as upstream's does. The lazy branch is
deleted rather than guarded, and `wanted` folded into `shoots`.

**A recording waits for its first frame.** `Page.Record` issued
`Page.startScreencast` and returned; Chrome answers that command before it
composites anything, so a storyboard whose scenes finished first encoded nothing
and `Encode` failed with "no frames were captured" — reproducibly, for a
storyboard over a static `file://` page whose only action is `pause: 0`, and
intermittently for `TestVideoShellSteps`. `Record` now wraps the caller's
callback in one that closes a `first` channel, and returns only once a frame has
been delivered, the context is cancelled, or the session timeout elapses. A
recording therefore always has a frame in it, and the playlist a sane start.
`StopRecording` issues `Page.stopScreencast` before clearing `onFrame`, so a
frame Chrome had already emitted still reaches the spool instead of being
dropped between the two statements.

**A storyboard `screenshot:` picks its format from the extension.** It built
`browser.ShotOptions` with only `FullPage` and `Clip`, so `screenshot: frame.jpg`
wrote PNG bytes into a `.jpg` file, while the same idiom in a multi-shot
configuration produced a real JPEG through `isJPEGPath`. Stage 2 records
extension-based selection as deliberately reproduced upstream behaviour, so the
two paths disagreed on a documented rule of shots' own. `recording.screenshot`
now calls the same helper. No `quality` key is added to `screenshot`: the action
has no field for it, and the extension alone is what the rule is about.

**Per-OS golden images are dropped; the visual assertions are structural.**
PLAN.md Section 13 asks for `testdata/golden/<os>/` and a thresholded full-image
diff. What that buys over what `shot_test.go` already asserts — exact dimensions
and encoded format through `image.DecodeConfig`, selector geometry, and
reference pixels decoded with `png.Decode` for transparency and background — is
a whole-image comparison whose only failure mode in practice is font
rasterization, which is why the plan already needed one directory per operating
system. Making that pass needs a threshold tuned until it accepts every renderer
difference, at which point it accepts most regressions too, plus a Chrome pinned
in CI on three platforms and three sets of images regenerated on every bump.
The structural assertions catch what a screenshot tool actually breaks — wrong
size, wrong format, wrong crop, lost transparency — and cost nothing to
maintain. `testdata/` therefore does not exist; the deterministic pages live in
`internal/testsite/pages/`, as Stage 1 records.

**`internal/app/` does not exist.** PLAN.md Section 4 lists it as "application
orchestration". `command` is that orchestration — `pageFlags.open` is the shared
lifecycle, `batch` and `recording` are the two batch runners — and a package
that only forwards between `cmd/shots` and `command` would be a name with no
behaviour behind it.

**Releases are a tag-triggered workflow, not GoReleaser.**
`.github/workflows/release.yml` cross-builds the six `darwin`/`linux`/`windows`
by `amd64`/`arm64` targets with `CGO_ENABLED=0 -trimpath`, stamps the tag into
`command.Version` with `-ldflags -X`, archives each (`.tar.gz`, `.zip` for
Windows), writes one `SHA256SUMS`, and attaches everything to the GitHub
release — PLAN.md Section 12 Phase 7 and Section 14. Cross-compiling from one
runner is enough because shots is pure Go: the only platform-specific code is
build-tagged (`process/unix.go`, `process/windows.go`), and CI already runs the
tests on all three operating systems. A release tool would add a dependency and
a configuration file to do the same twenty lines of shell.
