async () => {
  const marker = 'data-shots-stable';
  if (!document.querySelector(`style[${marker}]`)) {
    const style = document.createElement('style');
    style.setAttribute(marker, '');
    style.textContent = `
      *, *::before, *::after {
        animation: none !important;
        caret-color: transparent !important;
        transition: none !important;
      }
    `;
    (document.head || document.documentElement).append(style);
  }

  if (document.fonts) {
    await document.fonts.ready;
  }

  const images = Array.from(document.images);
  for (const image of images) {
    if (image.loading === 'lazy') {
      image.loading = 'eager';
    }
  }

  await Promise.all(
    images.map((image) => {
      if (image.complete) {
        return undefined;
      }
      return new Promise((resolve) => {
        image.addEventListener('load', resolve, { once: true });
        image.addEventListener('error', resolve, { once: true });
      });
    }),
  );

  await new Promise((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(resolve));
  });
}
