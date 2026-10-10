/** Clamp the panel after dragging, resizing or a viewport change. */
export function movable(node: HTMLElement, enabled: boolean) {
  let position: { x: number; y: number } | undefined;
  let drag: { id: number; x: number; y: number } | undefined;
  function place(x: number, y: number) {
    const rect = node.getBoundingClientRect();
    position = {
      x: Math.max(0, Math.min(x, document.documentElement.clientWidth - rect.width)),
      y: Math.max(0, Math.min(y, window.innerHeight - rect.height)),
    };
    Object.assign(node.style, {
      left: position.x + 'px',
      top: position.y + 'px',
      right: 'auto',
      bottom: 'auto',
    });
  }
  function clamp() {
    if (enabled && position) place(position.x, position.y);
  }
  function down(event: PointerEvent) {
    if (
      !enabled ||
      event.button !== 0 ||
      !(event.target instanceof Element) ||
      !event.target.closest('[data-drag-handle]')
    )
      return;
    const rect = node.getBoundingClientRect();
    drag = { id: event.pointerId, x: event.clientX - rect.left, y: event.clientY - rect.top };
    node.setPointerCapture(event.pointerId);
    event.preventDefault();
    node.querySelector<HTMLElement>('[data-drag-handle]')?.focus();
  }
  function move(event: PointerEvent) {
    if (drag?.id === event.pointerId) place(event.clientX - drag.x, event.clientY - drag.y);
  }
  function stop() {
    drag = undefined;
  }
  function key(event: KeyboardEvent) {
    if (
      !enabled ||
      !(event.target instanceof Element) ||
      !event.target.closest('[data-drag-handle]')
    )
      return;
    const directions: Record<string, [number, number]> = {
      ArrowLeft: [-1, 0],
      ArrowRight: [1, 0],
      ArrowUp: [0, -1],
      ArrowDown: [0, 1],
    };
    const direction = directions[event.key];
    if (!direction) return;
    event.preventDefault();
    const rect = node.getBoundingClientRect();
    const step = event.shiftKey ? 40 : 10;
    place(rect.left + direction[0] * step, rect.top + direction[1] * step);
  }
  function reset() {
    stop();
    position = undefined;
    for (const property of ['left', 'top', 'right', 'bottom']) node.style.removeProperty(property);
  }
  const observer = new ResizeObserver(clamp);
  observer.observe(node);
  window.addEventListener('resize', clamp);
  node.addEventListener('pointerdown', down);
  node.addEventListener('pointermove', move);
  node.addEventListener('pointerup', stop);
  node.addEventListener('pointercancel', stop);
  node.addEventListener('lostpointercapture', stop);
  node.addEventListener('keydown', key);
  node.addEventListener('eli-place', reset);
  return {
    update(value: boolean) {
      if (value !== enabled) {
        enabled = value;
        reset();
      }
    },
    destroy() {
      observer.disconnect();
      window.removeEventListener('resize', clamp);
      node.removeEventListener('pointerdown', down);
      node.removeEventListener('pointermove', move);
      node.removeEventListener('pointerup', stop);
      node.removeEventListener('pointercancel', stop);
      node.removeEventListener('lostpointercapture', stop);
      node.removeEventListener('keydown', key);
      node.removeEventListener('eli-place', reset);
    },
  };
}
