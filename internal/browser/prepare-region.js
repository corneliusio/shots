// Scrolls a document-space clip into view and temporarily hides fixed or
// sticky elements that overlap it without belonging to the selected DOM trees.
(async (clip) => {
  const selectedElementsKey = Symbol.for('github.com/corneliusio/shots.selected-elements');
  const restoreOverlaysKey = Symbol.for('github.com/corneliusio/shots.restore-overlays');
  const previousRestore = globalThis[restoreOverlaysKey];
  if (typeof previousRestore === 'function') previousRestore();
  delete globalThis[restoreOverlaysKey];

  const root = document.documentElement;
  window.scrollTo({
    left: Math.max(0, clip.x - (root.clientWidth - clip.width) / 2),
    top: Math.max(0, clip.y - (root.clientHeight - clip.height) / 2),
    behavior: 'instant',
  });
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));

  const storedSelection = globalThis[selectedElementsKey];
  const selectedElements = Array.isArray(storedSelection)
    ? storedSelection.filter((element) => element instanceof Element)
    : [];
  const target = {
    left: clip.x - window.scrollX,
    top: clip.y - window.scrollY,
    right: clip.x - window.scrollX + clip.width,
    bottom: clip.y - window.scrollY + clip.height,
  };
  const changes = [];
  globalThis[restoreOverlaysKey] = () => {
    for (const change of changes) {
      if (change.visibility === '') {
        change.element.style.removeProperty('visibility');
      } else {
        change.element.style.setProperty('visibility', change.visibility, change.priority);
      }
      if (!change.hadStyleAttribute && change.element.getAttribute('style') === '') {
        change.element.removeAttribute('style');
      }
    }
  };

  for (const element of document.getElementsByTagName('*')) {
    const style = getComputedStyle(element);
    if (style.position !== 'fixed' && style.position !== 'sticky') continue;
    if (style.display === 'none' || style.visibility !== 'visible' || Number(style.opacity) === 0) continue;
    const belongsToSelection = selectedElements.some((selected) =>
      selected === element || selected.contains(element) || element.contains(selected),
    );
    if (belongsToSelection) continue;
    const rect = element.getBoundingClientRect();
    const intersects = rect.width > 0 && rect.height > 0 &&
      rect.left < target.right && rect.right > target.left &&
      rect.top < target.bottom && rect.bottom > target.top;
    if (!intersects) continue;
    changes.push({
      element,
      hadStyleAttribute: element.hasAttribute('style'),
      visibility: element.style.getPropertyValue('visibility'),
      priority: element.style.getPropertyPriority('visibility'),
    });
    element.style.setProperty('visibility', 'hidden', 'important');
  }

  if (changes.length > 0) {
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  }
})
