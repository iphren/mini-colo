// Small dependency-free SVG charts, shared by the dashboard and the monthly
// report. renderChart(container, spec) draws a line or bar chart with a
// hover/keyboard readout and a data-table twin of the same numbers.
//
// spec = {
//   type:   "line" | "bar"      bars are stacked when there are 2+ series
//   label:  string              accessible name for the plot
//   x:      number[]            Unix milliseconds, one per point
//   series: [{ name, color, values: (number|null)[] }]
//   format: (value) => string   how to print a value, including its unit
//   zero:   boolean             line charts only; false lets the y axis
//                               start near the data instead of at zero
//   daily:  boolean             x values are whole days, not instants
//   table:  boolean             false omits the data table, for pages that
//                               already show the numbers some other way
// }
"use strict";

const SVG_NS = "http://www.w3.org/2000/svg";
const HEIGHT = 220;
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

function svg(name, attrs, parent) {
  const el = document.createElementNS(SVG_NS, name);
  for (const [key, value] of Object.entries(attrs || {})) el.setAttribute(key, value);
  if (parent) parent.appendChild(el);
  return el;
}

// All text goes in through textContent, never innerHTML, so nothing that
// ends up in a label can be interpreted as markup.
function html(tag, className, text, parent) {
  const el = document.createElement(tag);
  if (className) el.className = className;
  if (text != null) el.textContent = text;
  if (parent) parent.appendChild(el);
  return el;
}

// ---- Formatting -----------------------------------------------------------

function fmtNumber(value, digits = 1) {
  return value.toLocaleString(undefined, { maximumFractionDigits: digits });
}

const BYTE_UNITS = ["KiB", "MiB", "GiB", "TiB"];

// byteScale picks one unit for a whole chart, so its axis ticks stay round.
function byteScale(maxKiB) {
  let div = 1;
  let unit = 0;
  while (maxKiB / div >= 1024 && unit < BYTE_UNITS.length - 1) {
    div *= 1024;
    unit++;
  }
  return { div, unit: BYTE_UNITS[unit] };
}

function fmtBytes(kib) {
  const { div, unit } = byteScale(kib);
  return `${fmtNumber(kib / div, kib / div < 10 ? 2 : 1)} ${unit}`;
}

function fmtEnergy(wh) {
  return wh < 1000 ? `${fmtNumber(wh)} Wh` : `${fmtNumber(wh / 1000, 2)} kWh`;
}

const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });
const fullFmt = new Intl.DateTimeFormat(undefined, {
  weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit",
});
const fullDayFmt = new Intl.DateTimeFormat(undefined, {
  weekday: "short", day: "numeric", month: "short",
});

// ---- Scales ---------------------------------------------------------------

// niceTicks returns about `count` round numbers that cover [min, max].
function niceTicks(min, max, count = 4) {
  if (max <= min) max = min + 1;
  const rough = (max - min) / count;
  const magnitude = 10 ** Math.floor(Math.log10(rough));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * magnitude).find((s) => s >= rough);
  const ticks = [];
  for (let v = Math.floor(min / step) * step; v < max + step; v += step) {
    ticks.push(Number(v.toPrecision(12)));
    if (v >= max) break;
  }
  return ticks;
}

const TICK_STEPS = [
  5 * MINUTE, 15 * MINUTE, 30 * MINUTE, HOUR, 2 * HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR,
  DAY, 2 * DAY, 7 * DAY,
];

// timeTicks returns round local times between from and to, at most `count`.
function timeTicks(from, to, count) {
  const step = TICK_STEPS.find((s) => (to - from) / s <= count) || TICK_STEPS.at(-1);
  const offset = new Date(from).getTimezoneOffset() * MINUTE;
  const ticks = [];
  for (let t = Math.ceil((from - offset) / step) * step + offset; t <= to; t += step) {
    ticks.push(t);
  }
  return { ticks, step };
}

function timeTickLabel(t, step) {
  const d = new Date(t);
  const midnight = d.getHours() === 0 && d.getMinutes() === 0;
  return step >= DAY || midnight ? dayFmt.format(d) : timeFmt.format(d);
}

// ---- Rendering ------------------------------------------------------------

const resizeObserver = new ResizeObserver((entries) => {
  for (const { target } of entries) {
    if (target.clientWidth !== target.chartWidth) draw(target);
  }
});

function renderChart(container, spec) {
  container.chartSpec = spec;
  resizeObserver.observe(container);
  draw(container);
}

function draw(container) {
  const spec = container.chartSpec;
  const tableWasOpen = container.querySelector("details")?.open;
  container.replaceChildren();
  container.chartWidth = container.clientWidth;

  const plot = html("div", "chart-plot", null, container);
  const width = Math.max(container.clientWidth, 240);
  const root = svg("svg", {
    viewBox: `0 0 ${width} ${HEIGHT}`, height: HEIGHT,
    role: "img", "aria-label": spec.label, tabindex: 0,
  }, plot);
  const tooltip = html("div", "chart-tooltip", null, plot);
  tooltip.hidden = true;

  const values = spec.series.flatMap((s) => s.values).filter((v) => v != null);
  if (values.length === 0) {
    const text = svg("text", { x: width / 2, y: HEIGHT / 2, "text-anchor": "middle", class: "empty" }, root);
    text.textContent = "No data for this period yet";
    return;
  }

  const view = { spec, root, tooltip, width, values };
  if (spec.type === "bar") drawBars(view);
  else drawLines(view);

  if (spec.table !== false) drawTable(container, spec, tableWasOpen);
}

// drawAxes lays out the plot area and draws gridlines and tick labels.
// It returns the geometry the marks are drawn against.
function drawAxes(view, yMin, yMax, xTicks) {
  const { spec, root, width } = view;
  const ticks = niceTicks(yMin, yMax);
  const labels = ticks.map(spec.format);
  const margin = {
    top: 8, right: 12, bottom: 24,
    left: Math.max(...labels.map((l) => l.length)) * 6.2 + 12,
  };
  const plotWidth = width - margin.left - margin.right;
  const plotHeight = HEIGHT - margin.top - margin.bottom;
  const y0 = ticks[0];
  const y1 = ticks.at(-1);
  const y = (v) => margin.top + (1 - (v - y0) / (y1 - y0)) * plotHeight;

  ticks.forEach((tick, i) => {
    svg("line", {
      x1: margin.left, x2: width - margin.right, y1: y(tick), y2: y(tick),
      class: i === 0 ? "axis" : "grid",
    }, root);
    const text = svg("text", { x: margin.left - 8, y: y(tick) + 4, "text-anchor": "end" }, root);
    text.textContent = labels[i];
  });
  for (const { at, label } of xTicks(margin, plotWidth)) {
    const text = svg("text", { x: at, y: HEIGHT - 6, "text-anchor": "middle" }, root);
    text.textContent = label;
  }
  return { margin, plotWidth, plotHeight, y, baseline: y(y0) };
}

function drawLines(view) {
  const { spec, root, width, values } = view;
  const { x, series } = spec;
  const first = x[0];
  const span = x.at(-1) - first || 1;

  const yMin = spec.zero === false ? Math.min(...values) : 0;
  const geo = drawAxes(view, yMin, Math.max(...values), (margin, plotWidth) => {
    const { ticks, step } = timeTicks(first, first + span, Math.max(2, Math.floor(plotWidth / 90)));
    return ticks.map((t) => ({
      at: margin.left + ((t - first) / span) * plotWidth,
      label: timeTickLabel(t, step),
    }));
  });
  const px = (i) => geo.margin.left + ((x[i] - first) / span) * geo.plotWidth;

  for (const s of series) {
    // A null value breaks the line, so a gap in the data shows as a gap
    // rather than as a straight line drawn across it.
    const runs = [];
    let run = null;
    s.values.forEach((v, i) => {
      if (v == null) { run = null; return; }
      if (!run) runs.push((run = []));
      run.push([px(i), geo.y(v)]);
    });

    for (const points of runs) {
      const path = points.map(([cx, cy], i) => `${i ? "L" : "M"}${cx.toFixed(1)},${cy.toFixed(1)}`).join("");
      if (series.length === 1) {
        const area = `${path}L${points.at(-1)[0].toFixed(1)},${geo.baseline}L${points[0][0].toFixed(1)},${geo.baseline}Z`;
        svg("path", { d: area, class: "area", fill: s.color }, root);
      }
      // "h0" gives a single isolated point a zero-length segment, which
      // the round line cap turns into a visible dot.
      svg("path", { d: points.length === 1 ? path + "h0" : path, class: "line", stroke: s.color }, root);
    }
  }

  const crosshair = svg("line", {
    y1: geo.margin.top, y2: geo.baseline, class: "crosshair", visibility: "hidden",
  }, root);
  const dots = series.map((s) => svg("circle", { r: 4, class: "dot", fill: s.color, visibility: "hidden" }, root));

  attachReadout(view, {
    count: x.length,
    indexAt: (pointerX) => {
      const t = first + ((pointerX - geo.margin.left) / geo.plotWidth) * span;
      let best = 0;
      for (let i = 1; i < x.length; i++) {
        if (Math.abs(x[i] - t) < Math.abs(x[best] - t)) best = i;
      }
      return best;
    },
    show: (i) => {
      crosshair.setAttribute("x1", px(i));
      crosshair.setAttribute("x2", px(i));
      crosshair.setAttribute("visibility", "visible");
      series.forEach((s, k) => {
        const v = s.values[i];
        dots[k].setAttribute("visibility", v == null ? "hidden" : "visible");
        if (v != null) {
          dots[k].setAttribute("cx", px(i));
          dots[k].setAttribute("cy", geo.y(v));
        }
      });
      return px(i);
    },
    hide: () => {
      crosshair.setAttribute("visibility", "hidden");
      dots.forEach((dot) => dot.setAttribute("visibility", "hidden"));
    },
  });
}

function drawBars(view) {
  const { spec, root } = view;
  const { x, series } = spec;
  const totals = x.map((_, i) => series.reduce((sum, s) => sum + (s.values[i] || 0), 0));

  let band;
  const geo = drawAxes(view, 0, Math.max(...totals), (margin, plotWidth) => {
    band = plotWidth / x.length;
    // Label every bar if there is room, otherwise every 2nd, 5th, ...
    const every = [1, 2, 5, 10].find((n) => band * n >= 22) || 10;
    return x
      .map((t, i) => ({ at: margin.left + (i + 0.5) * band, label: String(new Date(t).getDate()), i }))
      .filter(({ i }) => (i + 1) % every === 0);
  });

  const barWidth = Math.min(24, Math.max(2, band - 2));
  const GAP = 2; // surface-coloured gap between stacked segments
  const group = svg("g", { class: "bars" }, root);
  const bars = x.map((_, i) => {
    const g = svg("g", {}, group);
    const left = geo.margin.left + (i + 0.5) * band - barWidth / 2;
    const last = series.findLastIndex((s) => s.values[i] > 0);
    let top = geo.baseline;
    let sum = 0;
    series.forEach((s, k) => {
      const v = s.values[i];
      if (!(v > 0)) return;
      const bottom = top;
      sum += v;
      top = geo.y(sum);
      const drawnBottom = k > 0 && bottom - top > GAP + 1 ? bottom - GAP : bottom;
      // Only the data end is rounded; the baseline end stays square.
      const r = k === last ? Math.min(4, barWidth / 2, drawnBottom - top) : 0;
      svg("path", {
        d: `M${left},${drawnBottom}V${top + r}q0,${-r} ${r},${-r}h${barWidth - 2 * r}q${r},0 ${r},${r}V${drawnBottom}Z`,
        fill: s.color,
      }, g);
    });
    return g;
  });

  attachReadout(view, {
    count: x.length,
    indexAt: (pointerX) => Math.min(x.length - 1, Math.max(0, Math.floor((pointerX - geo.margin.left) / band))),
    show: (i) => {
      group.classList.add("hovering");
      bars.forEach((g, k) => g.classList.toggle("active", k === i));
      return geo.margin.left + (i + 0.5) * band;
    },
    hide: () => group.classList.remove("hovering"),
  });
}

// attachReadout wires pointer and keyboard input to the tooltip. The chart
// supplies indexAt (which point is under this x?), show (highlight point i
// and return its x) and hide.
function attachReadout(view, { count, indexAt, show, hide }) {
  const { spec, root, tooltip, width } = view;
  let current = -1;

  const select = (i) => {
    current = i;
    const anchor = show(i);

    tooltip.replaceChildren();
    const when = spec.daily ? fullDayFmt.format(spec.x[i]) : fullFmt.format(spec.x[i]);
    html("div", "when", when, tooltip);
    const shown = spec.series.filter((s) => s.values[i] != null);
    if (shown.length === 0) html("div", null, "No data", tooltip);
    for (const s of shown) {
      const row = html("div", null, null, tooltip);
      if (spec.series.length > 1) html("span", "key", null, row).style.setProperty("--key", s.color);
      html("strong", null, spec.format(s.values[i]), row);
      if (spec.series.length > 1) row.append(" " + s.name);
    }
    if (spec.type === "bar" && shown.length > 1) {
      const row = html("div", null, null, tooltip);
      html("strong", null, spec.format(shown.reduce((sum, s) => sum + s.values[i], 0)), row);
      row.append(" total");
    }

    tooltip.hidden = false;
    // The viewBox matches the rendered width, so SVG units are pixels.
    const flip = anchor + 12 + tooltip.offsetWidth > width;
    tooltip.style.left = `${flip ? anchor - 12 - tooltip.offsetWidth : anchor + 12}px`;
  };
  const clear = () => {
    current = -1;
    tooltip.hidden = true;
    hide();
  };

  root.addEventListener("pointermove", (event) => {
    select(indexAt(event.clientX - root.getBoundingClientRect().left));
  });
  root.addEventListener("pointerleave", clear);
  root.addEventListener("blur", clear);
  root.addEventListener("keydown", (event) => {
    const move = { ArrowLeft: -1, ArrowRight: 1 }[event.key];
    if (!move) return;
    event.preventDefault();
    const from = current < 0 ? (move > 0 ? -1 : count) : current;
    select(Math.min(count - 1, Math.max(0, from + move)));
  });
}

// drawTable adds the same numbers as a table, so no value is reachable only
// by hovering.
function drawTable(container, spec, open) {
  const details = html("details", "chart-table no-print", null, container);
  details.open = Boolean(open);
  html("summary", null, "Data table", details);
  const table = html("table", null, null, html("div", "scroll", null, details));

  const head = html("tr", null, null, html("thead", null, null, table));
  html("th", null, spec.daily ? "Day" : "Time", head);
  for (const s of spec.series) html("th", null, s.name, head);

  const body = html("tbody", null, null, table);
  spec.x.forEach((t, i) => {
    const row = html("tr", null, null, body);
    html("td", null, spec.daily ? fullDayFmt.format(t) : fullFmt.format(t), row);
    for (const s of spec.series) {
      html("td", null, s.values[i] == null ? "–" : spec.format(s.values[i]), row);
    }
  });
}
