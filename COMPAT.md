# Upstream compatibility reference

The surface of `shot-scraper` 1.11, at commit
`177f5abb32add7622d5152eb7064a2ce47dd2fbf`, read out of `shot_scraper/cli.py`.

This is a lookup table, not a commitment: `shots` is a rewrite, and PLAN.md
Section 2 already rules out Firefox, WebKit and Playwright selector parity.
Where `shots` deviates, the reason belongs in DECISIONS.md. Refresh this file
only when the pinned commit moves.

Options declared through shared decorators (`browser_option`,
`skip_fail_options`, …) are listed inline on every command that applies them.
`-b/--browser` accepts `chromium`, `firefox`, `webkit`, `chrome` and
`chrome-beta`; `shots` supports Chromium only.

## Commands

`shot` is the default command, and runs when no arguments are given at all.

### `shot-scraper shot URL`

`-a, --auth`, `-w, --width` (default 1280), `-h, --height`, `-o, --output`,
`-s, --selector` (repeatable), `--selector-all` (repeatable), `--js-selector`
(repeatable), `--js-selector-all` (repeatable), `-p, --padding`,
`-j, --javascript`, `--js-file`, `--retina` (flag), `--scale-factor`,
`--omit-background` (flag), `--quality`, `--wait`, `--wait-for`, `--timeout`,
`-i, --interactive` (flag), `--devtools` (flag), `--log-requests`,
`--log-console` (flag), `-b, --browser`, `--browser-arg` (repeatable),
`--user-agent`, `--reduced-motion` (flag), `--skip` (flag), `--fail` (flag),
`--bypass-csp` (flag), `--silent` (flag), `--auth-username`, `--auth-password`

PLAN.md Section 7 also puts `--har`, `--har-zip` and `--har-file` on `shots
shot`; upstream 1.11 has them on `multi` only.

### `shot-scraper multi CONFIG`

`-a, --auth`, `--retina` (flag), `--scale-factor`, `--timeout`,
`--fail-on-error` (flag), `-n, --no-clobber` (flag), `-o, --output`
(repeatable), `-b, --browser`, `--browser-arg` (repeatable), `--user-agent`,
`--reduced-motion` (flag), `--log-console` (flag), `--skip` (flag), `--fail`
(flag), `--silent` (flag), `--auth-username`, `--auth-password`,
`--leave-server` (flag), `--har` (flag), `--har-zip` (flag), `--har-file`

### `shot-scraper javascript URL [JAVASCRIPT]`

`-i, --input` (default `-`), `-a, --auth`, `-w, --width` (default 1280),
`-h, --height`, `-o, --output` (default `-`), `-r, --raw` (flag), `--timeout`,
`-b, --browser`, `--browser-arg` (repeatable), `--user-agent`,
`--reduced-motion` (flag), `--log-console` (flag), `--skip` (flag), `--fail`
(flag), `--bypass-csp` (flag), `--auth-username`, `--auth-password`

### `shot-scraper html URL`

`-a, --auth`, `-o, --output` (default `-`), `-j, --javascript`, `--js-file`,
`-s, --selector`, `--wait`, `--timeout`, `--log-console` (flag),
`-b, --browser`, `--browser-arg` (repeatable), `--user-agent`, `--skip` (flag),
`--fail` (flag), `--bypass-csp` (flag), `--silent` (flag), `--auth-username`,
`--auth-password`

### `shot-scraper pdf URL`

`-a, --auth`, `-o, --output`, `-j, --javascript`, `--js-file`, `--wait`,
`--wait-for`, `--timeout`, `--media-screen` (flag), `--landscape` (flag),
`--format`, `--width`, `--height`, `--scale`, `--print-background` (flag),
`--log-console` (flag), `--skip` (flag), `--fail` (flag), `--bypass-csp` (flag),
`--silent` (flag), `--auth-username`, `--auth-password`

### `shot-scraper accessibility URL`

`-a, --auth`, `-o, --output` (default `-`), `-j, --javascript`, `--js-file`,
`--timeout`, `--log-console` (flag), `--skip` (flag), `--fail` (flag),
`--bypass-csp` (flag), `--auth-username`, `--auth-password`

### `shot-scraper har URL`

`-z, --zip` (flag), `-x, --extract` (flag), `-a, --auth`, `-o, --output`,
`--wait`, `--wait-for`, `-j, --javascript`, `--js-file`, `--timeout`,
`--log-console` (flag), `--skip` (flag), `--fail` (flag), `--bypass-csp` (flag),
`--auth-username`, `--auth-password`

### `shot-scraper auth URL CONTEXT_FILE`

`-b, --browser`, `--browser-arg` (repeatable), `--user-agent`, `--devtools`
(flag), `--log-console` (flag)

### `shot-scraper video STORYBOARD_FILE`

`-o, --output`, `-a, --auth`, `--timeout`, `-b, --browser`, `--browser-arg`
(repeatable), `--user-agent`, `--reduced-motion` (flag), `--log-console` (flag),
`--skip` (flag), `--fail` (flag), `--bypass-csp` (flag), `--silent` (flag),
`--auth-username`, `--auth-password`, `--leave-server` (flag), `--mp4` (flag)

### `shot-scraper install`

`-b, --browser`

## Multi-shot YAML keys

Every key upstream reads from an entry of a `multi` configuration, from the
`multi` command body and `take_shot`. There are no others: an entry is a plain
dict, and anything else in it is ignored.

| key | read at |
| --- | --- |
| `sh` | `multi` |
| `python` | `multi` |
| `server` | `multi` |
| `url` | `multi`, `take_shot` |
| `output` | `multi`, `take_shot` |
| `width` | `take_shot` |
| `height` | `take_shot` (absent means full-page) |
| `quality` | `take_shot` (also selects JPEG) |
| `omit_background` | `take_shot` |
| `padding` | `take_shot` |
| `wait` | `take_shot` |
| `wait_for` | `take_shot` |
| `javascript` | `take_shot` |
| `js_file` | `take_shot` |
| `selector`, `selectors` | `take_shot` |
| `selector_all`, `selectors_all` | `take_shot` |
| `js_selector`, `js_selectors` | `take_shot` |
| `js_selector_all`, `js_selectors_all` | `take_shot` |

Singular and plural spellings are aliases; upstream appends the singular value
to the plural list.

`skip_shot` is not a user key: the `multi` command injects it into entries with
no `output` when a HAR file is being recorded, so the page loads without a
screenshot being written.

Retina, scale factor, timeout, skip and fail are command-line flags only. They
apply to the whole run and are never read from an entry.

## Behaviours worth reproducing exactly

- `filename_for_url`: fold `netloc + path` on `.` and `/` to `-`, right-trim
  `-`, drop every character outside `[A-Za-z0-9_-]`, left-trim `-`, append the
  extension, then add `.1`, `.2`, … until the name is free. Query strings and
  fragments are not part of the name.
- `url_or_file_path`: an argument that names an existing file becomes
  `file:` plus its absolute path; anything that is not already `http://` or
  `https://` gets `http://`.
- `_resolve_javascript`: `javascript` and `js_file` together is an error.
- `normalize_scale_factor`: `--retina` and `--scale-factor` together is an
  error, `--scale-factor` must be positive, `--retina` means 2.
- `skip_or_fail`: `--skip` and `--fail` together is an error; a 4xx or 5xx
  response exits 0 under `--skip` and errors under `--fail`.
- `--js-file` accepts a path, `-` for standard input, or a `gh:` reference:
  `gh:username/repo/path/to/file.js`, or `gh:username/file.js` as shorthand for
  `gh:username/shot-scraper-scripts/file.js`. A missing `.js` suffix is added.
