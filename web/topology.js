// The topology: the cluster's machine in the middle, the nodes in a
// ring around it, each node's pod as a dot in its workload's colour,
// and each Service between the machine and the pods it is in front of.
//
// An SVG, drawn again from nothing each time the cluster is read. With
// a hundred nodes that is a few hundred elements.

import { device } from "./format.js";

const SVG = "http://www.w3.org/2000/svg";

// The drawing's size, and the rings: the Services close in, the nodes
// further out, and the nodes' names past them, each written along its
// node's spoke so that the names take the same room all the way round.
const SIZE = 640;
const CENTER = SIZE / 2;
const SERVICE_RING = 70;
const SERVICE_STEP = 26;
const SERVICE_STEPS = 3;
const NODE_RING = 170;
const LABEL_RING = 190;

// A node is drawn as a phone or as a wider screen, in these sizes.
const PHONE = { width: 13, height: 22, corner: 3.5 };
const SCREEN = { width: 24, height: 16, corner: 3 };

// Names are written beside the nodes while there are few enough to
// read, and a name longer than fits between the ring and the drawing's
// edge is cut short.
const LABELS_UP_TO = 28;
const LABEL_LETTERS = 19;
const LABEL_END = 5;

// More nodes than that are a crowd: small squares, packed into rings
// from the drawing's edge inwards, as large as lets every node in
// before the rings reach the room kept for the Services. The sizes are
// tried largest first, and a square is this far from the next.
const CROWD_OUTER = 300;
const CROWD_INNER = 160;
const CROWD_GAP = 3;
const CROWD_SIZES = [18, 15, 12, 10, 8, 6, 5, 4, 3];

// Services whose pods lie the same way are set at least this far
// apart, in radians, so that their names do not sit on each other.
const SERVICE_APART = 0.9;

// How strongly a Service's lines to its pods are drawn: this much for
// a few, and fainter the more there are, so that a Service in front of
// every node is a haze and not a blot.
const LINK_OPACITY = 0.55;
const LINKS_AT_FULL_OPACITY = 8;

// The colours workloads are given, in the order their names sort, and
// the colour of a node that has no pod.
const PALETTE = [
  "#6ee7a8",
  "#7ea6f4",
  "#f4b86a",
  "#e889c9",
  "#8be0e8",
  "#c7a6f7",
  "#f08a7e",
  "#d6e37a",
];

// The platforms whose devices are held in a hand.
const HANDHELD = new Set(["Android", "iOS"]);

// The dots that move: one from the machine to a node whose pods have
// carried something, a larger one for a visitor's own request, from
// the machine through the Service to the pod that answered, and one
// for each tile of the picture, from the machine, where the painter
// is, to the node that rendered the tile. Each has a colour, a size,
// and how long its way takes.
const PULSE = { color: "var(--accent)", radius: 2.4, ms: 900 };
const ASKED = { color: "#ffb020", radius: 4.5, ms: 1400 };
const TILE = { color: "#5fd39a", radius: 3.4, ms: 700 };

// The event the picture's panel sends for each tile as the tile comes
// in, with the name of the node that rendered the tile as its detail's
// node. The page hands each to the topology.
export const TILE_RENDERED = "tabernetes:tile";

// How long a dot stays where it arrived before it goes.
const LINGER_MS = 500;

function element(name, attributes, text) {
  const made = document.createElementNS(SVG, name);
  for (const [key, value] of Object.entries(attributes ?? {})) {
    made.setAttribute(key, value);
  }
  if (text !== undefined) {
    made.textContent = text;
  }
  return made;
}

// Where an angle and a distance from the middle are.
function polar(angle, distance) {
  return [CENTER + Math.cos(angle) * distance, CENTER + Math.sin(angle) * distance];
}

// Where each of a crowd of nodes goes, and how big each is drawn: the
// rings are filled from the outermost in, each with as many squares as
// fit round it, and the last ring's are spread evenly.
function crowd(count) {
  for (const size of CROWD_SIZES) {
    const rings = [];
    let left = count;
    for (let radius = CROWD_OUTER; radius >= CROWD_INNER && left > 0; radius -= size + CROWD_GAP) {
      const holds = Math.floor((2 * Math.PI * radius) / (size + CROWD_GAP));
      const here = Math.min(holds, left);
      rings.push({ radius, count: here });
      left -= here;
    }
    if (left > 0 && size !== CROWD_SIZES.at(-1)) {
      continue;
    }
    const spots = [];
    for (const ring of rings) {
      for (let index = 0; index < ring.count; index++) {
        spots.push({
          angle: -Math.PI / 2 + (2 * Math.PI * index) / ring.count,
          radius: ring.radius,
        });
      }
    }
    return { size, spots };
  }
  return { size: CROWD_SIZES.at(-1), spots: [] };
}

// A Service's name without the namespace everything is in by default.
function shortName(name) {
  return name.replace(/^default\//, "");
}

// The colour of each workload among the cluster's pods.
export function colors(view) {
  const apps = new Set();
  for (const node of view.nodes) {
    for (const pod of node.pods) {
      apps.add(pod.app);
    }
  }
  const assigned = new Map();
  for (const [index, app] of [...apps].sort().entries()) {
    assigned.set(app, PALETTE[index % PALETTE.length]);
  }
  return assigned;
}

// Make the topology in an <svg>. The returned object is told the
// cluster each time the page reads it.
export function createTopology(svg) {
  svg.setAttribute("viewBox", `0 0 ${SIZE} ${SIZE}`);

  // How much each node's pods had carried at the last reading, and
  // which node to flash.
  let carriedBefore = new Map();
  let flashed = null;

  // Where each node was drawn last, by name, for a dot that sets off
  // between two drawings.
  let drawn = new Map();

  // The dots in flight, and the layer they are drawn in. The layer is
  // kept from one drawing to the next, so that a dot's way is not cut
  // short when the cluster is read again, and the dots are moved a
  // frame at a time from here.
  const flights = [];
  const sky = element("g", { class: "flights" });
  let ticking = false;

  // Where a dot is, a part of the way along the straight pieces between
  // its points.
  function along(points, part) {
    const lengths = [];
    let whole = 0;
    for (let index = 1; index < points.length; index++) {
      const length = Math.hypot(
        points[index][0] - points[index - 1][0],
        points[index][1] - points[index - 1][1],
      );
      lengths.push(length);
      whole += length;
    }
    let left = Math.min(1, Math.max(0, part)) * whole;
    for (const [index, length] of lengths.entries()) {
      if (left <= length || index === lengths.length - 1) {
        const share = length > 0 ? left / length : 0;
        return [
          points[index][0] + (points[index + 1][0] - points[index][0]) * share,
          points[index][1] + (points[index + 1][1] - points[index][1]) * share,
        ];
      }
      left -= length;
    }
    return points.at(-1);
  }

  // Move every dot to where it is now, and drop the ones that arrived a
  // while ago.
  function tick(now) {
    for (const flight of [...flights]) {
      const elapsed = now - flight.started;
      if (elapsed > flight.kind.ms + LINGER_MS) {
        flight.dot.remove();
        flights.splice(flights.indexOf(flight), 1);
        continue;
      }
      const [x, y] = along(flight.points, elapsed / flight.kind.ms);
      flight.dot.setAttribute("cx", x);
      flight.dot.setAttribute("cy", y);
    }
    ticking = flights.length > 0;
    if (ticking) {
      requestAnimationFrame(tick);
    }
  }

  // Send a dot along some points.
  function fly(kind, points) {
    const dot = element("circle", {
      r: kind.radius,
      cx: points[0][0],
      cy: points[0][1],
    });
    dot.style.fill = kind.color;
    sky.append(dot);
    flights.push({ kind, points, dot, started: performance.now() });
    if (!ticking) {
      ticking = true;
      requestAnimationFrame(tick);
    }
  }

  return {
    update(view) {
      const palette = colors(view);

      const layers = {
        spokes: element("g", { class: "spokes" }),
        links: element("g", { class: "links" }),
        nodes: element("g", { class: "nodes" }),
        services: element("g", { class: "services" }),
        labels: element("g", { class: "labels" }),
      };

      // The nodes: a few go evenly around one ring with their names
      // beside them, the oldest at the top, and a crowd is packed into
      // rings of squares.
      const crowded = view.nodes.length > LABELS_UP_TO;
      const packed = crowded ? crowd(view.nodes.length) : null;
      const places = new Map();
      const podNode = new Map();
      const carriedNow = new Map();
      for (const [index, node] of view.nodes.entries()) {
        const spot = packed?.spots[index];
        const angle =
          spot?.angle ?? -Math.PI / 2 + (2 * Math.PI * index) / Math.max(1, view.nodes.length);
        const [x, y] = polar(angle, spot?.radius ?? NODE_RING);
        places.set(node.name, { angle, x, y });
        for (const pod of node.pods) {
          podNode.set(pod.name, node.name);
        }
        const carried = node.pods.reduce((sum, pod) => sum + pod.down + pod.up, 0);
        carriedNow.set(node.name, carried);

        // The spoke, which a crowd goes without, and a dot along it
        // when the node's pods have carried something since the last
        // reading.
        if (!crowded) {
          layers.spokes.append(element("line", { x1: CENTER, y1: CENTER, x2: x, y2: y }));
        }
        if (carried > (carriedBefore.get(node.name) ?? carried)) {
          fly(PULSE, [
            [CENTER, CENTER],
            [x, y],
          ]);
        }

        // The node: a phone or a screen, or one square of a crowd,
        // filled with its pod's colour when the pod is running.
        const shape = crowded
          ? { width: packed.size, height: packed.size, corner: packed.size / 4 }
          : HANDHELD.has(node.platform)
            ? PHONE
            : SCREEN;
        const pod = node.pods[0];
        const color = pod ? palette.get(pod.app) : null;
        const group = element("g", {
          class: `node${node.name === flashed ? " flash" : ""}`,
          transform: `translate(${x},${y})`,
        });
        group.append(
          element("rect", {
            x: -shape.width / 2,
            y: -shape.height / 2,
            width: shape.width,
            height: shape.height,
            rx: shape.corner,
            fill: pod?.running ? color : "none",
            stroke: color ?? "currentColor",
            "stroke-dasharray": pod && !pod.running ? "3 2" : "none",
            class: pod ? "" : "idle",
          }),
        );
        const lines = [`${node.name} (${device(node)})`];
        for (const each of node.pods) {
          lines.push(`${each.name}${each.running ? "" : ", starting"}`);
        }
        group.append(element("title", {}, lines.join("\n")));

        // Pressing a node shows it: the page reads the node's name
        // from its address.
        group.addEventListener("click", () => {
          location.hash = `node=${encodeURIComponent(node.name)}`;
        });
        layers.nodes.append(group);

        if (!crowded) {
          // Along the spoke, reading outwards on the right half of the
          // ring and inwards on the left, so that no name is upside
          // down.
          const [lx, ly] = polar(angle, LABEL_RING);
          const left = Math.cos(angle) < 0;
          const turn = (angle * 180) / Math.PI + (left ? 180 : 0);
          layers.labels.append(
            element(
              "text",
              {
                x: lx,
                y: ly,
                transform: `rotate(${turn} ${lx} ${ly})`,
                "text-anchor": left ? "end" : "start",
                "dominant-baseline": "middle",
              },
              // A long name loses its middle, not its end: the end is
              // what tells two nodes of one device apart.
              node.name.length > LABEL_LETTERS
                ? `${node.name.slice(0, LABEL_LETTERS - LABEL_END - 1)}…${node.name.slice(-LABEL_END)}`
                : node.name,
            ),
          );
        }
      }
      carriedBefore = carriedNow;

      // The Services, each at the middle of the angles of its pods'
      // nodes, with a line to each.
      const hubs = [];
      for (const service of view.services) {
        const targets = service.pods
          .map((pod) => places.get(podNode.get(pod)))
          .filter((place) => place !== undefined);
        if (targets.length === 0) {
          continue;
        }
        const sumX = targets.reduce((sum, place) => sum + Math.cos(place.angle), 0);
        const sumY = targets.reduce((sum, place) => sum + Math.sin(place.angle), 0);
        hubs.push({ service, targets, angle: Math.atan2(sumY, sumX) });
      }

      // Services whose pods lie the same way would sit on top of each
      // other: going round, each is pushed on from the one before it,
      // each takes the next of a few distances in turn, and its name
      // goes above or below it by turns.
      hubs.sort((a, b) => a.angle - b.angle);
      const apart = Math.min(SERVICE_APART, (2 * Math.PI) / Math.max(1, hubs.length));
      for (const [index, hub] of hubs.entries()) {
        if (index > 0 && hub.angle - hubs[index - 1].angle < apart) {
          hub.angle = hubs[index - 1].angle + apart;
        }
        const { service, targets } = hub;
        const [x, y] = polar(hub.angle, SERVICE_RING + (index % SERVICE_STEPS) * SERVICE_STEP);

        // The visitor's request, if a pod behind this Service answered
        // it: a dot from the machine to the Service and on to the node.
        const answered = places.get(flashed);
        if (answered && targets.includes(answered)) {
          fly(ASKED, [
            [CENTER, CENTER],
            [x, y],
            [answered.x, answered.y],
          ]);
        }
        const labelOffset = index % 2 === 0 ? -13 : 20;

        const pod = view.nodes
          .flatMap((node) => node.pods)
          .find((each) => each.name === service.pods[0]);
        const color = palette.get(pod?.app) ?? "currentColor";
        layers.links.append(
          element("line", {
            x1: CENTER,
            y1: CENTER,
            x2: x,
            y2: y,
            stroke: color,
            class: "trunk",
          }),
        );
        const opacity = Math.min(
          LINK_OPACITY,
          (LINK_OPACITY * LINKS_AT_FULL_OPACITY) / targets.length,
        );
        for (const place of targets) {
          layers.links.append(
            element("line", {
              x1: x,
              y1: y,
              x2: place.x,
              y2: place.y,
              stroke: color,
              opacity,
            }),
          );
        }
        const group = element("g", {
          class: "service",
          transform: `translate(${x},${y})`,
        });
        group.append(element("circle", { r: 7, fill: color }));
        group.append(
          element(
            "text",
            { y: labelOffset, "text-anchor": "middle" },
            `svc/${shortName(service.name)}`,
          ),
        );
        layers.services.append(group);
      }

      // The machine.
      const hub = element("g", {
        class: "hub",
        transform: `translate(${CENTER},${CENTER})`,
      });
      hub.append(element("circle", { r: 30 }));
      hub.append(element("text", { y: -3, "text-anchor": "middle" }, "control"));
      hub.append(element("text", { y: 10, "text-anchor": "middle" }, "plane"));

      svg.replaceChildren(
        layers.spokes,
        layers.links,
        layers.nodes,
        layers.services,
        layers.labels,
        hub,
        sky,
      );
      flashed = null;
      drawn = places;
    },

    // A node rendered a tile of the picture: a dot goes from the
    // machine to the node, now, without waiting for the next drawing.
    tile(node) {
      const place = drawn.get(node);
      if (!place) {
        return;
      }
      fly(TILE, [
        [CENTER, CENTER],
        [place.x, place.y],
      ]);
    },

    // A visitor's request was answered by a pod on this node: the node
    // is drawn once with a flash.
    asked(name) {
      flashed = name;
    },
  };
}
