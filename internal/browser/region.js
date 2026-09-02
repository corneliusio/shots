// Returns the padded rectangle enclosing every element the selectors match, in
// document coordinates, or {error} naming the selector that matched nothing.
//
// This reproduces shot-scraper's _selector_javascript without its wrapper
// element: upstream builds an absolutely positioned div over the combined
// bounds and screenshots that, because Playwright can only capture a locator.
// CDP takes a clip rectangle directly, so the div, its random id and the 300ms
// settle it needed are not required.
(spec) => {
  const selectedElementsKey = Symbol.for('github.com/corneliusio/shots.selected-elements');
  delete globalThis[selectedElementsKey];

  const els = [];
  const every = () => Array.from(document.getElementsByTagName('*'));
  const predicate = (src) => new Function('el', `return (${src})`);

  for (const s of spec.selectors ?? []) {
    const el = document.querySelector(s);
    if (!el) return { error: `no element matches selector ${JSON.stringify(s)}` };
    els.push(el);
  }
  for (const s of spec.selectorsAll ?? []) {
    els.push(...document.querySelectorAll(s));
  }
  for (const src of spec.jsSelectors ?? []) {
    const el = every().find(predicate(src));
    if (!el) return { error: `no element matches js selector ${JSON.stringify(src)}` };
    els.push(el);
  }
  for (const src of spec.jsSelectorsAll ?? []) {
    els.push(...every().filter(predicate(src)));
  }
  if (els.length === 0) return { error: 'no elements matched' };

  globalThis[selectedElementsKey] = els;

  let top = Infinity, left = Infinity, bottom = -Infinity, right = -Infinity;
  for (const el of els) {
    const r = el.getBoundingClientRect();
    top = Math.min(top, r.top);
    left = Math.min(left, r.left);
    bottom = Math.max(bottom, r.bottom);
    right = Math.max(right, r.right);
  }
  // Padding is clamped to the document: Chrome keeps a clip's size but drops
  // its position when the origin is negative, which turned a padded crop of an
  // edge-flush element into the top of the viewport.
  const pad = spec.padding ?? 0;
  const root = document.documentElement;
  const x = Math.max(0, left + window.scrollX - pad);
  const y = Math.max(0, top + window.scrollY - pad);
  return {
    x,
    y,
    width: Math.min(right + window.scrollX + pad, Math.max(root.scrollWidth, root.clientWidth)) - x,
    height: Math.min(bottom + window.scrollY + pad, Math.max(root.scrollHeight, root.clientHeight)) - y,
  };
}
