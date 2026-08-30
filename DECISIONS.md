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
