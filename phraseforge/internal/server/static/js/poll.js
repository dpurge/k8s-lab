// poll.js: one polling helper for every page that waits on background work (the
// review columns of Vocabulary and Models, the review row of a text or dialog,
// the Jobs list), so they all behave the same and none loads the server more
// than it must.
//
//   const poll = pfPoll({ run, anchor });
//   poll.kick();   // run now, and keep going while run() reports pending work
//   poll.stop();   // end it (a new poll replaces it when the page is shown again)
//
// run is async and resolves true while something is still pending; it renders
// its own result and reports its own errors (an error means "do not poll again":
// a next click kicks it). anchor is an element of the page being polled: polling
// stops for good once it has left the document or its section is hidden (the
// shell hides inactive sections with display: none instead of removing them,
// and the section re-renders, with a new poll, when it is shown again).
//
// The interval backs off 8 s, 15 s, 30 s. While the tab is hidden no timer runs
// and no request is made; becoming visible again refreshes once. After 15
// minutes of waiting in total polling stops until the next kick (any click).
(function () {
  const STEPS_MS = [8000, 15000, 30000];
  const CAP_MS = 15 * 60 * 1000;

  window.pfPoll = function pfPoll({ run, anchor }) {
    let timer = null;
    let step = 0;
    let waited = 0;
    let generation = 0;
    let paused = false;
    let stopped = false;

    // getClientRects is empty when the element or an ancestor is display: none.
    const isOnPage = () => anchor.isConnected && anchor.getClientRects().length > 0;

    function stop() {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    }

    function onVisible() {
      if (!document.hidden && paused) {
        paused = false;
        execute();
      }
    }

    function schedule() {
      clearTimeout(timer);
      if (document.hidden) {
        paused = true;
        return;
      }
      const delay = STEPS_MS[Math.min(step, STEPS_MS.length - 1)];
      if (waited + delay > CAP_MS) return; // the next kick starts again
      timer = setTimeout(() => {
        if (document.hidden) {
          // The tab was hidden while waiting: no request, and this wait does not count.
          paused = true;
          return;
        }
        waited += delay;
        step += 1;
        execute();
      }, delay);
    }

    async function execute() {
      const mine = ++generation;
      if (!isOnPage()) {
        stop();
        return;
      }
      let pending = false;
      try {
        pending = await run();
      } catch (_e) {
        pending = false; // run reports its own errors; see above
      }
      // A kick while this run was in flight owns the schedule now.
      if (stopped || mine !== generation) return;
      if (pending) schedule();
    }

    document.addEventListener("visibilitychange", onVisible);
    return {
      // kick returns a promise that settles when its run has finished, for
      // callers that show a message after the refresh.
      kick() {
        if (stopped) return Promise.resolve();
        clearTimeout(timer);
        step = 0;
        waited = 0;
        paused = false;
        return execute();
      },
      // stop ends polling for good; for a page whose anchor outlives its renders.
      stop,
    };
  };
})();
