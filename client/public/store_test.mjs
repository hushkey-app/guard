// A fake tab: the store only needs sessionStorage.
const bag = new Map();
globalThis.sessionStorage = {
  getItem: (k) => (bag.has(k) ? bag.get(k) : null),
  setItem: (k, v) => bag.set(k, v),
  removeItem: (k) => bag.delete(k),
};
Object.keys = ((original) => (target) => (target === globalThis.sessionStorage ? [...bag.keys()] : original(target)))(Object.keys);

const store = await import("./store.js");
const draws = [];
let fetches = 0;
const load = (value) => async () => { fetches++; return value; };
const draw = (value, stale) => draws.push(`${JSON.stringify(value)} stale=${stale}`);

// 1. First visit: nothing known, one draw of the fetched value.
await store.ensure("k", load({ a: 1 }), draw);
console.assert(draws.length === 1 && draws[0] === '{"a":1} stale=false', "first visit", draws);

// 2. Still on that page, refreshing: the answer has not changed and it is
//    already on screen, so nothing is drawn at all.
draws.length = 0;
await store.ensure("k", load({ a: 1 }), draw);
console.assert(draws.length === 0, "an unchanged answer already on screen is not redrawn", draws);

// 3. The answer moves: drawn once, with the new value.
draws.length = 0;
await store.ensure("k", load({ a: 2 }), draw);
console.assert(draws.length === 1 && draws[0] === '{"a":2} stale=false', "a changed answer is drawn", draws);

// 4. Navigating away throws the DOM out; coming back draws what is known
//    immediately, and does not draw again when the answer is the same.
draws.length = 0;
store.screenCleared();
await store.ensure("k", load({ a: 2 }), draw);
console.assert(draws.length === 1 && draws[0] === '{"a":2} stale=true',
  "a return visit paints from the store and stops there", draws);

// 5. And when it has moved while away: the known value first, then the new one.
draws.length = 0;
store.screenCleared();
await store.ensure("k", load({ a: 3 }), draw);
console.assert(draws.length === 2 && draws[1] === '{"a":3} stale=false',
  "a return visit corrects itself when the answer moved", draws);

// 6. Concurrent callers share one request.
fetches = 0;
store.forget("shared");
await Promise.all([
  store.ensure("shared", load({ b: 1 }), () => {}),
  store.ensure("shared", load({ b: 1 }), () => {}),
]);
console.assert(fetches === 1, "one fetch for concurrent callers", fetches);

// 7. set() wakes subscribers only when something changed.
let woke = 0;
const off = store.subscribe("k", () => woke++);
store.set("k", { a: 3 });
console.assert(woke === 0, "an identical set wakes nobody", woke);
store.set("k", { a: 4 });
console.assert(woke === 1, "a real change wakes subscribers", woke);
off();

// 8. Cold start: a fresh module instance reads the mirror and knows it has
//    never been confirmed live.
const cold = await import("./store.js?cold=1");
console.assert(JSON.stringify(cold.get("k")) === '{"a":4}', "hydrated from sessionStorage", cold.get("k"));
console.assert(cold.freshness("k") === 0, "a hydrated value has never been confirmed live");

// 9. A restored backup: every key describes an instance that is gone, and the
//    mirror has to go with the memory or a reload would bring it all back.
store.set("k", { a: 5 });
store.forgetAll();
console.assert(store.get("k") === undefined, "forgetAll drops what is in memory", store.get("k"));
const afterRestore = await import("./store.js?cold=2");
console.assert(afterRestore.get("k") === undefined, "forgetAll drops the mirror too", afterRestore.get("k"));

console.log("store: all checks passed");
