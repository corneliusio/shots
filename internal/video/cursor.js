// Draws a pointer into a recorded page. Installed as an init script, so it
// survives every navigation, and guarded so that a document it has already run
// in is left alone.
(() => {
  const options = OPTIONS;
  if (window.__shotsCursor) return;
  window.__shotsCursor = true;

  const install = () => {
    if (!document.body) {
      requestAnimationFrame(install);
      return;
    }
    const style = document.createElement('style');
    style.textContent = `
      #shots-cursor {
        position: fixed; left: 0; top: 0;
        width: ${options.size}px; height: ${options.size}px;
        margin-left: ${-options.size / 2}px; margin-top: ${-options.size / 2}px;
        border-radius: 999px;
        background: ${options.color};
        border: 2px solid white;
        box-shadow: 0 2px 10px rgba(0, 0, 0, 0.35);
        opacity: 0;
        pointer-events: none;
        z-index: 2147483647;
        transition: left 120ms ease-out, top 120ms ease-out, opacity 120ms ease-out;
      }
      .shots-click {
        position: fixed;
        width: ${options.clickSize}px; height: ${options.clickSize}px;
        margin-left: ${-options.clickSize / 2}px; margin-top: ${-options.clickSize / 2}px;
        border: 3px solid ${options.color};
        border-radius: 999px;
        pointer-events: none;
        z-index: 2147483646;
        animation: shots-click 650ms ease-out forwards;
      }
      @keyframes shots-click {
        from { opacity: 0.85; transform: scale(0.25); }
        to { opacity: 0; transform: scale(1.25); }
      }
    `;
    document.documentElement.appendChild(style);

    let dot = null;
    if (options.visible) {
      dot = document.createElement('div');
      dot.id = 'shots-cursor';
      document.body.appendChild(dot);
    }

    const move = (event) => {
      if (!dot) return;
      dot.style.left = `${event.clientX}px`;
      dot.style.top = `${event.clientY}px`;
      dot.style.opacity = '1';
    };
    const ring = (event) => {
      if (!options.clicks) return;
      const el = document.createElement('div');
      el.className = 'shots-click';
      el.style.left = `${event.clientX}px`;
      el.style.top = `${event.clientY}px`;
      document.body.appendChild(el);
      setTimeout(() => el.remove(), 700);
    };

    document.addEventListener('mousemove', move, true);
    document.addEventListener('mousedown', (event) => { move(event); ring(event); }, true);
    document.addEventListener('click', move, true);
  };

  install();
})();
