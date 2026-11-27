// The site's front page: the cluster's nodes, read from the agent every
// few seconds.

// Where the agent says what the cluster is, and how often the page
// asks.
const CLUSTER_PATH = "api/cluster";
const REFRESH_MS = 2000;

// What stands in a cell with nothing to say, and what the agent writes
// where a browser said nothing.
const NONE = "-";
const UNKNOWN = "unknown";

// The lengths of time an age is given in, in seconds, largest first.
const AGE_UNITS = [
  ["d", 86400],
  ["h", 3600],
  ["m", 60],
  ["s", 1],
];

const nodesElement = document.getElementById("nodes");
const emptyElement = document.getElementById("empty");

// An age as kubectl writes one: the largest unit that is not zero.
function age(since) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(since).getTime()) / 1000));
  for (const [unit, length] of AGE_UNITS) {
    if (seconds >= length) {
      return `${Math.floor(seconds / length)}${unit}`;
    }
  }
  return "0s";
}

// A label's value as text.
function known(value) {
  return value && value !== UNKNOWN ? value.replaceAll("-", " ") : NONE;
}

// One row of the table.
function row(node) {
  const cells = [
    { text: node.name, mono: true },
    { text: known(node.model) !== NONE ? known(node.model) : known(node.platform) },
    { text: known(node.browser) },
    { text: node.pods.length > 0 ? node.pods.join(", ") : NONE, mono: true },
    { text: age(node.joined) },
  ];
  const element = document.createElement("tr");
  for (const { text, mono } of cells) {
    const cell = document.createElement("td");
    cell.textContent = text;
    if (mono) {
      cell.className = "mono";
    }
    element.append(cell);
  }
  return element;
}

// Read the cluster and show it.
async function refresh() {
  let view;
  try {
    const response = await fetch(CLUSTER_PATH, { cache: "no-store" });
    view = await response.json();
  } catch {
    // The next refresh tries again.
    return;
  }

  document.getElementById("count-nodes").textContent = view.nodes.length;
  document.getElementById("count-pods").textContent = view.pods;
  document.getElementById("count-peak").textContent = view.peakNodes;
  document.getElementById("count-room").textContent = Math.max(
    0,
    view.maxNodes - view.nodes.length,
  );

  nodesElement.replaceChildren(...view.nodes.map(row));
  emptyElement.hidden = view.nodes.length > 0;
}

refresh();
setInterval(refresh, REFRESH_MS);
