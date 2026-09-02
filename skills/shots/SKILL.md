---
name: shots
description: Capture screenshots, PDFs, HTML, accessibility trees, HAR files and screen recordings of web pages with the `shots` CLI (Chrome-based, a Go rewrite of shot-scraper). Use when asked to screenshot a page or element, check how a page renders, verify a UI change in a real browser, run JavaScript against a page, or record a browser demo. Do not use for driving a browser interactively step by step.
---

# shots

`shots` is a single binary that drives Chrome. Its `--help` output is complete
and current; read it instead of guessing flags.

```bash
shots --help              # commands
shots take --help         # flags for one command; every command has this
shots doctor              # run first if anything fails; exits 1 with the reason
shots install             # download a pinned headless Chrome if none is found
```

`shots example.com` is shorthand for `shots take example.com`.

## Working as an agent

- Prefer `--stable --report -` when you need to judge a result. `--stable`
  removes animation and font noise; the report is one JSON object on stdout
  with `ok`, `status`, `final_url`, `console`, `page_errors` and
  `failed_requests`. Read it before opening the image.
- Iterating on one page: `shots take URL --repeat --stable --report - -o shot.png`
  keeps Chrome open and retakes the shot for every line on stdin.
- Many separate commands: run `shots browser > browser.url &` once, then pass
  `--cdp-url "$(cat browser.url)"` to each command.
- A known set of pages: write a `multi` YAML (below) rather than looping.
- Use `--color-scheme light|dark` when output must not depend on the machine,
  `--insecure` for a local server with a self-signed certificate, and
  `-a state.json` from `shots auth` for pages behind a login.
- Exit codes: 0 ok, 1 browser/network/filesystem failure, 2 bad arguments or
  config. stdout carries only the requested output; progress goes to stderr.
- `-o` paths need an existing parent directory. Formats and extensions must
  agree (`--format png` with `.png`).
- `--javascript`, `--js-file` and YAML `sh:` steps run unsandboxed with your
  privileges.

## YAML formats

Help does not document these; the README section for each command does.

`shots multi shots.yml` runs entries in order in one browser context:

```yaml
- server: ./scripts/serve
- url: http://localhost:8000/
  output: home.png
  height: 720
- sh: node scripts/seed.js
- url: http://localhost:8000/pricing
  output: pricing.jpg
  selectors: [.plans]
  wait_for: document.querySelectorAll('.plan').length === 3
```

Entry keys mirror the `take` flags in snake_case (`width`, `height`, `format`,
`quality`, `omit_background`, `wait`, `wait_for`, `javascript`, `js_file`,
`padding`, `selector(s)`, `selector(s)_all`, `js_selector(s)`,
`js_selector(s)_all`) plus `sh` and `server`. Unknown keys are rejected.

`shots video demo.yml` records a storyboard:

```yaml
url: http://localhost:8000/
output: demo.webm
viewport: {width: 1280, height: 720}
scenes:
  - name: Search
    do:
      - click: '#search'
      - type: {selector: '#search', text: shots, delay_ms: 90}
      - press: Enter
      - wait_for: '.results'
      - screenshot: results.png
```

Actions: `open`, `click`, `fill`, `type`, `press`, `scroll`, `pause`,
`wait_for`, `wait_for_url` (glob), `javascript`, `screenshot`, `sh`. Each takes
a scalar shorthand or a mapping. Needs `ffmpeg`.
