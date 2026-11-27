// The site's front page: the cluster, read from the agent every few
// seconds and shown as a globe, a topology, some numbers and a feed.

import { MACHINE, createGlobe } from "./globe.js";
import { colors, createTopology } from "./topology.js";
import {
  NONE,
  bytes,
  city,
  count,
  device,
  duration,
  flag,
  known,
  median,
  seconds,
} from "./format.js";

// Where the agent says what the cluster is, how often the page asks,
// and the tables the globe is drawn from.
const CLUSTER_PATH = "api/cluster";
const REFRESH_MS = 3000;
const LAND_PATH = "land.json";
const ZONES_PATH = "zones.json";

// The Service a visitor may send a request to, by the name the agent
// exposes it under, and where its answer is read from.
const ASK_NAME = "counter";
const ASK_PATH = `apps/${ASK_NAME}/json`;

// How many times a request is sent before the page gives up, and the
// wait between tries. A pod whose tab has just closed fails the
// request that was sent to it, and the next try goes to another pod.
const ASK_TRIES = 3;
const ASK_RETRY_MS = 300;

// How much of each list is shown.
const EVENTS_SHOWN = 8;
const ANSWERS_SHOWN = 5;
const FLEET_SHOWN = 6;
const DEVICES_RANKED = 5;
const FLAGS_SHOWN = 14;

// The boot chart's bars: one for each second up to the last, which
// takes every boot longer than that.
const BOOT_BARS = 10;

const SECONDS_PER_HOUR = 3600;

// The kinds of event (internal/agent/ledger.go).
const EventKind = Object.freeze({
  JOIN: "join",
  LEAVE: "leave",
  START: "start",
  MOVE: "move",
});

const byId = (id) => document.getElementById(id);

// What marks a resident beside its name, and what a resident is, for
// someone who picked one.
const RESIDENT_TAG = "resident";
const RESIDENT_NOTE =
  "A headless browser on the cluster's own machine, there so the cluster is never empty. It gives its place up when the cluster is full.";

// The parts of a line of YAML: a comment, and a key with what follows
// its colon.
const YAML_COMMENT = /^(\s*)(#.*)$/;
const YAML_KEY = /^(\s*(?:- )?)([\w.-]+)(:)(.*)$/;

// The views, once the globe's tables are read, the cluster as last
// read, the name of the dot picked on the globe, and the answers to
// this visitor's requests.
let globe = null;
let topology = null;
let zones = {};
let lastView = null;
let pickedName = null;
const answers = [];

// The node a visitor came to see: the name after this in the page's
// address, which a node's own tab puts in its link to this page.
const NODE_KEY = "node";
let showFirst = new URLSearchParams(location.hash.slice(1)).get(NODE_KEY);

// Make an element with a class and some text.
function make(tag, className, text) {
  const element = document.createElement(tag);
  if (className) {
    element.className = className;
  }
  if (text !== undefined) {
    element.textContent = text;
  }
  return element;
}

// A pod's name without the namespace everything is in by default.
function shortPod(name) {
  return name.replace(/^default\//, "");
}

// Seconds from a time of the agent's to the agent's now.
function since(view, time) {
  return (Date.parse(view.now) - Date.parse(time)) / 1000;
}

// Set a tile's number and the line under it.
function tile(name, number, sub) {
  byId(`tile-${name}`).textContent = number;
  byId(`tile-${name}-sub`).textContent = sub ?? "";
}

function renderTiles(view) {
  const pods = view.nodes.flatMap((node) => node.pods);
  const running = pods.filter((pod) => pod.running).length;
  const starting = pods.length - running;
  const residents = view.nodes.filter((node) => node.resident).length;
  tile(
    "nodes",
    view.nodes.length,
    `room for ${count(view.room)} more` + (residents > 0 ? `, ${residents} the cluster's own` : ""),
  );
  tile(
    "pods",
    running,
    starting > 0 ? `${starting} starting` : `${count(view.totals.podsStarted)} started in all`,
  );

  // The countries the nodes are in.
  const countries = new Set();
  for (const node of view.nodes) {
    const code = country(node);
    if (code) {
      countries.add(code);
    }
  }
  const flags = [...countries].sort().slice(0, FLAGS_SHOWN).map(flag).join(" ");
  tile("countries", countries.size, flags);

  const boots = view.boots.map((boot) => boot.seconds);
  const middle = median(boots);
  tile(
    "boot",
    middle === null ? NONE : seconds(middle),
    boots.length > 0 ? `fastest ${seconds(Math.min(...boots))}` : "no pod has started yet",
  );

  tile(
    "age",
    duration(since(view, view.totals.since)),
    `this agent up ${duration(since(view, view.started))}`,
  );

  // The time visitors' tabs have been nodes: those that left, and those
  // here now. A resident lends nothing: it is the machine's own.
  const live = view.nodes
    .filter((node) => !node.resident)
    .reduce((sum, node) => sum + since(view, node.joined), 0);
  const hours = (view.totals.nodeSeconds + live) / SECONDS_PER_HOUR;
  tile(
    "lent",
    hours >= 100 ? count(hours) : hours.toFixed(1),
    `${count(view.totals.joins)} joins in all`,
  );

  tile("moved", count(view.totals.podsMoved), "each started again on another node");
  tile(
    "carried",
    bytes(view.totals.bytes),
    `${count(view.totals.asked)} requests asked of the cluster`,
  );
}

// What an event says.
function eventText(event) {
  switch (event.kind) {
    case EventKind.JOIN:
      return `${event.node} joined`;
    case EventKind.LEAVE: {
      const took =
        event.pods > 0 ? `, taking ${event.pods} pod${event.pods === 1 ? "" : "s"} with it` : "";
      return `${event.node} left after ${duration(event.seconds)}${took}`;
    }
    case EventKind.START:
      return `${shortPod(event.pod)} started on ${event.node} in ${seconds(event.seconds)}`;
    case EventKind.MOVE:
      return `${shortPod(event.pod)} lost its tab and is being started elsewhere`;
    default:
      return `${event.kind} ${event.node}`;
  }
}

function renderEvents(view) {
  const shown = view.events.slice(-EVENTS_SHOWN).reverse();
  byId("events").replaceChildren(
    ...shown.map((event) => {
      const item = make("li", `event ${event.kind}`);
      item.append(make("span", "what", eventText(event)));
      item.append(make("span", "when", `${duration(since(view, event.at))} ago`));
      return item;
    }),
  );
  byId("events-empty").hidden = shown.length > 0;
}

// A row of a chart of bars lying down: a name, a bar, a number.
function barRow(name, value, most, color) {
  const row = make("div", "bar-row");
  row.append(make("span", "bar-name", name));
  const track = make("span", "bar-track");
  const bar = make("span", "bar");
  bar.style.width = `${most > 0 ? (100 * value) / most : 0}%`;
  if (color) {
    bar.style.background = color;
  }
  track.append(bar);
  row.append(track);
  row.append(make("span", "bar-value", String(value)));
  return row;
}

function renderBoots(view) {
  byId("boots-count").textContent = view.boots.length;
  byId("boots").hidden = view.boots.length === 0;
  byId("boots-empty").hidden = view.boots.length > 0;

  // One bar a second, the last for everything slower.
  const bars = new Array(BOOT_BARS).fill(0);
  for (const boot of view.boots) {
    bars[Math.min(BOOT_BARS - 1, Math.floor(boot.seconds))]++;
  }
  const most = Math.max(1, ...bars);
  byId("boots").replaceChildren(
    ...bars.map((number, index) => {
      const column = make("div", "column");
      column.append(make("span", "column-value", number > 0 ? String(number) : ""));
      const bar = make("span", "bar");
      bar.style.height = `${(100 * number) / most}%`;
      column.append(bar);
      column.append(
        make("span", "column-name", index === BOOT_BARS - 1 ? `${index}s+` : `${index}s`),
      );
      return column;
    }),
  );

  // The devices, fastest first.
  const byDevice = new Map();
  for (const boot of view.boots) {
    const name = known(boot.model) || known(boot.platform) || "a browser";
    byDevice.set(name, [...(byDevice.get(name) ?? []), boot.seconds]);
  }
  const ranked = [...byDevice.entries()]
    .map(([name, times]) => ({ name, middle: median(times), starts: times.length }))
    .sort((a, b) => a.middle - b.middle)
    .slice(0, DEVICES_RANKED);
  byId("boots-by-device").replaceChildren(
    ...ranked.map((entry) => {
      const item = make("li");
      item.append(make("span", "what", entry.name));
      item.append(
        make(
          "span",
          "when",
          `${seconds(entry.middle)} over ${entry.starts} start${entry.starts === 1 ? "" : "s"}`,
        ),
      );
      return item;
    }),
  );
}

function renderFleet(view) {
  // The devices there are most of.
  const devices = new Map();
  for (const node of view.nodes) {
    devices.set(device(node), (devices.get(device(node)) ?? 0) + 1);
  }
  const sorted = [...devices.entries()].sort((a, b) => b[1] - a[1]);
  const shown = sorted.slice(0, FLEET_SHOWN);
  const rest = sorted.slice(FLEET_SHOWN).reduce((sum, [, number]) => sum + number, 0);
  if (rest > 0) {
    shown.push(["others", rest]);
  }
  const most = Math.max(1, ...shown.map(([, number]) => number));
  byId("fleet").replaceChildren(...shown.map(([name, number]) => barRow(name, number, most)));

  // The records.
  const browsers = new Map();
  for (const node of view.nodes) {
    const name = known(node.browser) || "a browser";
    browsers.set(name, (browsers.get(name) ?? 0) + 1);
  }
  const threads = view.nodes.reduce((sum, node) => sum + node.threads, 0);
  const records = [
    ["most nodes at once", count(view.totals.peakNodes)],
    [
      "longest-lived node",
      view.totals.longestName
        ? `${view.totals.longestName}, ${duration(view.totals.longestSeconds)}`
        : NONE,
    ],
    ["threads behind the tabs", count(threads)],
    [
      "browsers",
      [...browsers.entries()]
        .sort((a, b) => b[1] - a[1])
        .map(([name, number]) => `${name} ${number}`)
        .join(", ") || NONE,
    ],
    ["control plane", `${view.machine.version}, ${view.machine.runtime}`],
  ];
  byId("records").replaceChildren(
    ...records.map(([what, value]) => {
      const item = make("li");
      item.append(make("span", "what", what));
      item.append(make("span", "when", value));
      return item;
    }),
  );
}

function renderLegend(view) {
  const palette = colors(view);
  const counts = new Map();
  for (const node of view.nodes) {
    for (const pod of node.pods) {
      counts.set(pod.app, (counts.get(pod.app) ?? 0) + 1);
    }
  }
  const idle = view.nodes.filter((node) => node.pods.length === 0).length;
  const entries = [...palette.entries()].map(([app, color]) => {
    const entry = make("span", "key");
    const swatch = make("span", "swatch");
    swatch.style.background = color;
    entry.append(swatch, `${app} ${counts.get(app)}`);
    return entry;
  });
  if (idle > 0) {
    const entry = make("span", "key");
    entry.append(make("span", "swatch idle"), `no pod ${idle}`);
    entries.push(entry);
  }
  byId("legend").replaceChildren(...entries);
}

// Where a time zone's city is, as a flag and a name.
function nearZone(zone) {
  return [flag(zones[zone]?.[2]), city(zone)].filter(Boolean).join(" ");
}

// The country a node is in, as its two-letter code: where the agent
// says its address is, or else its time zone's.
function country(node) {
  return node.placed ? node.country : zones[node.zone]?.[2];
}

// Roughly where a node is, as a flag and a city's name.
function near(node) {
  if (!node.placed) {
    return nearZone(node.zone);
  }
  return [flag(node.country), node.city].filter(Boolean).join(" ");
}

// The card of the dot picked on the globe: a node, or the machine.
function renderPicked(view) {
  const card = byId("picked");
  const node = view.nodes.find((each) => each.name === pickedName);
  if (pickedName !== MACHINE && !node) {
    card.hidden = true;
    return;
  }

  // What the card says, as names and values.
  let facts;
  if (node) {
    const pod = node.pods[0];
    const carried = node.pods.reduce((sum, each) => sum + each.down + each.up, 0);
    facts = [
      ["device", device(node)],
      ["browser", known(node.browser) || NONE],
      ["near", near(node) || NONE],
      ["pod", pod ? shortPod(pod.name) + (pod.running ? "" : " (starting)") : "none yet"],
      ["boot", pod?.running ? seconds(pod.boot) : NONE],
      ["carried", carried > 0 ? bytes(carried) : NONE],
      ["a node for", duration(since(view, node.joined))],
    ];
    if (node.resident) {
      facts.push([RESIDENT_TAG, RESIDENT_NOTE]);
    }
  } else {
    const machine = view.machine;
    facts = [
      ["near", nearZone(machine.zone) || NONE],
      ["kubernetes", machine.version],
      ["nodes", `${view.nodes.length}, room for ${count(view.room)} more`],
      ["agent up", duration(since(view, view.started))],
    ];
    if (machine.memoryTotal > 0) {
      facts.push(
        ["memory", `${bytes(machine.memoryAvailable)} free of ${bytes(machine.memoryTotal)}`],
        ["CPUs busy", `${Math.round(100 * machine.busy)}%`],
      );
    }
  }

  byId("picked-name").textContent = pickedName;
  byId("picked-facts").replaceChildren(
    ...facts.flatMap(([name, value]) => [make("dt", "", name), make("dd", "", value)]),
  );
  card.hidden = false;
}

// The Services, each with where it listens and which nodes its pods
// are on.
function renderServices(view) {
  const nodeOf = new Map();
  for (const node of view.nodes) {
    for (const pod of node.pods) {
      nodeOf.set(pod.name, node.name);
    }
  }
  byId("services").replaceChildren(
    ...view.services.map((service) => {
      const item = make("li");
      const what = make("span", "what");
      what.append(make("span", "mono", `svc/${shortPod(service.name)}`));
      what.append(
        make("span", "address mono", ` ${service.clusterIP}:${(service.ports ?? []).join(",")}`),
      );
      item.append(what);
      const nodes = service.pods.map((pod) => nodeOf.get(pod)).filter(Boolean);
      item.append(
        make(
          "span",
          "when",
          `${nodes.length} pod${nodes.length === 1 ? "" : "s"}: ${nodes.join(", ")}`,
        ),
      );
      return item;
    }),
  );
  byId("services-panel").hidden = view.services.length === 0;
}

function renderNodes(view) {
  byId("nodes").replaceChildren(
    ...view.nodes.map((node) => {
      const pod = node.pods[0];
      const carried = node.pods.reduce((sum, each) => sum + each.down + each.up, 0);
      const cells = [
        { text: node.name, mono: true },
        { text: node.resident ? `${device(node)} (${RESIDENT_TAG})` : device(node) },
        { text: known(node.browser) || NONE },
        { text: near(node) || NONE },
        { text: pod ? shortPod(pod.name) + (pod.running ? "" : " (starting)") : NONE, mono: true },
        { text: pod?.running ? seconds(pod.boot) : NONE },
        { text: carried > 0 ? bytes(carried) : NONE },
        { text: duration(since(view, node.joined)) },
      ];
      const row = document.createElement("tr");
      for (const { text, mono } of cells) {
        row.append(make("td", mono ? "mono" : "", text));
      }
      return row;
    }),
  );
  byId("nodes-empty").hidden = view.nodes.length > 0;
  byId("residents-note").hidden = !view.nodes.some((node) => node.resident);
}

function renderAnswers() {
  byId("answers").replaceChildren(
    ...answers.map((answer) => {
      const item = make("li");
      item.append(make("span", "what", answer.text));
      item.append(make("span", "when", answer.took));
      return item;
    }),
  );
}

function render(view) {
  lastView = view;
  renderTiles(view);
  renderEvents(view);
  renderBoots(view);
  renderFleet(view);
  renderLegend(view);
  renderNodes(view);
  renderServices(view);
  globe.update(view);
  topology.update(view);
  renderPicked(view);
  byId("ask-panel").hidden = !view.exposed.includes(ASK_NAME);

  // A page opened from a node's own tab shows that node, once.
  if (showFirst) {
    globe.show(showFirst);
    showFirst = null;
  }
}

// Read the cluster and show it.
async function refresh() {
  if (document.hidden) {
    return;
  }
  try {
    const response = await fetch(CLUSTER_PATH, { cache: "no-store" });
    render(await response.json());
  } catch {
    // The next refresh tries again.
  }
}

// Send a request into the cluster, and show who answered.
async function ask() {
  const button = byId("ask");
  button.disabled = true;
  const began = performance.now();
  let text;
  let node = null;
  for (let attempt = 1; attempt <= ASK_TRIES; attempt++) {
    try {
      const response = await fetch(ASK_PATH, { cache: "no-store" });
      if (!response.ok) {
        throw new Error((await response.text()).trim() || `status ${response.status}`);
      }
      const answer = await response.json();
      node = answer.node;
      text = `visit ${count(answer.visit)}, answered by a pod on ${answer.node}`;
      break;
    } catch (error) {
      text = `no answer: ${error.message}`;
      await new Promise((resolve) => setTimeout(resolve, ASK_RETRY_MS));
    }
  }
  answers.unshift({ text, took: `${Math.round(performance.now() - began)} ms` });
  answers.length = Math.min(answers.length, ANSWERS_SHOWN);
  renderAnswers();

  // The request's way out and back, on the globe and in the topology.
  if (node && lastView) {
    globe.asked(node);
    topology.asked(node);
    topology.update(lastView);
  }
  button.disabled = false;
}

// Colour a listing of YAML: its comments, its keys and what each key
// is set to, with a mark on every line that has the word the listing
// asks to have marked (data-mark).
function colourYaml(listing) {
  const marked = listing.dataset.mark;
  const lines = listing.textContent.split("\n").map((text) => {
    const line = make("span", marked && text.includes(marked) ? "line mark" : "line");
    const comment = YAML_COMMENT.exec(text);
    const key = YAML_KEY.exec(text);
    if (comment) {
      line.append(comment[1], make("span", "comment", comment[2]));
    } else if (key) {
      line.append(key[1], make("span", "key", key[2]), key[3], make("span", "value", key[4]));
    } else {
      line.append(text);
    }
    line.append("\n");
    return line;
  });
  listing.replaceChildren(...lines);
}

async function start() {
  const [land, table] = await Promise.all(
    [LAND_PATH, ZONES_PATH].map((path) => fetch(path).then((response) => response.json())),
  );
  zones = table;
  globe = createGlobe(byId("globe"), land, zones, (name) => {
    // A dot was picked, or the pick let go: the card follows, and the
    // node is shown in the topology too.
    pickedName = name;
    if (!lastView) {
      return;
    }
    renderPicked(lastView);
    if (name && name !== MACHINE) {
      topology.asked(name);
      topology.update(lastView);
    }
  });
  topology = createTopology(byId("topology"));
  byId("ask").addEventListener("click", ask);
  byId("picked-close").addEventListener("click", () => globe.unpick());
  document.querySelectorAll("pre.yaml").forEach(colourYaml);

  await refresh();
  setInterval(refresh, REFRESH_MS);
  document.addEventListener("visibilitychange", refresh);
}

start();
