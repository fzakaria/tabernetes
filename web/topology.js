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

// How long a dot takes from the machine to a node along its spoke.
const PULSE_SECONDS = 0.9;

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

  return {
    update(view) {
      const palette = colors(view);
      const layers = {
        spokes: element("g", { class: "spokes" }),
        links: element("g", { class: "links" }),
        pulses: element("g", { class: "pulses" }),
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
          const pulse = element("circle", { r: 2.4 });
          pulse.append(
            element("animateMotion", {
              dur: `${PULSE_SECONDS}s`,
              repeatCount: 1,
              fill: "freeze",
              path: `M${CENTER},${CENTER} L${x},${y}`,
            }),
          );
          layers.pulses.append(pulse);
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
        const lines = [`${node.name} (${device(node)}${node.resident ? ", a resident" : ""})`];
        for (const each of node.pods) {
          lines.push(`${each.name}${each.running ? "" : ", starting"}`);
        }
        group.append(element("title", {}, lines.join("\n")));
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
              node.name.length > LABEL_LETTERS
                ? `${node.name.slice(0, LABEL_LETTERS - 1)}…`
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
        const labelOffset = index % 2 === 0 ? -13 : 20;

        const pod = view.nodes
          .flatMap((node) => node.pods)
          .find((each) => each.name === service.pods[0]);
        const color = palette.get(pod?.app) ?? "currentColor";
        layers.links.append(
          element("line", { x1: CENTER, y1: CENTER, x2: x, y2: y, stroke: color, class: "trunk" }),
        );
        for (const place of targets) {
          layers.links.append(
            element("line", { x1: x, y1: y, x2: place.x, y2: place.y, stroke: color }),
          );
        }
        const group = element("g", { class: "service", transform: `translate(${x},${y})` });
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
      const hub = element("g", { class: "hub", transform: `translate(${CENTER},${CENTER})` });
      hub.append(element("circle", { r: 30 }));
      hub.append(element("text", { y: -3, "text-anchor": "middle" }, "control"));
      hub.append(element("text", { y: 10, "text-anchor": "middle" }, "plane"));

      svg.replaceChildren(
        layers.spokes,
        layers.links,
        layers.pulses,
        layers.nodes,
        layers.services,
        layers.labels,
        hub,
      );
      flashed = null;
    },

    // A visitor's request was answered by a pod on this node: the node
    // is drawn once with a flash.
    asked(name) {
      flashed = name;
    },
  };
}
