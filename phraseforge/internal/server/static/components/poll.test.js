import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// poll.js is loaded from disk (the real file) and run against a stub document
// and anchor, with node's mock timers, so the timing rules are checked without
// waiting: backoff, pause while the tab is hidden, the upper bound, kick, and
// stopping when the page or section is gone.
const source = readFileSync(new URL("../js/poll.js", import.meta.url), "utf8");
const SECOND = 1000;
const MINUTE = 60 * SECOND;

function setup({ hidden = false } = {}) {
  mock.timers.enable({ apis: ["setTimeout"] });
  const listeners = new Set();
  const doc = {
    hidden,
    addEventListener: (_type, fn) => listeners.add(fn),
    removeEventListener: (_type, fn) => listeners.delete(fn),
    show() {
      doc.hidden = false;
      [...listeners].forEach((fn) => fn());
    },
  };
  const win = {};
  new Function("window", "document", source)(win, doc);
  const anchor = { isConnected: true, rects: 1, getClientRects() { return { length: this.rects }; } };
  return { pfPoll: win.pfPoll, doc, anchor, listeners };
}

afterEach(() => mock.timers.reset());

// flush lets the awaited run() settle: setImmediate is not mocked.
const flush = () => new Promise((resolve) => setImmediate(resolve));
async function advance(ms) {
  mock.timers.tick(ms);
  await flush();
}
// advanceInSteps moves time in 1 s steps, letting each run settle and schedule
// the next timer, as a real clock would; one big tick would fire only the first.
async function advanceInSteps(ms) {
  for (let t = 0; t < ms; t += SECOND) await advance(SECOND);
}

test("kick runs at once and polling stops as soon as nothing is pending", async () => {
  const { pfPoll, anchor } = setup();
  let calls = 0;
  const poll = pfPoll({ run: async () => { calls += 1; return false; }, anchor });
  poll.kick();
  await flush();
  assert.equal(calls, 1);
  await advance(60 * MINUTE);
  assert.equal(calls, 1, "no timer may be left running when nothing is pending");
});

test("while work is pending the interval backs off 8 s, 15 s, 30 s and stays at 30 s", async () => {
  const { pfPoll, anchor } = setup();
  let calls = 0;
  const poll = pfPoll({ run: async () => { calls += 1; return true; }, anchor });
  poll.kick();
  await flush();
  assert.equal(calls, 1);
  for (const [wait, want] of [[8 * SECOND, 2], [15 * SECOND, 3], [30 * SECOND, 4], [30 * SECOND, 5]]) {
    await advance(wait - 1);
    assert.equal(calls, want - 1, `still ${want - 1} calls 1 ms before ${wait}`);
    await advance(1);
    assert.equal(calls, want, `${want} calls after ${wait}`);
  }
});

test("polling stops after 15 minutes of waiting and the next kick starts it again", async () => {
  const { pfPoll, anchor } = setup();
  let calls = 0;
  const poll = pfPoll({ run: async () => { calls += 1; return true; }, anchor });
  poll.kick();
  await flush();
  await advanceInSteps(15 * MINUTE);
  const atCap = calls;
  assert.ok(atCap > 20 && atCap < 40, `calls in 15 minutes = ${atCap}: 8 s, 15 s, then 30 s steps give about 32`);
  await advanceInSteps(60 * MINUTE);
  assert.equal(calls, atCap, "no request after the cap");
  poll.kick();
  await flush();
  assert.equal(calls, atCap + 1, "a kick restarts polling");
  await advance(8 * SECOND);
  assert.equal(calls, atCap + 2, "and at the 8 s interval again");
});

test("a hidden tab makes no request and becoming visible refreshes once", async () => {
  const { pfPoll, doc, anchor } = setup({ hidden: true });
  let calls = 0;
  const poll = pfPoll({ run: async () => { calls += 1; return true; }, anchor });
  poll.kick();
  await flush();
  assert.equal(calls, 1, "the kick itself runs");
  await advance(60 * MINUTE);
  assert.equal(calls, 1, "no timer while hidden");
  doc.show();
  await flush();
  assert.equal(calls, 2, "one refresh when the tab is visible again");
  await advance(8 * SECOND);
  assert.equal(calls, 3, "and polling goes on");
});

test("the time spent hidden does not count towards the 15 minute bound", async () => {
  const { pfPoll, doc, anchor } = setup();
  let calls = 0;
  const poll = pfPoll({ run: async () => { calls += 1; return true; }, anchor });
  poll.kick();
  await flush();
  await advance(8 * SECOND); // 2 calls
  doc.hidden = true;
  await advance(15 * SECOND); // the timer fires and finds the tab hidden
  const before = calls;
  assert.equal(before, 2, "a timer that fires while hidden makes no request");
  await advance(10 * MINUTE);
  assert.equal(calls, before, "hidden: no requests");
  doc.show();
  await flush();
  await advance(30 * SECOND);
  assert.ok(calls > before + 1, "visible again: polling continues instead of having used up its time");
});

test("a kick restarts the backoff at 8 s", async () => {
  const { pfPoll, anchor } = setup();
  let calls = 0;
  const poll = pfPoll({ run: async () => { calls += 1; return true; }, anchor });
  poll.kick();
  await flush();
  await advance(8 * SECOND);
  await advance(15 * SECOND); // now at the 30 s step
  const before = calls;
  poll.kick();
  await flush();
  assert.equal(calls, before + 1);
  await advance(8 * SECOND);
  assert.equal(calls, before + 2, "the next poll is 8 s after the kick, not 30 s");
});

test("it stops for good when the anchor leaves the page or its section is hidden", async () => {
  for (const [name, leave] of [["removed", (a) => { a.isConnected = false; }], ["section hidden", (a) => { a.rects = 0; }]]) {
    const { pfPoll, anchor } = setup();
    let calls = 0;
    const poll = pfPoll({ run: async () => { calls += 1; return true; }, anchor });
    poll.kick();
    await flush();
    leave(anchor);
    await advance(8 * SECOND);
    await advance(60 * MINUTE);
    assert.equal(calls, 1, `${name}: no request after the page is gone`);
    poll.kick();
    await flush();
    assert.equal(calls, 1, `${name}: a stale kick must not revive it`);
    mock.timers.reset();
  }
});

test("a kick during a run owns the schedule: the stale run adds no second timer", async () => {
  const { pfPoll, anchor } = setup();
  const releases = [];
  let calls = 0;
  const poll = pfPoll({
    run: () => { calls += 1; return new Promise((resolve) => releases.push(() => resolve(true))); },
    anchor,
  });
  poll.kick(); // run 1 in flight
  poll.kick(); // run 2 in flight, run 1 is stale
  await flush();
  assert.equal(calls, 2);
  releases[0](); // the stale one finishes first
  await flush();
  releases[1]();
  await flush();
  await advance(8 * SECOND);
  assert.equal(calls, 3, "exactly one timer after both runs settled");
});

test("a run that throws ends polling quietly", async () => {
  const { pfPoll, anchor } = setup();
  let calls = 0;
  const poll = pfPoll({ run: async () => { calls += 1; throw new Error("boom"); }, anchor });
  poll.kick();
  await flush();
  await advance(60 * MINUTE);
  assert.equal(calls, 1);
});

test("kick returns a promise that settles when its run has finished", async () => {
  const { pfPoll, anchor } = setup();
  let finished = false;
  const poll = pfPoll({ run: async () => { await flush(); finished = true; return false; }, anchor });
  await poll.kick();
  assert.equal(finished, true, "awaiting kick waits for the run, so a caller can show its message after the refresh");
});

test("stop ends polling for good, also against a later kick", async () => {
  const { pfPoll, anchor } = setup();
  let calls = 0;
  const poll = pfPoll({ run: async () => { calls += 1; return true; }, anchor });
  poll.kick();
  await flush();
  poll.stop();
  await advance(60 * MINUTE);
  assert.equal(calls, 1);
  await poll.kick();
  assert.equal(calls, 1, "a stopped poll cannot be revived by a stale kick");
});
