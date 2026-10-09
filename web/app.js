// The dashboard page: fetch /api/dashboard on a timer and redraw.
"use strict";

const REFRESH_MS = 30_000;
const $ = (id) => document.getElementById(id);

let range = "24h";
try {
  range = localStorage.getItem("range") || range;
} catch {
  // Storage can be unavailable (private windows); the default is fine.
}

async function refresh() {
  let data;
  try {
    const response = await fetch(`api/dashboard?range=${encodeURIComponent(range)}`);
    if (!response.ok) throw new Error(response.statusText);
    data = await response.json();
  } catch (err) {
    setStatus(`Cannot reach the server (${err.message})`, true);
    return;
  } finally {
    $("charts").classList.remove("loading");
  }
  render(data);
}

function setStatus(text, stale) {
  $("status").textContent = text;
  $("status").classList.toggle("stale", stale);
}

function render({ latest, month, series, stale_after }) {
  if (latest) {
    const at = new Date(latest.timestamp);
    const stale = (Date.now() - at) / 1000 > stale_after;
    setStatus(`${stale ? "Collection stalled · last" : "Last"} sample ${at.toLocaleTimeString()}`, stale);
    $("power").textContent = latest.power_w == null ? "–" : `${fmtNumber(latest.power_w)} W`;
    $("temperature").textContent = `${fmtNumber(latest.temperature_c)} °C`;
  } else {
    setStatus("Waiting for the first sample", true);
  }

  for (const el of document.querySelectorAll(".month")) el.textContent = month.label;
  $("energy").textContent = fmtEnergy(month.energy_wh);
  $("cost").textContent = month.cost == null ? "" : `${month.currency}${month.cost.toFixed(2)} so far`;
  $("network").textContent = fmtBytes(month.rx_kib + month.tx_kib);
  $("network-split").textContent = `${fmtBytes(month.rx_kib)} received · ${fmtBytes(month.tx_kib)} sent`;

  renderChart($("chart-power"), {
    type: "line",
    label: "Average power draw over time, in watts",
    x: series.t,
    series: [{ name: "Power", color: "var(--series-1)", values: series.power_w }],
    format: (v) => `${fmtNumber(v)} W`,
  });

  renderChart($("chart-temperature"), {
    type: "line",
    label: "Temperature over time, in degrees Celsius",
    x: series.t,
    zero: false, // temperatures sit far from zero; show their variation
    series: [{ name: "Temperature", color: "var(--series-1)", values: series.temperature_c }],
    format: (v) => `${fmtNumber(v)} °C`,
  });

  // One unit for both network series, so they share a single y axis.
  const peak = Math.max(0, ...series.rx_kib_s, ...series.tx_kib_s);
  const { div, unit } = byteScale(peak);
  const scale = (values) => values.map((v) => (v == null ? null : v / div));
  $("network-unit").textContent = `· ${unit}/s`;
  renderChart($("chart-network"), {
    type: "line",
    label: "Network receive and send rate over time",
    x: series.t,
    series: [
      { name: "Received", color: "var(--series-1)", values: scale(series.rx_kib_s) },
      { name: "Sent", color: "var(--series-2)", values: scale(series.tx_kib_s) },
    ],
    format: (v) => `${fmtNumber(v, 2)} ${unit}/s`,
  });
}

for (const button of document.querySelectorAll(".filters button")) {
  button.setAttribute("aria-pressed", button.dataset.range === range);
  button.addEventListener("click", () => {
    range = button.dataset.range;
    try {
      localStorage.setItem("range", range);
    } catch {}
    for (const other of document.querySelectorAll(".filters button")) {
      other.setAttribute("aria-pressed", other === button);
    }
    // Keep the old charts on screen, dimmed, until the new data arrives.
    $("charts").classList.add("loading");
    refresh();
  });
}

refresh();
setInterval(refresh, REFRESH_MS);
