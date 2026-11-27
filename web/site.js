// The site's front page: the cluster, read from the agent every few
// seconds and shown as a globe, a topology, some numbers and a feed.

import { MACHINE, createGlobe } from "./globe.js";
import { TILE_RENDERED, colors, createTopology } from "./topology.js";
import {
  MS_PER_SECOND,
  NONE,
  bytes,
  city,
  count,
  device,
  duration,
  flag,
  known,
  latency,
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
// After the agent restarts, every pod is started again and none is
// ready for some seconds, which the tries are enough to wait out.
const ASK_TRIES = 8;
const ASK_RETRY_MS = 1000;

// What the agent says when the Service has no pod ready, and what the
// page says in its place.
const NO_POD = "connection refused";
const NO_POD_TEXT = "no pod is ready to answer yet; the cluster's pods are starting";

// How much of each list is shown.
const EVENTS_SHOWN = 8;
const ANSWERS_SHOWN = 5;
const FLEET_SHOWN = 6;
const DEVICES_RANKED = 5;
const FLAGS_SHOWN = 14;

// The boot chart's bars: one for each second up to the last, which
// takes every boot longer than that.
const BOOT_BARS = 10;

// The latency chart's bars, each named by the least time a request in
// the bar took, in milliseconds: a bar's requests took that long and
// less than the next bar's time, and the last bar takes every request
// slower still. Each time is twice the one before, since most
// requests are quick and a few take many times as long.
const ASK_BARS_MS = [0, 25, 50, 100, 200, 400, 800, 1600, 3200];

// What the agent answers a request with when no pod answered the
// request (internal/agent/ask.go).
const NO_ANSWER_STATUS = 502;

const SECONDS_PER_HOUR = 3600;

// The kinds of event (internal/agent/ledger.go).
const EventKind = Object.freeze({
  JOIN: "join",
  LEAVE: "leave",
  START: "start",
  MOVE: "move",
});

const byId = (id) => document.getElementById(id);

// The browser the cluster's own machine keeps a few of open with the
// join page, so that the cluster is never without nodes, as the browser
// names itself.
const OWN_BROWSER = "HeadlessChrome";

// How many of a Service's nodes are named before the rest are counted.
const SERVICE_NODES_NAMED = 3;

// The class of a line that says more to whoever points at the line,
// and of a button that reads as a link.
const TIP_CLASS = "tip";
const LINKLIKE_CLASS = "linklike";

const BYTES_PER_GB = 1e9;

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
// address, which a node's own tab puts in its link to this page. The
// node may not be in the first reading of the cluster, so the page
// looks for it over this many readings before giving up.
const NODE_KEY = "node";
const SHOW_TRIES = 5;
let showFirst = null;
let showTriesLeft = 0;

// What starts the id of a node's row in the table of nodes, the
// Services whose every node is listed, and whether the page is to
// scroll to the row of the node it shows next: so for a link pressed
// in the page, and not for the address the page was opened with.
const ROW_PREFIX = "node-row-";
const openServices = new Set();
let scrollToRow = false;

// Take the node to show from the page's address.
function readAddress() {
  showFirst = new URLSearchParams(location.hash.slice(1)).get(NODE_KEY);
  showTriesLeft = SHOW_TRIES;
}

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

// A node's name as a link that shows the node: on the globe, and in
// the table of nodes, where the page scrolls to it.
function nodeLink(name) {
  const link = make("a", "node-link mono", name);
  link.href = `#${NODE_KEY}=${encodeURIComponent(name)}`;
  return link;
}

// A line of text with a node's name in it, as the parts of the line
// with the name made a link.
function withNode(text, name) {
  if (!name || !text.includes(name)) {
    return [text];
  }
  const [before, ...after] = text.split(name);
  return [before, nodeLink(name), after.join(name)];
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

  // Why there is that much room, and how many of the nodes are the
  // cluster's own, for whoever points at the line under the number of
  // nodes.
  const own = view.nodes.filter((node) => node.browser === OWN_BROWSER).length;
  const spare = (view.machine.memoryAvailable / BYTES_PER_GB).toFixed(1);
  const why = [
    view.machine.memoryTotal > 0
      ? `The cluster takes nodes until its machine has less than 1 GB of memory to spare. Each node costs the machine about 10 MB, and ${spare} GB is spare now.`
      : "The cluster takes a set number of nodes.",
    own > 0
      ? `${own} of the nodes are headless browsers the cluster keeps open itself, so that it is never empty.`
      : "",
  ];

  // The line under the number of nodes, underlined with dots to say
  // that pointing at the line shows more. The line's element is made
  // once and kept from one reading to the next, so that a line a
  // visitor has touched stays in focus and goes on showing why.
  byId("tile-nodes").textContent = view.nodes.length;
  const sub = byId("tile-nodes-sub");
  let room = sub.firstElementChild;
  if (!room) {
    room = make("span", TIP_CLASS);
    room.tabIndex = 0;
    sub.replaceChildren(room);
  }
  room.textContent = `room for ${count(view.room)} more`;
  room.dataset.tip = why.filter(Boolean).join(" ");

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

  // The time tabs have been nodes: those that left, and those here now.
  const live = view.nodes.reduce((sum, node) => sum + since(view, node.joined), 0);
  const hours = (view.totals.nodeSeconds + live) / SECONDS_PER_HOUR;
  tile(
    "lent",
    hours >= 100 ? count(hours) : hours.toFixed(1),
    `${count(view.totals.joins)} joins in all`,
  );

  tile("moved", count(view.totals.podsMoved), "each started again on another node");

  // The requests visitors sent with the button, and not what the page
  // itself reads from another exposed Service every second.
  tile(
    "carried",
    bytes(view.totals.bytes),
    `${count(view.totals.askedOf?.[ASK_NAME] ?? 0)} requests asked of the cluster`,
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
      const what = make("span", "what");
      what.append(...withNode(eventText(event), event.node));
      item.append(what);
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

// The columns of a chart of bars standing up: over each bar the number
// the bar stands for, and under each bar a name.
function columns(numbers, names) {
  const most = Math.max(1, ...numbers);
  return numbers.map((number, index) => {
    const column = make("div", "column");
    column.append(make("span", "column-value", number > 0 ? String(number) : ""));
    const bar = make("span", "bar");
    bar.style.height = `${(100 * number) / most}%`;
    column.append(bar);
    column.append(make("span", "column-name", names[index]));
    return column;
  });
}

function renderAsks(view) {
  // The requests sent to the Service the page's button asks, and of
  // those the ones a pod answered. A request no pod answered has no
  // latency to chart: the request was refused at once, or ran out of
  // time.
  const asks = view.asks.filter((ask) => ask.app === ASK_NAME);
  const answered = asks.filter((ask) => ask.status !== NO_ANSWER_STATUS);
  byId("asks-panel").hidden = !view.exposed.includes(ASK_NAME);
  byId("asks-count").textContent = asks.length;
  byId("asks").hidden = answered.length === 0;
  byId("asks-empty").hidden = answered.length > 0;

  // Each answered request goes in the last bar whose time the request
  // took at least.
  const bars = new Array(ASK_BARS_MS.length).fill(0);
  for (const ask of answered) {
    const ms = ask.seconds * MS_PER_SECOND;
    bars[ASK_BARS_MS.findLastIndex((least) => ms >= least)]++;
  }
  // Each bar is named by its time alone, without the unit, which the
  // words over the chart give: nine names with units are wider than a
  // phone's screen.
  const names = ASK_BARS_MS.map((least, index) =>
    index === ASK_BARS_MS.length - 1 ? `${least}+` : String(least),
  );
  byId("asks").replaceChildren(...columns(bars, names));

  // What the bars do not say: the middle, the worst, and how many
  // requests are not in a bar at all.
  const times = answered.map((ask) => ask.seconds);
  const facts = [];
  if (times.length > 0) {
    facts.push(["median", latency(median(times))], ["slowest", latency(Math.max(...times))]);
  }
  if (asks.length > answered.length) {
    facts.push(["no pod answered", `${asks.length - answered.length} of ${asks.length}`]);
  }
  byId("asks-facts").replaceChildren(
    ...facts.map(([what, value]) => {
      const item = make("li");
      item.append(make("span", "what", what));
      item.append(make("span", "when", value));
      return item;
    }),
  );
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
  const names = bars.map((_, index) => (index === BOOT_BARS - 1 ? `${index}s+` : `${index}s`));
  byId("boots").replaceChildren(...columns(bars, names));

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
      // The first few nodes by name, and the rest behind a count that
      // is underlined with dots and opens the rest when it is pressed.
      const open = openServices.has(service.name);
      const named = open ? nodes : nodes.slice(0, SERVICE_NODES_NAMED);
      const list = make("span", "when", `${nodes.length} pod${nodes.length === 1 ? "" : "s"}: `);
      for (const [index, name] of named.entries()) {
        list.append(index > 0 ? ", " : "", nodeLink(name));
      }
      if (nodes.length > SERVICE_NODES_NAMED) {
        const toggle = make(
          "button",
          LINKLIKE_CLASS,
          open ? "show fewer" : `${nodes.length - SERVICE_NODES_NAMED} more`,
        );
        toggle.type = "button";
        toggle.addEventListener("click", () => {
          if (open) {
            openServices.delete(service.name);
          } else {
            openServices.add(service.name);
          }
          renderServices(lastView);
        });
        list.append(open ? " " : " and ", toggle);
      }
      item.append(list);
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
        { text: device(node) },
        { text: known(node.browser) || NONE },
        { text: near(node) || NONE },
        { text: pod ? shortPod(pod.name) + (pod.running ? "" : " (starting)") : NONE, mono: true },
        { text: pod?.running ? seconds(pod.boot) : NONE },
        { text: carried > 0 ? bytes(carried) : NONE },
        { text: duration(since(view, node.joined)) },
      ];
      const row = document.createElement("tr");
      row.id = ROW_PREFIX + node.name;
      row.className = node.name === pickedName ? "lit" : "";
      for (const { text, mono } of cells) {
        row.append(make("td", mono ? "mono" : "", text));
      }
      return row;
    }),
  );
  byId("nodes-empty").hidden = view.nodes.length > 0;
}

function renderAnswers() {
  byId("answers").replaceChildren(
    ...answers.map((answer) => {
      const item = make("li");
      const what = make("span", "what");
      what.append(...withNode(answer.text, answer.node));
      item.append(what);
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
  renderAsks(view);
  renderFleet(view);
  renderLegend(view);
  renderNodes(view);
  renderServices(view);
  globe.update(view);
  topology.update(view);
  renderPicked(view);
  byId("ask-panel").hidden = !view.exposed.includes(ASK_NAME);

  // A page opened from a node's own tab shows that node, once the node
  // is in a reading.
  if (showFirst && (globe.show(showFirst) || --showTriesLeft <= 0)) {
    if (scrollToRow) {
      document
        .getElementById(ROW_PREFIX + showFirst)
        ?.scrollIntoView({ block: "center", behavior: "smooth" });
      scrollToRow = false;
    }
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
      text = error.message.includes(NO_POD) ? NO_POD_TEXT : `no answer: ${error.message}`;
      await new Promise((resolve) => setTimeout(resolve, ASK_RETRY_MS));
    }
  }
  answers.unshift({ text, node, took: latency((performance.now() - began) / MS_PER_SECOND) });
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
    renderNodes(lastView);
    if (name && name !== MACHINE) {
      topology.asked(name);
      topology.update(lastView);
    }
  });
  topology = createTopology(byId("topology"));
  document.addEventListener(TILE_RENDERED, (event) => topology.tile(event.detail.node));
  byId("ask").addEventListener("click", ask);
  byId("picked-close").addEventListener("click", () => globe.unpick());
  document.querySelectorAll("pre.yaml").forEach(colourYaml);

  // The address names a node to show, now and whenever it changes.
  readAddress();
  addEventListener("hashchange", () => {
    readAddress();
    scrollToRow = true;
    if (lastView) {
      render(lastView);
    }
  });

  await refresh();
  setInterval(refresh, REFRESH_MS);
  document.addEventListener("visibilitychange", refresh);
}

start();
