// The picture the tabs render together: read from the painter
// (examples/picture.yaml) by way of the agent, drawn tile by tile as
// the tiles arrive, with the device that rendered each.

import { count, seconds } from "./format.js";

// The painter, by the name the agent exposes it under: where the
// picture is read from, and what starts the next picture.
const PAINTER = "picture";
const STATE_PATH = `apps/${PAINTER}/state`;
const START_PATH = `apps/${PAINTER}/start`;

// How often the picture is read: while one is being rendered, and
// otherwise, to see a picture someone else started.
const WATCH_MS = 1000;
const IDLE_MS = 10000;

// How often the button's count of seconds is brought up to date.
const TICK_MS = 1000;
const MS_PER_SECOND = 1000;

// What the agent answers for a Service it does not expose: a cluster
// without the painter, whose page shows no picture.
const NOT_EXPOSED = 404;

// The canvas's own size in pixels, whatever size the page shows the
// canvas at.
const CANVAS_PX = 480;

// A pixel's value is two hex digits: how many turns the pixel's point
// took to leave the set, or STAYED for a point that never left. LATE
// is the count that gets the palette's last colour.
const HEX = 16;
const DIGITS = 2;
const STAYED = 255;
const LATE = 64;

// The colours: of a point that stayed, and of a point that left, from
// one that left at once to one that left late. The blues and the amber
// are the page's own.
const INSIDE = "#070b16";
const PALETTE = [
  [0.0, [9, 16, 40]],
  [0.3, [28, 58, 138]],
  [0.45, [59, 111, 212]],
  [0.6, [126, 166, 244]],
  [0.75, [233, 240, 255]],
  [0.88, [255, 176, 32]],
  [1.0, [194, 65, 12]],
];

// What lies over the tiles of other devices while one device's tiles
// are shown, and the line around the tile under the pointer.
const VEIL = "rgba(7, 11, 22, 0.78)";
const MARK = "#ffb020";
const MARK_WIDTH = 3;

// How many devices are named before the rest are counted.
const DEVICES_NAMED = 8;

// The part of the page's address that names a node, which the page's
// main script (site.js) reads to show the node.
const NODE_KEY = "node";

const byId = (id) => document.getElementById(id);

// The picture as far as the page has read it, and what of it the page
// was last told: whether it is being rendered, how long it took, when
// the next may start, and anything the painter had to say.
const picture = { number: 0, side: 0, tiles: [], rendering: false, seconds: 0, note: "" };
let restUntil = 0;

// What the visitor is pointing at: a device, and the tile when the
// device was pointed at by one of its tiles. And whether every device
// is listed, or only the first few.
let pickedNode = null;
let pickedTile = null;
let everyDevice = false;

// What the painter said the last time the button was pressed and the
// painter would not start, which is shown for a while.
let refusal = "";
const REFUSAL_MS = 6000;

let timer = null;

// An element with a class and a text.
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

// A node's name as a link that shows the node.
function nodeLink(name) {
  const link = make("a", "node-link mono", name);
  link.href = `#${NODE_KEY}=${encodeURIComponent(name)}`;
  return link;
}

// The colour of each count of turns that has been worked out, since a
// picture has thousands of pixels and a few dozen counts.
const colours = new Map();

// The colour of a point that left after some turns.
function colour(turns) {
  if (!colours.has(turns)) {
    colours.set(turns, mix(turns));
  }
  return colours.get(turns);
}

// Work out the colour of a count of turns: the palette's colour at the
// square root of the count's share of LATE, mixed from the two stops
// on either side. The root spreads the first few turns, where most of
// a picture is, over more colours.
function mix(turns) {
  if (turns >= STAYED) {
    return INSIDE;
  }
  const at = Math.min(1, Math.sqrt(turns / LATE));
  const above = PALETTE.findIndex(([stop]) => stop >= at);
  if (above <= 0) {
    return `rgb(${PALETTE[0][1].join(",")})`;
  }
  const [fromStop, from] = PALETTE[above - 1];
  const [toStop, to] = PALETTE[above];
  const part = (at - fromStop) / (toStop - fromStop);
  const mixed = from.map((value, index) => Math.round(value + (to[index] - value) * part));
  return `rgb(${mixed.join(",")})`;
}

// Where a tile is on the canvas: its left, its top and its width.
function place(tile) {
  const width = CANVAS_PX / picture.side;
  return [(tile % picture.side) * width, Math.floor(tile / picture.side) * width, width];
}

// Draw one tile. A tile is square, of as many pixels as the pod that
// rendered it chose, and is stretched over the tile's place.
function drawTile({ tile, pixels }) {
  const context = byId("picture").getContext("2d");
  const [left, top, width] = place(tile);
  const across = Math.round(Math.sqrt(pixels.length / DIGITS));
  for (let index = 0; index < across * across; index++) {
    const turns = parseInt(pixels.slice(index * DIGITS, (index + 1) * DIGITS), HEX);

    // The pixel's edges, each rounded on its own so that neighbours
    // meet without a gap or an overlap.
    const column = index % across;
    const row = Math.floor(index / across);
    const x = Math.round(left + (column * width) / across);
    const y = Math.round(top + (row * width) / across);
    const right = Math.round(left + ((column + 1) * width) / across);
    const bottom = Math.round(top + ((row + 1) * width) / across);
    context.fillStyle = colour(turns);
    context.fillRect(x, y, right - x, bottom - y);
  }
}

// Draw the picture again from nothing.
function drawAll() {
  byId("picture").getContext("2d").clearRect(0, 0, CANVAS_PX, CANVAS_PX);
  picture.tiles.forEach(drawTile);
}

// Draw the veil: nothing when no device is picked, and otherwise a
// shade over every tile that another device rendered, and a line
// around the tile under the pointer.
function drawVeil() {
  const context = byId("picture-veil").getContext("2d");
  context.clearRect(0, 0, CANVAS_PX, CANVAS_PX);
  if (!pickedNode) {
    return;
  }
  context.fillStyle = VEIL;
  for (const { tile, node } of picture.tiles) {
    if (node !== pickedNode) {
      const [left, top, width] = place(tile);
      context.fillRect(left, top, width, width);
    }
  }
  if (pickedTile === null) {
    return;
  }
  const [left, top, width] = place(pickedTile);
  context.strokeStyle = MARK;
  context.lineWidth = MARK_WIDTH;
  context.strokeRect(
    left + MARK_WIDTH / 2,
    top + MARK_WIDTH / 2,
    width - MARK_WIDTH,
    width - MARK_WIDTH,
  );
}

// The devices that rendered a tile, with how many each rendered, the
// busiest first.
function devices() {
  const tiles = new Map();
  for (const { node } of picture.tiles) {
    tiles.set(node, (tiles.get(node) ?? 0) + 1);
  }
  return [...tiles].sort((one, other) => other[1] - one[1] || one[0].localeCompare(other[0]));
}

// "1 tile", "2 tiles".
function plural(number, word) {
  return `${count(number)} ${word}${number === 1 ? "" : "s"}`;
}

// Say what the picture is: how far along, or how long it took.
function renderCaption() {
  const tiles = picture.tiles.length;
  const by = `by ${plural(devices().length, "device")}`;
  let text = "No picture has been rendered yet.";
  if (picture.rendering) {
    const all = picture.side * picture.side;
    text = `${count(tiles)} of ${plural(all, "tile")} ${by}, ${seconds(picture.seconds)} so far.`;
  } else if (tiles > 0) {
    text = `${plural(tiles, "tile")} ${by} in ${seconds(picture.seconds)}.`;
  }
  byId("picture-caption").textContent = [text, picture.note, refusal].filter(Boolean).join(" ");
}

// Name the device whose tile is under the pointer.
function renderPicked() {
  const line = byId("picture-picked");
  if (pickedTile === null || !pickedNode) {
    line.replaceChildren();
    return;
  }
  line.replaceChildren(`Tile ${pickedTile + 1} was rendered by `, nodeLink(pickedNode), ".");
}

// List the devices under the picture. Pointing at one shows its tiles.
function renderDevices() {
  const all = devices();
  const shown = everyDevice ? all : all.slice(0, DEVICES_NAMED);
  const keys = shown.map(([node, tiles]) => {
    const key = make("span", "key");
    key.append(nodeLink(node), make("span", "tiles", `×${tiles}`));
    key.addEventListener("pointerenter", () => pick(node, null));
    key.addEventListener("pointerleave", () => pick(null, null));
    return key;
  });

  // The rest, behind a button that lists them.
  if (all.length > shown.length) {
    const more = make("button", "linklike", `${count(all.length - shown.length)} more…`);
    more.addEventListener("click", () => {
      everyDevice = true;
      renderDevices();
    });
    keys.push(more);
  }
  byId("picture-devices").replaceChildren(...keys);
}

// Say on the button what pressing it would do now.
function renderButton() {
  const button = byId("paint");
  const rest = Math.ceil((restUntil - performance.now()) / MS_PER_SECOND);
  button.disabled = picture.rendering || rest > 0;
  if (picture.rendering) {
    button.textContent = "Rendering…";
  } else if (rest > 0) {
    button.textContent = `Again in ${rest} s`;
  } else {
    button.textContent = "Render a picture";
  }
}

// Show one device's tiles, or every tile when no device is given.
function pick(node, tile) {
  if (node === pickedNode && tile === pickedTile) {
    return;
  }
  pickedNode = node;
  pickedTile = tile;
  drawVeil();
  renderPicked();
}

// Pick the device whose tile is under a pointer, or nothing when the
// tile there is not rendered yet.
function pickAt(event) {
  if (picture.side === 0) {
    return;
  }
  const box = event.currentTarget.getBoundingClientRect();
  const column = Math.floor(((event.clientX - box.left) / box.width) * picture.side);
  const row = Math.floor(((event.clientY - box.top) / box.height) * picture.side);
  const found = picture.tiles.find(({ tile }) => tile === row * picture.side + column);
  pick(found ? found.node : null, found ? found.tile : null);
}

// Take what the painter said of the picture: the tiles the page did
// not have, or a whole new picture.
function take(answer) {
  const same = answer.number === picture.number && answer.from > 0;
  if (!same) {
    // Another picture, or the same one from its first tile: nothing
    // the page has drawn or picked is of this picture.
    picture.tiles = [];
    pickedNode = null;
    pickedTile = null;
    everyDevice = false;
  }
  picture.number = answer.number;
  picture.side = answer.side;
  picture.tiles = picture.tiles.slice(0, answer.from).concat(answer.tiles);
  picture.rendering = answer.rendering;
  picture.seconds = answer.seconds;
  picture.note = answer.note;
  restUntil = performance.now() + answer.rest * MS_PER_SECOND;

  // Only the new tiles are drawn when the picture is the one already
  // on the canvas, and the devices are listed again only when there is
  // a tile more to count: a list rebuilt under a pointer loses the
  // pointer.
  if (same) {
    answer.tiles.forEach(drawTile);
  } else {
    drawAll();
  }
  if (!same || answer.tiles.length > 0) {
    drawVeil();
    renderPicked();
    renderDevices();
  }
  renderCaption();
  renderButton();
  byId("picture-panel").hidden = false;
}

// Read the picture again after a while.
function readIn(ms) {
  clearTimeout(timer);
  timer = setTimeout(read, ms);
}

// Read the picture, and the tiles the page does not have. A read that
// fails is tried again at the page's usual pace: the painter may be
// starting, or the agent may have had enough of this address for now.
async function read() {
  if (document.hidden) {
    readIn(IDLE_MS);
    return;
  }
  try {
    const have = `${picture.number}/${picture.tiles.length}`;
    const response = await fetch(`${STATE_PATH}/${have}`, { cache: "no-store" });
    if (response.status === NOT_EXPOSED) {
      return;
    }
    if (response.ok) {
      take(await response.json());
    }
  } catch {
    // The next read tries again.
  }
  readIn(picture.rendering ? WATCH_MS : IDLE_MS);
}

// Ask the painter for the next picture. The painter refuses while a
// picture is being rendered and for a while after, and what it says
// then is shown.
async function paint() {
  const button = byId("paint");
  button.disabled = true;
  refusal = "";
  try {
    const response = await fetch(START_PATH, { cache: "no-store" });
    if (response.ok) {
      take(await response.json());
    } else {
      refusal = (await response.text()).trim();
    }
  } catch (error) {
    refusal = `The painter did not answer: ${error.message}.`;
  }
  renderCaption();
  renderButton();
  readIn(WATCH_MS);

  // What the painter said goes when it has been read.
  if (refusal) {
    setTimeout(() => {
      refusal = "";
      renderCaption();
    }, REFUSAL_MS);
  }
}

function start() {
  for (const id of ["picture", "picture-veil"]) {
    byId(id).width = CANVAS_PX;
    byId(id).height = CANVAS_PX;
  }

  // A mouse picks the tile it is over and lets go when it leaves. A
  // finger picks the tile it touches, which stays picked.
  const canvas = byId("picture");
  canvas.addEventListener("pointermove", pickAt);
  canvas.addEventListener("pointerdown", pickAt);
  canvas.addEventListener("pointerleave", (event) => {
    if (event.pointerType === "mouse") {
      pick(null, null);
    }
  });

  byId("paint").addEventListener("click", paint);
  setInterval(renderButton, TICK_MS);
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) {
      read();
    }
  });
  read();
}

start();
