// Shared by guard.js, charts.js and views.js.
//
// These lived in guard.js until the views page needed them too. Everything here
// is either a DOM shorthand, a formatter, or the one fetch wrapper — no state,
// nothing that renders. Modules that render import from here; this imports from
// nothing.
//
// Note for anyone adding a Tailwind class in a JavaScript file: the @source
// list in the Makefile's css target has to see the file, and the class has to
// appear as a literal string. A class assembled from a variable is a class
// Tailwind cannot find, and it will simply not be in the bundle.

export const number = new Intl.NumberFormat();
export const svgNS = "http://www.w3.org/2000/svg";

// Eight hues that stay distinguishable on the dark surface and keep their
// order — series colours are assigned by index, so a series must not change
// colour because another one appeared above it.
export const palette = ["#5bd8a6", "#60a5fa", "#c084fc", "#f59e0b", "#fb7185", "#22d3ee", "#a3e635", "#f472b6"];

export const muted = "text-muted-foreground";

export const text = (value) => document.createTextNode(value ?? "");
export const qs = (selector, root = document) => root.querySelector(selector);
export const qsa = (selector, root = document) => [...root.querySelectorAll(selector)];

export async function request(path, options = {}) {
  const response = await fetch(path, { headers: { Accept: "application/json", ...(options.headers || {}) }, ...options });
  if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
  if (response.status === 204) return null;
  return response.json();
}

// Guard's write endpoints take one shared bearer token, kept in sessionStorage
// by the settings page. The dashboard is the only caller, so this is the whole
// of its auth story.
export function adminHeaders() {
  const token = qs('[data-setting="token"]')?.value || sessionStorage.getItem("guard.token") || "";
  if (token) sessionStorage.setItem("guard.token", token);
  return { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) };
}

export function el(tag, className = "", content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.appendChild(text(content));
  return node;
}

export function svg(tag, attributes = {}) {
  const node = document.createElementNS(svgNS, tag);
  for (const [key, value] of Object.entries(attributes)) {
    if (value !== undefined && value !== null) node.setAttribute(key, value);
  }
  return node;
}

// A number a panel has room for. Axis labels and stat values are read at a
// glance, and 1.24M is read faster than 1,240,000 — but only above a thousand,
// because rounding 847 to 847 helps nobody and rounding it to 0.85k hurts.
export function compact(value) {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  const magnitude = Math.abs(value);
  if (magnitude >= 1_000_000_000) return `${trim(value / 1_000_000_000)}B`;
  if (magnitude >= 1_000_000) return `${trim(value / 1_000_000)}M`;
  if (magnitude >= 1_000) return `${trim(value / 1_000)}k`;
  if (magnitude >= 100) return String(Math.round(value));
  if (magnitude >= 1) return trim(value);
  if (magnitude === 0) return "0";
  return String(Number(value.toPrecision(2)));
}

function trim(value) {
  return String(Number(value.toFixed(2)));
}

export function withUnit(value, unit) {
  return unit ? `${compact(value)} ${unit}` : compact(value);
}

// Durations are read as durations, not as a count of milliseconds: 1.4s beats
// 1400 ms, and 2m 30s beats 150000.
export function duration(ms) {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return "—";
  if (ms < 1) return `${Number(ms.toPrecision(2))}ms`;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  const minutes = Math.floor(ms / 60_000);
  return `${minutes}m ${Math.round((ms % 60_000) / 1000)}s`;
}

export function timeText(value) {
  return new Date(value).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 });
}

// Axis labels across a window: the date is only worth the space when the window
// spans more than a day, and seconds only when it spans less than a minute.
export function axisTimeText(value, spanMS) {
  const date = new Date(value);
  if (spanMS > 3 * 86_400_000) return date.toLocaleDateString([], { month: "short", day: "numeric" });
  if (spanMS > 86_400_000) return date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  if (spanMS < 120_000) return date.toLocaleTimeString([], { minute: "2-digit", second: "2-digit" });
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function relativeTime(value) {
  const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

export function shortID(value) {
  return value ? `${value.slice(0, 12)}${value.length > 12 ? "…" : ""}` : "—";
}

// A stable colour per series key, so the same service is the same colour in
// every panel on the dashboard and across a reload. Assigning by array index
// instead would recolour everything the moment a new service appeared.
export function colourFor(key, index = 0) {
  if (!key) return palette[index % palette.length];
  let hash = 0;
  for (let i = 0; i < key.length; i++) hash = (hash * 31 + key.charCodeAt(i)) | 0;
  return palette[Math.abs(hash) % palette.length];
}

// Nice round numbers for an axis, so gridlines land on 0, 25, 50 rather than
// 0, 23.7, 47.4.
export function niceScale(min, max, ticks = 4) {
  if (!Number.isFinite(min) || !Number.isFinite(max)) return { min: 0, max: 1, step: 0.25 };
  if (min === max) {
    const pad = Math.abs(min || 1) * 0.1;
    min -= pad;
    max += pad;
  }
  // A chart of positive quantities that does not include zero exaggerates every
  // change on it. Counts and durations are always positive, so anchor there.
  if (min > 0 && min < max * 0.5) min = 0;
  const raw = (max - min) / ticks;
  const magnitude = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * magnitude).find((s) => s >= raw) ?? magnitude * 10;
  return { min: Math.floor(min / step) * step, max: Math.ceil(max / step) * step, step };
}
