/* Progressive enhancement for Material's instant navigation. No animation
   library, per-frame JavaScript, remote assets or persistent tracking. */
(() => {
  const gestures = ["hello", "curious", "zoom", "spin"];
  const duration = 6000; // Matches the finite CSS performances.
  let dispose = () => {};

  function mount() {
    dispose();
    const root = document.querySelector("[data-zoomies-brand]");
    if (!root) return;
    const image = root.querySelector("img");
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
    let alive = true;
    let ready = false;
    let visible = !("IntersectionObserver" in window);
    let timer = null;
    let started = 0;
    let remaining = 500;
    let active = false;
    let previous = "";
    let bag = [];

    function nextGesture() {
      if (!bag.length) {
        bag = [...gestures];
        for (let i = bag.length - 1; i > 0; i--) {
          const j = Math.floor(Math.random() * (i + 1));
          [bag[i], bag[j]] = [bag[j], bag[i]];
        }
        // Do not repeat across the boundary of two shuffled bags either.
        if (bag[bag.length - 1] === previous)
          [bag[0], bag[bag.length - 1]] = [bag[bag.length - 1], bag[0]];
      }
      previous = bag.pop();
      return previous;
    }

    function tick() {
      timer = null;
      if (active) {
        root.removeAttribute("data-gesture");
        remaining = 2600 + Math.random() * 4000;
      } else {
        root.dataset.gesture = nextGesture();
        remaining = duration;
      }
      active = !active;
      sync();
    }

    function sync() {
      const running =
        alive && ready && visible && !document.hidden && !reduced.matches;
      root.dataset.paused = String(!running);
      if (!running && timer !== null) {
        clearTimeout(timer);
        timer = null;
        remaining = Math.max(0, remaining - (performance.now() - started));
      } else if (running && timer === null) {
        started = performance.now();
        timer = setTimeout(tick, remaining);
      }
    }

    function motionChange() {
      sync();
      // A preference change returns to the canonical static mark immediately.
      if (reduced.matches) {
        root.removeAttribute("data-gesture");
        active = false;
        remaining = 500;
      }
    }

    const observer =
      "IntersectionObserver" in window
        ? new IntersectionObserver((entries) => {
            visible = entries.some((entry) => entry.isIntersecting);
            sync();
          })
        : null;
    observer?.observe(root);
    document.addEventListener("visibilitychange", sync);
    reduced.addEventListener("change", motionChange);
    dispose = () => {
      alive = false;
      if (timer !== null) clearTimeout(timer);
      observer?.disconnect();
      document.removeEventListener("visibilitychange", sync);
      reduced.removeEventListener("change", motionChange);
      image.removeEventListener("load", enhance);
      root.classList.remove("is-ready");
      root.removeAttribute("data-gesture");
    };

    function enhance() {
      image
        .decode()
        .then(() => {
          if (!alive) return;
          ready = true;
          root.classList.add("is-ready");
          sync();
        })
        .catch(() => {});
    }
    if (image.complete) enhance();
    else image.addEventListener("load", enhance, { once: true });
  }

  if (typeof document$ !== "undefined") document$.subscribe(mount);
  else if (document.readyState === "loading")
    document.addEventListener("DOMContentLoaded", mount, { once: true });
  else mount();
})();
