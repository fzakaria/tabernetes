// The page a browser opens to become a node.
//
// The page tells the agent what the browser is, over a WebSocket that
// stays open for as long as the tab is a node. The agent answers with
// the node's name, and from then on with the containers to show: each
// is a frame on a page tabc serves, which boots Linux in the frame and
// runs the pod's program there. Closing the tab closes the socket, and
// the agent takes the node out of the cluster.

// Where the agent listens for tabs, beside this page.
const SOCKET_PATH = "ws";

// The messages, by type (internal/agent/agent.go).
const FromAgent = Object.freeze({
  JOINED: "joined",
  REFUSED: "refused",
  RUN: "run",
  STOP: "stop",
  CLUSTER: "cluster",
});
const ToAgent = Object.freeze({ BYE: "bye" });

// What the status line is styled by.
const State = Object.freeze({
  JOINING: "joining",
  IDLE: "idle",
  RUNNING: "running",
  LEFT: "left",
  REFUSED: "refused",
  UNSUPPORTED: "unsupported",
});

// The word a resident's page is given, after this name in the part of
// its address a browser keeps to itself. A resident is a browser the
// cluster's own machine keeps open; a visitor's address has no word.
const RESIDENT_KEY = "resident";
const residentWord = new URLSearchParams(location.hash.slice(1)).get(RESIDENT_KEY) ?? "";

// How long after the tab comes back into view a tab that lost its place
// asks for one again.
const REJOIN_DELAY_MS = 500;

// A tab that lost its place without asking to, because the agent
// restarted or the network dropped, asks again by itself: first after
// RETRY_FIRST_MS, then after twice as long each time, up to
// RETRY_MOST_MS.
const RETRY_FIRST_MS = 2000;
const RETRY_MOST_MS = 30000;

const nameElement = document.getElementById("name");
const statusElement = document.getElementById("status");
const podsElement = document.getElementById("pods");
const clusterElement = document.getElementById("cluster");
const leaveButton = document.getElementById("leave");
const rejoinButton = document.getElementById("rejoin");
const explainElement = document.getElementById("explain");
const watchElement = document.getElementById("watch");

// The socket while the tab is a node, the containers shown by id, the
// state the status line shows, and the lock that keeps the screen on.
let socket = null;
const frames = new Map();
let state = State.JOINING;
let wakeLock = null;

// Whether the person asked to leave, the wait before the next try at
// joining again, and the timer of that try.
let leaving = false;
let retryDelay = RETRY_FIRST_MS;
let retryTimer = null;

// Show the status line.
function show(next, text) {
  state = next;
  statusElement.dataset.state = next;
  statusElement.textContent = text;
  explainElement.hidden = next !== State.RUNNING;
  leaveButton.hidden = next !== State.IDLE && next !== State.RUNNING;
  rejoinButton.hidden = next !== State.LEFT && next !== State.REFUSED;
}

// The first whole number in a version, which is as much as a node's
// description needs.
function major(version) {
  const match = /\d+/.exec(version ?? "");
  return match ? match[0] : "";
}

// What this browser is, from the browser's own account where it gives
// one and from its user agent where it does not.
async function describe() {
  const hello = {
    model: "",
    platform: "",
    platformVersion: "",
    browser: "",
    browserVersion: "",
    threads: navigator.hardwareConcurrency ?? 0,
    touch: matchMedia("(pointer: coarse)").matches,
    // The time zone, which is how the site's map places the node: at
    // the zone's city, and no nearer.
    zone: Intl.DateTimeFormat().resolvedOptions().timeZone ?? "",
    resident: residentWord,
  };
  const agent = navigator.userAgent;

  // Chromium's own account, which has the device's model on a phone.
  const data = navigator.userAgentData;
  if (data) {
    let detail = {};
    try {
      detail = await data.getHighEntropyValues(["model", "platformVersion"]);
    } catch {
      // A browser may refuse the detail and still give the rest.
    }
    hello.model = detail.model ?? "";
    hello.platform = data.platform ?? "";
    hello.platformVersion = major(detail.platformVersion);

    // The brand that is a product, not Chromium's deliberate nonsense
    // entry or the engine's own name.
    const brand =
      data.brands.find((b) => !/Not.?A.?Brand|Chromium/i.test(b.brand)) ??
      data.brands.find((b) => /Chromium/i.test(b.brand));
    if (brand) {
      hello.browser = brand.brand.replace(/^Google |^Microsoft /, "");
      hello.browserVersion = major(brand.version);
    }
    return hello;
  }

  // Everyone else, from the user agent.
  const firefox = /Firefox\/(\d+)/.exec(agent);
  const safari = /Version\/(\d+)[\d.]* .*Safari/.exec(agent);
  if (firefox) {
    hello.browser = "Firefox";
    hello.browserVersion = firefox[1];
  } else if (safari) {
    hello.browser = "Safari";
    hello.browserVersion = safari[1];
  }

  const iphone = /(iPhone|iPad).* OS (\d+)_/.exec(agent);
  const android = /Android (\d+)/.exec(agent);
  if (iphone) {
    hello.model = iphone[1];
    hello.platform = "iOS";
    hello.platformVersion = iphone[2];
  } else if (android) {
    hello.platform = "Android";
    hello.platformVersion = android[1];
  } else if (/Macintosh/.test(agent)) {
    hello.platform = "macOS";
  } else if (/Windows/.test(agent)) {
    hello.platform = "Windows";
  } else if (/Linux/.test(agent)) {
    hello.platform = "Linux";
  }
  return hello;
}

// Whether this browser can run the guest: the kernel needs WebAssembly
// that can wait on a promise.
function supported() {
  return typeof WebAssembly === "object" && typeof WebAssembly.Suspending === "function";
}

// Keep the screen on while the tab is a node: a phone that locks stops
// running the page, and the agent then takes the node away.
async function stayAwake() {
  if (!("wakeLock" in navigator) || wakeLock !== null || document.visibilityState !== "visible") {
    return;
  }
  try {
    wakeLock = await navigator.wakeLock.request("screen");
    wakeLock.addEventListener("release", () => {
      wakeLock = null;
    });
  } catch {
    // The browser may refuse, on low battery for one. The tab is a node
    // for as long as the screen stays on by other means.
  }
}

// Show a container: a frame on the container's page.
function run(message) {
  stop(message);

  const pod = document.createElement("div");
  pod.className = "pod";

  const title = document.createElement("div");
  title.className = "title";
  const name = document.createElement("span");
  name.className = "mono";
  name.textContent = message.pod;
  const image = document.createElement("span");
  image.className = "image mono";
  image.textContent = message.image;
  title.append(name, image);

  const frame = document.createElement("iframe");
  frame.src = message.path;
  frame.title = message.pod;
  pod.append(title, frame);

  podsElement.append(pod);
  frames.set(message.id, pod);
  show(State.RUNNING, `Running ${message.pod}. The Linux below is booted in this tab.`);
}

// Remove a container's frame.
function stop(message) {
  const pod = frames.get(message.id);
  if (!pod) {
    return;
  }
  pod.remove();
  frames.delete(message.id);
  if (frames.size === 0 && state === State.RUNNING) {
    show(State.IDLE, "Ready. Waiting for the scheduler to send a pod.");
  }
}

// Remove every frame: the tab is no longer a node.
function clear() {
  for (const pod of frames.values()) {
    pod.remove();
  }
  frames.clear();
}

// Become a node.
async function join() {
  clearTimeout(retryTimer);
  leaving = false;
  if (!supported()) {
    nameElement.textContent = "not a node";
    show(
      State.UNSUPPORTED,
      "This browser cannot run the guest. Chrome 137, Firefox 153 or Safari 27, or newer, can.",
    );
    return;
  }
  nameElement.textContent = "joining";
  show(State.JOINING, "Asking the cluster for a place.");

  const hello = await describe();
  const address = new URL(SOCKET_PATH, location.href);
  address.protocol = address.protocol.replace("http", "ws");
  const opened = new WebSocket(address);
  socket = opened;

  opened.addEventListener("open", () => opened.send(JSON.stringify(hello)));
  opened.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    switch (message.type) {
      case FromAgent.JOINED:
        retryDelay = RETRY_FIRST_MS;
        nameElement.textContent = message.node;
        watchElement.href = `/#node=${encodeURIComponent(message.node)}`;
        document.title = `${message.node} · tabernetes`;
        show(State.IDLE, "Ready. Waiting for the scheduler to send a pod.");
        stayAwake();
        break;
      case FromAgent.REFUSED:
        nameElement.textContent = "not a node";
        if (message.retry || residentWord) {
          // Turned away for now: the machine is busy, or this is a
          // resident, which waits for room however long that takes.
          // The socket's closing asks again.
          show(State.LEFT, message.reason);
          break;
        }
        show(State.REFUSED, message.reason);
        break;
      case FromAgent.RUN:
        run(message);
        break;
      case FromAgent.STOP:
        stop(message);
        break;
      case FromAgent.CLUSTER:
        clusterElement.textContent = `${message.nodes} nodes, ${message.pods} pods`;
        break;
    }
  });
  opened.addEventListener("close", () => {
    if (socket !== opened) {
      return;
    }
    socket = null;
    clear();
    if (state === State.REFUSED) {
      return;
    }
    nameElement.textContent = "not a node";
    if (leaving) {
      show(State.LEFT, "This tab left the cluster, and its pods went to other nodes.");
      return;
    }

    // The place was lost, not given up, or the tab was asked to wait
    // for one: ask again, later each time the asking fails. What a tab
    // that was asked to wait was told stays on the page.
    if (state !== State.LEFT) {
      show(State.LEFT, "This tab lost its place in the cluster, and is asking for one again.");
    }
    retryTimer = setTimeout(join, retryDelay);
    retryDelay = Math.min(retryDelay * 2, RETRY_MOST_MS);
  });
}

// Stop being a node, because the person said so or the tab is closing.
function leave() {
  leaving = true;
  clearTimeout(retryTimer);
  if (socket === null) {
    return;
  }
  try {
    socket.send(JSON.stringify({ type: ToAgent.BYE }));
  } catch {
    // The socket may be closing already, which is all a goodbye asks.
  }
  socket.close();
}

leaveButton.addEventListener("click", leave);
rejoinButton.addEventListener("click", join);

// A tab that is being closed says so, which is faster than the agent
// noticing the silence.
addEventListener("pagehide", leave);

// A phone that was locked lost its place. Back in view, the tab asks
// for a place again, and takes the screen lock again.
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "visible") {
    return;
  }
  if (state === State.LEFT && !leaving) {
    clearTimeout(retryTimer);
    retryTimer = setTimeout(join, REJOIN_DELAY_MS);
    return;
  }
  stayAwake();
});

join();
