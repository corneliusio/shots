# Decisions

Deviations from PLAN.md, and choices the plan left open. Newest stage last.

## Stage 1 — Foundation

**Test pages live in `internal/testsite/pages/`, not `testdata/pages/`.**
PLAN.md Section 4 puts them under the repository-root `testdata/`. They are
embedded with `go:embed` instead, which cannot reach outside its own package
directory. Embedding means a test in any package gets the same site without
knowing where the repository root is, and without a relative-path dance in
every test binary. `testdata/golden/<os>/` still belongs at the root, because
golden images are compared by tests, not served.

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
