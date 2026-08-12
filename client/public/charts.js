// The renderers.
//
// Every function here takes a host element and a Frame — the generic result
// layout the Go compiler produces (see internal/telemetry/model/view.go) — and
// draws into it. None of them knows what a view is, what SQL ran, or how the
// data was fetched, which is what makes adding a panel that reads an existing
// shape a matter of adding one function to `renderers` below.
//
// Everything is hand-drawn SVG. That is not stubbornness: the whole file is
// smaller than a charting library's stylesheet, it inherits the theme through
// currentColor and the cn-* tokens instead of fighting it, and it has no
// opinion about the DOM around it. The one rule is that a colour or a size
// computed at runtime goes in an inline style, never in a class name — Tailwind
// only emits classes it can see as literal strings.

import { axisTimeText, colourFor, compact, duration, el, niceScale, palette, svg, text, withUnit } from "./core.js";

const WIDTH = 960;
const HEIGHT = 320;
const PAD = { left: 56, right: 16, top: 14, bottom: 28 };

// Which body template a panel needs. The chart shapes all draw into one SVG
// host; the rest are markup with slots, declared in client/ui/components.
export function bodyKind(panel) {
  if (panel === "stat") return "stat";
  if (panel === "gauge") return "gauge";
  if (panel === "bar_gauge") return "bar_gauge";
  if (panel === "waterfall") return "waterfall";
  return "chart";
}

// draw renders a frame and returns the legend entries the caller should show.
// An unknown panel is a bug rather than a user error, so it says so in place
// instead of silently drawing nothing.
export function draw(host, frame, options = {}) {
  const render = renderers[frame.panel];
  if (!render) {
    host.replaceChildren(note(`No renderer for "${frame.panel}".`));
    return [];
  }
  if (!frame.rows?.length) {
    host.replaceChildren(note(options.emptyMessage || "Nothing matched this window."));
    return [];
  }
  return render(host, frame, options) || [];
}

function note(message) {
  return el("div", "grid h-full min-h-40 place-items-center px-4 text-center text-sm text-muted-foreground", message);
}

// ---------------------------------------------------------------------------
// Plot furniture
// ---------------------------------------------------------------------------

function plot(host, description) {
  // No preserveAspectRatio override: the viewBox keeps its ratio and the
  // drawing scales to fit. Stretching it to the host would be closer to a
  // "fills the box" ideal and would also turn every circle into an ellipse and
  // every label into condensed type, which is a worse trade than some unused
  // vertical space.
  const root = svg("svg", {
    viewBox: `0 0 ${WIDTH} ${HEIGHT}`,
    class: "h-full w-full overflow-visible",
    role: "img",
    "aria-label": description,
  });
  host.replaceChildren(root);
  return {
    root,
    left: PAD.left,
    right: WIDTH - PAD.right,
    top: PAD.top,
    bottom: HEIGHT - PAD.bottom,
    get width() { return WIDTH - PAD.left - PAD.right; },
    get height() { return HEIGHT - PAD.top - PAD.bottom; },
  };
}

function label(x, y, value, anchor = "middle", extra = "") {
  const node = svg("text", {
    x, y,
    "text-anchor": anchor,
    class: `fill-muted-foreground text-[11px] ${extra}`,
    style: "font-size:11px",
  });
  node.appendChild(text(value));
  return node;
}

function yAxis(area, scale, format = compact) {
  const nodes = [];
  const ticks = Math.round((scale.max - scale.min) / scale.step);
  for (let i = 0; i <= ticks; i++) {
    const value = scale.min + i * scale.step;
    const y = area.bottom - ((value - scale.min) / (scale.max - scale.min)) * area.height;
    nodes.push(svg("line", { x1: area.left, x2: area.right, y1: y, y2: y, stroke: "currentColor", class: "text-border" }));
    nodes.push(label(area.left - 8, y + 4, format(value), "end"));
  }
  return nodes;
}

function xTimeAxis(area, minX, maxX) {
  const nodes = [];
  const span = maxX - minX;
  const count = 4;
  for (let i = 0; i <= count; i++) {
    const value = minX + (span * i) / count;
    const x = area.left + (area.width * i) / count;
    nodes.push(label(x, area.bottom + 18, axisTimeText(value, span), i === 0 ? "start" : i === count ? "end" : "middle"));
  }
  return nodes;
}

function tooltip(node, value) {
  const title = svg("title");
  title.appendChild(text(value));
  node.appendChild(title);
  return node;
}

// Series in a frame arrive as flat rows. Every time-shaped renderer wants them
// grouped, in a stable order, with one colour each.
function seriesFrom(frame) {
  const keys = frame.series?.length ? frame.series : [...new Set(frame.rows.map((row) => row[1]))];
  const byKey = new Map(keys.map((key, index) => [key, { key, index, colour: colourFor(key, index), points: [] }]));
  for (const [time, key, value] of frame.rows) {
    const series = byKey.get(key);
    if (series) series.points.push({ x: new Date(time).getTime(), y: value });
  }
  for (const series of byKey.values()) series.points.sort((a, b) => a.x - b.x);
  return [...byKey.values()];
}

function extent(values) {
  return [Math.min(...values), Math.max(...values)];
}

function legendFor(series) {
  return series.map((item) => ({ colour: item.colour, label: item.key || "all" }));
}

// ---------------------------------------------------------------------------
// Time series
// ---------------------------------------------------------------------------

function renderTimeseries(host, frame) {
  const series = seriesFrom(frame);
  const points = series.flatMap((item) => item.points);
  if (!points.length) return [];
  const [minX, maxX] = extent(points.map((p) => p.x));
  const scale = niceScale(...extent(points.map((p) => p.y)));
  const area = plot(host, "Time series");
  const x = (value) => (maxX === minX ? area.left + area.width / 2 : area.left + ((value - minX) / (maxX - minX)) * area.width);
  const y = (value) => area.bottom - ((value - scale.min) / (scale.max - scale.min)) * area.height;

  area.root.append(...yAxis(area, scale), ...xTimeAxis(area, minX, maxX));
  for (const item of series) {
    if (!item.points.length) continue;
    area.root.appendChild(svg("polyline", {
      fill: "none",
      stroke: item.colour,
      "stroke-width": 2,
      "stroke-linejoin": "round",
      "stroke-linecap": "round",
      "vector-effect": "non-scaling-stroke",
      points: item.points.map((p) => `${x(p.x)},${y(p.y)}`).join(" "),
    }));
    // Dots stop being helpful once they merge into the line.
    if (item.points.length <= 60) {
      for (const point of item.points) {
        area.root.appendChild(tooltip(
          svg("circle", { cx: x(point.x), cy: y(point.y), r: 2.5, fill: item.colour }),
          `${item.key ? `${item.key} · ` : ""}${withUnit(point.y, frame.unit)} · ${new Date(point.x).toLocaleString()}`,
        ));
      }
    }
  }
  return legendFor(series);
}

function renderBarTimeseries(host, frame) {
  const series = seriesFrom(frame);
  const buckets = [...new Set(frame.rows.map((row) => new Date(row[0]).getTime()))].sort((a, b) => a - b);
  const points = series.flatMap((item) => item.points);
  if (!points.length || !buckets.length) return [];
  const scale = niceScale(0, Math.max(...points.map((p) => p.y)));
  const area = plot(host, "Bar time series");
  const slot = area.width / buckets.length;
  const barWidth = Math.max(1, (slot * 0.8) / series.length);
  const y = (value) => area.bottom - ((value - scale.min) / (scale.max - scale.min)) * area.height;

  area.root.append(...yAxis(area, scale), ...xTimeAxis(area, buckets[0], buckets.at(-1)));
  series.forEach((item, index) => {
    for (const point of item.points) {
      const slotIndex = buckets.indexOf(point.x);
      if (slotIndex < 0) continue;
      const x = area.left + slotIndex * slot + slot * 0.1 + index * barWidth;
      area.root.appendChild(tooltip(svg("rect", {
        x, y: y(point.y), width: barWidth, height: Math.max(0, area.bottom - y(point.y)),
        fill: item.colour, rx: 1,
      }), `${item.key ? `${item.key} · ` : ""}${withUnit(point.y, frame.unit)}`));
    }
  });
  return legendFor(series);
}

// A state timeline is one lane per series, with runs of equal value merged into
// a single block. Merging is the point: twelve identical adjacent buckets are
// one state that lasted twelve buckets, and drawing them separately hides
// exactly the transition you opened the panel to find.
function renderStateTimeline(host, frame) {
  const series = seriesFrom(frame);
  const points = series.flatMap((item) => item.points);
  if (!points.length) return [];
  const [minX, maxX] = extent(points.map((p) => p.x));
  const area = plot(host, "State timeline");
  const laneHeight = Math.min(34, area.height / Math.max(series.length, 1));
  const x = (value) => (maxX === minX ? area.left : area.left + ((value - minX) / (maxX - minX)) * area.width);

  series.forEach((item, index) => {
    const top = area.top + index * laneHeight;
    area.root.appendChild(label(area.left - 8, top + laneHeight / 2 + 4, truncate(item.key || "all", 14), "end"));
    let run = null;
    const flush = (endX) => {
      if (!run) return;
      const left = x(run.from);
      area.root.appendChild(tooltip(svg("rect", {
        x: left, y: top + 3, width: Math.max(2, endX - left), height: laneHeight - 6,
        fill: colourFor(String(run.value)), rx: 3, opacity: 0.85,
      }), `${item.key ? `${item.key} · ` : ""}${withUnit(run.value, frame.unit)} from ${new Date(run.from).toLocaleString()}`));
      run = null;
    };
    for (const point of item.points) {
      if (run && run.value === point.y) continue;
      flush(x(point.x));
      run = { value: point.y, from: point.x };
    }
    flush(area.right);
  });
  area.root.append(...xTimeAxis(area, minX, maxX));
  return [];
}

// Status history keeps every bucket as its own block — the opposite choice from
// the state timeline, and the right one for a periodic check, where "reported
// twelve times" and "reported once and stayed" are different facts.
function renderStatusHistory(host, frame) {
  const series = seriesFrom(frame);
  const buckets = [...new Set(frame.rows.map((row) => new Date(row[0]).getTime()))].sort((a, b) => a - b);
  if (!series.length || !buckets.length) return [];
  const values = frame.rows.map((row) => row[2]);
  const [low, high] = extent(values);
  const area = plot(host, "Status history");
  const laneHeight = Math.min(30, area.height / series.length);
  const slot = area.width / buckets.length;

  series.forEach((item, index) => {
    const top = area.top + index * laneHeight;
    area.root.appendChild(label(area.left - 8, top + laneHeight / 2 + 4, truncate(item.key || "all", 14), "end"));
    for (const point of item.points) {
      const slotIndex = buckets.indexOf(point.x);
      if (slotIndex < 0) continue;
      area.root.appendChild(tooltip(svg("rect", {
        x: area.left + slotIndex * slot + 1, y: top + 3,
        width: Math.max(2, slot - 2), height: laneHeight - 6,
        fill: item.colour, rx: 2,
        opacity: high === low ? 0.85 : 0.35 + 0.6 * ((point.y - low) / (high - low)),
      }), `${item.key ? `${item.key} · ` : ""}${withUnit(point.y, frame.unit)} · ${new Date(point.x).toLocaleString()}`));
    }
  });
  area.root.append(...xTimeAxis(area, buckets[0], buckets.at(-1)));
  return legendFor(series);
}

// ---------------------------------------------------------------------------
// Categorical
// ---------------------------------------------------------------------------

// Horizontal, because the labels are route templates and service names. A
// vertical bar chart of "/api/v1/publishers/{id}/properties" is a chart of
// rotated text with some bars behind it.
function renderBar(host, frame) {
  const rows = frame.rows;
  const max = Math.max(...rows.map((row) => row[1]), 0) || 1;
  const area = plot(host, "Bar chart");
  const rowHeight = Math.min(30, (area.height + PAD.bottom) / rows.length);
  const labelWidth = 150;

  rows.forEach(([category, value], index) => {
    const top = area.top + index * rowHeight;
    const width = Math.max(2, ((value / max) * (area.width + PAD.left - labelWidth)));
    area.root.appendChild(label(labelWidth - 8, top + rowHeight / 2 + 4, truncate(String(category), 22), "end"));
    area.root.appendChild(tooltip(svg("rect", {
      x: labelWidth, y: top + 3, width, height: Math.max(4, rowHeight - 8),
      fill: colourFor(String(category), index), rx: 3,
    }), `${category} · ${withUnit(value, frame.unit)}`));
    area.root.appendChild(label(labelWidth + width + 6, top + rowHeight / 2 + 4, withUnit(value, frame.unit), "start", "fill-foreground"));
  });
  return [];
}

function renderPie(host, frame) {
  const rows = frame.rows;
  const total = rows.reduce((sum, row) => sum + row[1], 0);
  if (!total) return [];
  const size = 300;
  const root = svg("svg", { viewBox: `0 0 ${size} ${size}`, class: "h-full w-full", role: "img", "aria-label": "Pie chart" });
  host.replaceChildren(root);
  const centre = size / 2;
  const outer = 130;
  const inner = 70; // A donut: the hole is where the total goes.
  let angle = -Math.PI / 2;

  rows.forEach(([category, value], index) => {
    const sweep = (value / total) * Math.PI * 2;
    const colour = colourFor(String(category), index);
    root.appendChild(tooltip(svg("path", {
      d: arc(centre, centre, inner, outer, angle, angle + sweep),
      fill: colour,
      class: "stroke-card",
      "stroke-width": 2,
    }), `${category} · ${withUnit(value, frame.unit)} · ${((value / total) * 100).toFixed(1)}%`));
    angle += sweep;
  });

  const totalNode = svg("text", { x: centre, y: centre, "text-anchor": "middle", class: "fill-foreground", style: "font-size:22px;font-weight:600" });
  totalNode.appendChild(text(compact(total)));
  const caption = svg("text", { x: centre, y: centre + 18, "text-anchor": "middle", class: "fill-muted-foreground", style: "font-size:11px" });
  caption.appendChild(text("total"));
  root.append(totalNode, caption);
  return rows.map(([category], index) => ({ colour: colourFor(String(category), index), label: String(category) }));
}

function arc(cx, cy, inner, outer, from, to) {
  // A full circle cannot be expressed as one arc — the start and end points
  // coincide and the renderer draws nothing at all.
  if (to - from >= Math.PI * 2 - 1e-6) to = from + Math.PI * 2 - 1e-4;
  const large = to - from > Math.PI ? 1 : 0;
  const p = (radius, a) => [cx + radius * Math.cos(a), cy + radius * Math.sin(a)];
  const [x1, y1] = p(outer, from);
  const [x2, y2] = p(outer, to);
  const [x3, y3] = p(inner, to);
  const [x4, y4] = p(inner, from);
  return `M${x1} ${y1} A${outer} ${outer} 0 ${large} 1 ${x2} ${y2} L${x3} ${y3} A${inner} ${inner} 0 ${large} 0 ${x4} ${y4} Z`;
}

// ---------------------------------------------------------------------------
// Distribution
// ---------------------------------------------------------------------------

function renderHistogram(host, frame) {
  const rows = frame.rows;
  const counts = rows.map((row) => row[2]);
  const scale = niceScale(0, Math.max(...counts, 1));
  const area = plot(host, "Histogram");
  const slot = area.width / rows.length;
  const y = (value) => area.bottom - ((value - scale.min) / (scale.max - scale.min)) * area.height;

  area.root.append(...yAxis(area, scale));
  rows.forEach(([start, end, count], index) => {
    const x = area.left + index * slot;
    area.root.appendChild(tooltip(svg("rect", {
      x: x + 1, y: y(count), width: Math.max(1, slot - 2), height: Math.max(0, area.bottom - y(count)),
      fill: palette[1], rx: 2, opacity: count ? 0.9 : 0.25,
    }), `${compact(start)}–${compact(end)}${frame.unit ? ` ${frame.unit}` : ""} · ${count} events`));
    // Label every few bars, or the axis becomes a smear.
    if (index % Math.ceil(rows.length / 6) === 0) {
      area.root.appendChild(label(x + slot / 2, area.bottom + 18, compact(start)));
    }
  });
  const last = rows.at(-1);
  area.root.appendChild(label(area.right, area.bottom + 18, `${compact(last[1])}${frame.unit ? ` ${frame.unit}` : ""}`, "end"));
  return [];
}

// The latency heatmap: time across, value buckets up, colour by how many events
// landed in each cell. It is the panel that shows a slow tail as a band that
// was always there rather than as an average that moved a little.
function renderHeatmap(host, frame) {
  const times = [...new Set(frame.rows.map((row) => new Date(row[0]).getTime()))].sort((a, b) => a - b);
  const bands = [...new Set(frame.rows.map((row) => row[1]))].sort((a, b) => a - b);
  if (!times.length || !bands.length) return [];
  const max = Math.max(...frame.rows.map((row) => row[3]), 1);
  const area = plot(host, "Heatmap");
  const cellWidth = area.width / times.length;
  const cellHeight = area.height / bands.length;

  for (const [time, start, end, count] of frame.rows) {
    if (!count) continue;
    const column = times.indexOf(new Date(time).getTime());
    const row = bands.indexOf(start);
    if (column < 0 || row < 0) continue;
    area.root.appendChild(tooltip(svg("rect", {
      x: area.left + column * cellWidth,
      y: area.bottom - (row + 1) * cellHeight,
      width: Math.max(1, cellWidth),
      height: Math.max(1, cellHeight),
      fill: palette[0],
      // Linear opacity would make everything but the busiest cell invisible;
      // the square root keeps a cell with three events visible next to one
      // with three hundred.
      opacity: 0.12 + 0.88 * Math.sqrt(count / max),
    }), `${compact(start)}–${compact(end)}${frame.unit ? ` ${frame.unit}` : ""} · ${count} events · ${new Date(time).toLocaleString()}`));
  }
  const step = Math.ceil(bands.length / 5);
  bands.forEach((start, index) => {
    if (index % step) return;
    area.root.appendChild(label(area.left - 8, area.bottom - index * cellHeight - 2, compact(start), "end"));
  });
  area.root.append(...xTimeAxis(area, times[0], times.at(-1)));
  return [];
}

// ---------------------------------------------------------------------------
// Scatter
// ---------------------------------------------------------------------------

function renderScatter(host, frame, options = {}, connect = false) {
  const timeAxis = frame.fields?.[0]?.type === "time";
  const points = frame.rows.map(([x, y, seriesLabel, id]) => ({
    x: timeAxis ? new Date(x).getTime() : Number(x),
    y: Number(y),
    label: seriesLabel,
    id,
  })).filter((point) => Number.isFinite(point.x) && Number.isFinite(point.y));
  if (!points.length) return [];

  const [minX, maxX] = extent(points.map((p) => p.x));
  const scale = niceScale(...extent(points.map((p) => p.y)));
  const area = plot(host, connect ? "Trend" : "XY chart");
  const x = (value) => (maxX === minX ? area.left + area.width / 2 : area.left + ((value - minX) / (maxX - minX)) * area.width);
  const y = (value) => area.bottom - ((value - scale.min) / (scale.max - scale.min)) * area.height;

  area.root.append(...yAxis(area, scale));
  if (timeAxis) {
    area.root.append(...xTimeAxis(area, minX, maxX));
  } else {
    for (let i = 0; i <= 4; i++) {
      area.root.appendChild(label(area.left + (area.width * i) / 4, area.bottom + 18, compact(minX + ((maxX - minX) * i) / 4)));
    }
  }

  const labels = [...new Set(points.map((point) => point.label))];
  if (connect) {
    for (const key of labels) {
      const line = points.filter((point) => point.label === key).sort((a, b) => a.x - b.x);
      area.root.appendChild(svg("polyline", {
        fill: "none", stroke: colourFor(key, labels.indexOf(key)), "stroke-width": 1.5, opacity: 0.7,
        "vector-effect": "non-scaling-stroke",
        points: line.map((point) => `${x(point.x)},${y(point.y)}`).join(" "),
      }));
    }
  }
  for (const point of points) {
    const dot = svg("circle", {
      cx: x(point.x), cy: y(point.y), r: 3,
      fill: colourFor(point.label, labels.indexOf(point.label)),
      opacity: 0.75,
      class: options.onPoint ? "cursor-pointer" : "",
    });
    tooltip(dot, `${point.label ? `${point.label} · ` : ""}${withUnit(point.y, frame.unit)} · ${timeAxis ? new Date(point.x).toLocaleString() : compact(point.x)}`);
    if (options.onPoint && point.id) dot.addEventListener("click", () => options.onPoint(point.id));
    area.root.appendChild(dot);
  }
  return labels.filter(Boolean).map((key, index) => ({ colour: colourFor(key, index), label: key }));
}

// ---------------------------------------------------------------------------
// OHLC
// ---------------------------------------------------------------------------

// Candlestick and box read the same four columns and differ in what those
// columns mean — open/high/low/close against min/p25/p75/max. Drawing them from
// one function keeps that relationship visible instead of duplicating the axis
// code twice and letting the two drift.
function renderOHLC(host, frame, options, box) {
  const rows = frame.rows.filter((row) => row.slice(1).every((value) => Number.isFinite(value)));
  if (!rows.length) return [];
  const values = rows.flatMap((row) => row.slice(1));
  const scale = niceScale(...extent(values));
  const times = rows.map((row) => new Date(row[0]).getTime());
  const area = plot(host, box ? "Box plot" : "Candlestick");
  const slot = area.width / rows.length;
  const bodyWidth = Math.max(2, Math.min(26, slot * 0.6));
  const y = (value) => area.bottom - ((value - scale.min) / (scale.max - scale.min)) * area.height;

  area.root.append(...yAxis(area, scale), ...xTimeAxis(area, times[0], times.at(-1)));
  rows.forEach(([time, a, b, c, d], index) => {
    const centre = area.left + index * slot + slot / 2;
    const [low, high] = box ? [a, d] : [c, b];
    const [bodyLow, bodyHigh] = box ? [b, c] : [Math.min(a, d), Math.max(a, d)];
    // Green when the bucket closed at or above where it opened; for a box, the
    // spread has no direction, so it takes the neutral series colour.
    const colour = box ? palette[1] : d >= a ? palette[0] : palette[4];

    area.root.appendChild(svg("line", {
      x1: centre, x2: centre, y1: y(high), y2: y(low),
      stroke: colour, "stroke-width": 1.5, "vector-effect": "non-scaling-stroke",
    }));
    const body = svg("rect", {
      x: centre - bodyWidth / 2,
      y: y(bodyHigh),
      width: bodyWidth,
      height: Math.max(1.5, y(bodyLow) - y(bodyHigh)),
      fill: colour,
      opacity: box ? 0.45 : 0.9,
      stroke: colour,
      rx: 1,
    });
    tooltip(body, box
      ? `min ${compact(a)} · p25 ${compact(b)} · p75 ${compact(c)} · max ${compact(d)} · ${new Date(time).toLocaleString()}`
      : `open ${compact(a)} · high ${compact(b)} · low ${compact(c)} · close ${compact(d)} · ${new Date(time).toLocaleString()}`);
    area.root.appendChild(body);
  });
  return [];
}

// ---------------------------------------------------------------------------
// Single value
// ---------------------------------------------------------------------------

// The single-value panels are markup, not drawings — the templates in
// client/ui/components own their layout, and this only fills the slots.
export function fillSingle(body, frame, view = {}) {
  const [value, previous] = frame.rows[0] ?? [0, null];
  const query = view.query || {};
  const unit = frame.unit;
  const valueNode = body.querySelector("[data-stat-value]");
  if (valueNode) valueNode.textContent = unit === "ms" ? duration(value) : withUnit(value, unit);

  // The caption says what the number is being read against — not what the
  // number is. The panel subtitle already says that, and repeating it here
  // spends the one line under a stat saying nothing new.
  const caption = body.querySelector("[data-stat-caption]");
  if (caption) {
    if (query.critical || query.warn) {
      caption.textContent = [
        query.warn ? `warn at ${compact(query.warn)}` : "",
        query.critical ? `critical at ${compact(query.critical)}` : "",
      ].filter(Boolean).join(" · ");
    } else if (previous !== null && previous !== undefined) {
      caption.textContent = `against the previous ${query.range || "window"}`;
    } else {
      caption.textContent = "";
    }
  }

  const delta = body.querySelector("[data-stat-delta]");
  if (delta) {
    delta.className = "font-medium tabular-nums";
    if (previous === null || previous === undefined) {
      delta.textContent = "";
    } else if (previous === 0) {
      // A change from nothing has no percentage. Saying "+100%" would be a
      // number invented to fill the space.
      delta.textContent = value ? "new" : "no change";
      delta.classList.add("text-muted-foreground");
    } else {
      const change = ((value - previous) / Math.abs(previous)) * 100;
      delta.textContent = `${change >= 0 ? "▲" : "▼"} ${Math.abs(change).toFixed(1)}%`;
      delta.classList.add(change >= 0 ? "text-primary" : "text-muted-foreground");
      delta.title = `previous window: ${withUnit(previous, unit)}`;
    }
  }

  const spark = body.querySelector("[data-stat-spark]");
  if (spark) {
    if (frame.spark?.length) sparkline(spark, frame.spark);
    else spark.replaceChildren();
  }

  const fill = body.querySelector("[data-gauge-fill]");
  const arcNode = body.querySelector("[data-gauge-arc]");
  if (fill || arcNode) {
    // Without a declared ceiling the gauge has to invent one, and the obvious
    // candidate — the value itself — makes every gauge permanently full and
    // therefore useless. Scaling to a quarter above the larger of this window
    // and the last one keeps the needle moving: steady sits at 80%, and a
    // doubling visibly fills it.
    const reference = Math.max(value, previous || 0);
    const max = query.max || reference * 1.25 || 1;
    const fraction = Math.max(0, Math.min(1, value / max));
    const colour = thresholdColour(value, query);
    const maxLabel = body.querySelector("[data-gauge-max]");
    if (maxLabel) maxLabel.textContent = query.max ? `of ${withUnit(max, unit)}` : "no set ceiling";
    if (fill) {
      fill.style.width = `${(fraction * 100).toFixed(1)}%`;
      fill.style.background = colour;
    }
    if (arcNode) {
      // The arc path is 157 units long — half a circle of radius 50.
      const length = 157;
      arcNode.setAttribute("stroke-dashoffset", String(length * (1 - fraction)));
      arcNode.setAttribute("stroke", colour);
    }
  }
  return [];
}

function thresholdColour(value, query) {
  if (query.critical && value >= query.critical) return "var(--destructive)";
  if (query.warn && value >= query.warn) return palette[3];
  return "var(--primary)";
}

function sparkline(host, values) {
  const width = 200;
  const height = 40;
  const root = svg("svg", { viewBox: `0 0 ${width} ${height}`, class: "h-full w-full", preserveAspectRatio: "none", "aria-hidden": "true" });
  const [low, high] = extent(values);
  const span = high - low || 1;
  const x = (index) => (index / Math.max(values.length - 1, 1)) * width;
  const y = (value) => height - 2 - ((value - low) / span) * (height - 4);
  const line = values.map((value, index) => `${x(index)},${y(value)}`).join(" ");
  root.appendChild(svg("polyline", {
    points: `0,${height} ${line} ${width},${height}`,
    fill: palette[0], opacity: 0.12, stroke: "none",
  }));
  root.appendChild(svg("polyline", {
    points: line, fill: "none", stroke: palette[0], "stroke-width": 1.5,
    "vector-effect": "non-scaling-stroke", "stroke-linejoin": "round",
  }));
  host.replaceChildren(root);
}

// ---------------------------------------------------------------------------
// Waterfall
// ---------------------------------------------------------------------------

// One trace as a Gantt chart: a row per span, indented by depth, each bar
// placed and sized as a percentage of the whole trace. That proportional
// reading — this span is a third of the request, and it started only after that
// one finished — is the entire reason to draw a trace rather than list it.
export function drawWaterfall(host, trace, options = {}) {
  const template = document.querySelector("[data-span-row-template]");
  if (!template) {
    host.replaceChildren(note("The span row template is missing from this page."));
    return;
  }
  if (!trace?.spans?.length) {
    host.replaceChildren(note("No spans in this trace."));
    return;
  }
  const total = trace.duration_ms || 1;
  host.replaceChildren(...trace.spans.map((span) => {
    const row = template.content.firstElementChild.cloneNode(true);
    const indent = row.querySelector("[data-span-indent]");
    // Indentation is inline because the depth is data — a class name built from
    // a number is one Tailwind never sees.
    indent.style.width = `${Math.min(span.depth, 8) * 12}px`;

    const colour = colourFor(span.service);
    const errored = /error|fatal/i.test(span.severity || "");
    row.querySelector("[data-span-dot]").style.background = errored ? "var(--destructive)" : colour;
    row.querySelector("[data-span-name]").textContent = span.name || "unnamed span";
    row.querySelector("[data-span-service]").textContent = span.service + (span.orphan ? " · detached" : "");
    row.querySelector("[data-span-duration]").textContent = duration(span.duration_ms);

    const bar = row.querySelector("[data-span-bar]");
    bar.style.left = `${Math.max(0, Math.min(100, (span.offset_ms / total) * 100))}%`;
    // A zero-duration span still has to be visible, so it gets a minimum width.
    bar.style.width = `${Math.max(0.6, Math.min(100, (span.duration_ms / total) * 100))}%`;
    bar.style.background = errored ? "var(--destructive)" : colour;
    bar.style.opacity = span.orphan ? "0.5" : "0.9";
    row.title = `${span.name} · ${span.service} · ${duration(span.duration_ms)} · starts +${duration(span.offset_ms)}`;

    if (options.onSpan && span.id) {
      row.classList.add("cursor-pointer");
      row.addEventListener("click", () => options.onSpan(span.id));
    }
    return row;
  }));
}

// A waterfall panel carries a whole trace in one row rather than a table of
// numbers, so it renders through the same function the traces page uses.
function renderWaterfallPanel(host, frame, options) {
  const trace = frame.rows[0]?.[0];
  const root = host.closest("[data-panel-body]") || host;
  const id = root.querySelector("[data-trace-id]");
  const time = root.querySelector("[data-trace-duration]");
  const services = root.querySelector("[data-trace-services]");
  if (id) id.textContent = trace?.trace_id ? trace.trace_id.slice(0, 16) : "";
  if (time) time.textContent = trace ? duration(trace.duration_ms) : "";
  if (services) services.textContent = trace?.services?.join(" · ") || "";
  drawWaterfall(root.querySelector("[data-waterfall]") || host, trace, options);
  return [];
}

function truncate(value, max) {
  return value.length > max ? `${value.slice(0, max - 1)}…` : value;
}

// Panel name to renderer. The whole extensibility story: a new panel over an
// existing shape is one entry here.
const renderers = {
  timeseries: renderTimeseries,
  bar_timeseries: renderBarTimeseries,
  state_timeline: renderStateTimeline,
  status_history: renderStatusHistory,
  bar: renderBar,
  pie: renderPie,
  histogram: renderHistogram,
  heatmap: renderHeatmap,
  scatter: (host, frame, options) => renderScatter(host, frame, options, false),
  trend: (host, frame, options) => renderScatter(host, frame, options, true),
  candlestick: (host, frame, options) => renderOHLC(host, frame, options, false),
  box: (host, frame, options) => renderOHLC(host, frame, options, true),
  stat: (host, frame, options) => fillSingle(host, frame, options.view),
  gauge: (host, frame, options) => fillSingle(host, frame, options.view),
  bar_gauge: (host, frame, options) => fillSingle(host, frame, options.view),
  waterfall: renderWaterfallPanel,
};

// describe puts a query into a sentence, for the subtitle of a panel. A
// dashboard of numbers whose definitions live only inside an edit dialog is a
// dashboard people misread.
//
// It needs the panel as well as the query, because the same query means
// different things under different panels: a histogram with agg "count" is not
// counting events, it is distributing a value, and saying otherwise would
// describe a chart nobody is looking at.
export function describe(query = {}, panel = "") {
  const parts = [];
  const value = fieldLabel(query.value);
  switch (panel) {
    case "histogram":
    case "heatmap":
      parts.push(`distribution of ${value}`);
      break;
    case "candlestick":
      parts.push(`${value} open, high, low and close`);
      break;
    case "box":
      parts.push(`${value} spread`);
      break;
    case "scatter":
    case "trend":
      parts.push(`${value} against ${fieldLabel(query.x)}`);
      break;
    case "waterfall":
      parts.push(query.trace_id ? "one trace" : `the ${query.order === "slowest" ? "slowest" : "most recent"} trace`);
      break;
    default: {
      const agg = query.agg || "count";
      parts.push(agg === "count" ? "count of events" : `${agg} of ${value}`);
    }
  }
  if (query.signal) parts.push(`in ${query.signal}`);
  if (query.group_by && panel !== "waterfall") parts.push(`by ${fieldLabel(query.group_by)}`);
  if (query.range && query.range !== "all") parts.push(`over the last ${query.range}`);
  if (query.range === "all") parts.push("over everything retained");
  if (query.filters?.length) parts.push(`· ${query.filters.length} filter${query.filters.length > 1 ? "s" : ""}`);
  return parts.join(" ");
}

export function fieldLabel(ref = "") {
  return ref.startsWith("attr:") ? ref.slice(5) : ref.replace(/_/g, " ");
}
