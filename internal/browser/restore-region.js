() => {
  const selectedElementsKey = Symbol.for('github.com/corneliusio/shots.selected-elements');
  const restoreOverlaysKey = Symbol.for('github.com/corneliusio/shots.restore-overlays');
  const restore = globalThis[restoreOverlaysKey];
  if (typeof restore === 'function') restore();
  delete globalThis[restoreOverlaysKey];
  delete globalThis[selectedElementsKey];
}
