// The page a browser opens to become a node.
//
// The page tells the agent what the browser is, over a WebSocket that
// stays open for as long as the tab is a node. The agent answers with
// the node's name, and from then on with the containers to show: each
// is a frame on a page tabc serves, which boots Linux in the frame and
// runs the pod's program there. Closing the tab closes the socket, and
// the agent takes the node out of the cluster.
//
// A node also sees what it has done, can send a request to its own pod,
// and, in a pod that allows one, can open a shell.

import { duration } from "./format.js";

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

// A page opened with this after the # in its address keeps asking for
// a place when it is turned away, however long that takes. A browser
// with nobody in front of it has nobody to press the button.
const KEEP_ASKING = "keep";
const keepsAsking = location.hash.slice(1) === KEEP_ASKING;

// Where the browser keeps the end of the name this tab had as a node,
// and how long that end is. The tab asks for the same name when it
// joins again, so that a node which lost its place to a restart of the
// agent comes back as itself.
const SUFFIX_KEY = "tabernetes-suffix";
const SUFFIX_LENGTH = 4;

// How long after the tab comes back into view a tab that lost its place
// asks for one again.
const REJOIN_DELAY_MS = 500;

// A tab that lost its place without asking to, because the agent
// restarted or the network dropped, asks again by itself: first after
// RETRY_FIRST_MS, then after twice as long each time, up to
// RETRY_MOST_MS.
const RETRY_FIRST_MS = 2000;
const RETRY_MOST_MS = 30000;

// What this page asks of its own pod, each as the name an exposed
// Service goes by and a path there (deploy/setup.sh,
// examples/counter.yaml): an answer to a visit, and how many visits
// the pod has answered and tiles it has rendered. The tally has a name
// of its own so that the agent does not count it among the requests
// visitors send.
const ASK = Object.freeze({ app: "counter", path: "json" });
const TALLY = Object.freeze({ app: "tally", path: "tally" });

// How often the page asks its pod for the tally: often until the pod
// first answers, which is when its program has started, and seldom
// after. And how often the node's age on the page is written again.
const TALLY_FIRST_MS = 3000;
const TALLY_EVERY_MS = 15000;
const AGE_EVERY_MS = 1000;
const MS_PER_SECOND = 1000;

// Where the agent gives a tab a shell in one of its containers, and
// the files of the terminal the shell is shown on, which a container's
// page has too and are fetched from beside it.
const SHELL_PATH = "shells/";
const TERMINAL_STYLE = "xterm/xterm.css";
const TERMINAL_SCRIPTS = ["xterm/xterm.js", "xterm/addon-fit.js"];
const TERMINAL_OPTIONS = Object.freeze({
  cursorBlink: true,
  fontSize: 13,
  fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace',
});

// The annotation a pod allows a shell with (internal/kube/shell.go),
// which the page names on a pod that has none.
const SHELL_ANNOTATION = "tabernetes.dev/shell";

// What the shell's button says.
const SHELL_OPEN = "Open a shell";
const SHELL_CLOSE = "Close the shell";

const nameElement = document.getElementById("name");
const statusElement = document.getElementById("status");
const podsElement = document.getElementById("pods");
const clusterElement = document.getElementById("cluster");
const leaveButton = document.getElementById("leave");
const rejoinButton = document.getElementById("rejoin");
const explainElement = document.getElementById("explain");
const watchElement = document.getElementById("watch");
const mineElement = document.getElementById("mine");
const ageElement = document.getElementById("tally-age");
const answeredElement = document.getElementById("tally-answered");
const tilesElement = document.getElementById("tally-tiles");
const askButton = document.getElementById("ask-mine");
const askAnswer = document.getElementById("ask-mine-answer");

// The socket while the tab is a node, the containers shown by id, the
// state the status line shows, and the lock that keeps the screen on.
let socket = null;
const frames = new Map();
let state = State.JOINING;
let wakeLock = null;

// The node's name and when the tab became the node, while it is one,
// and the timer of the next question about the tally.
let nodeName = "";
let joinedAt = 0;
let tallyTimer = null;

// The terminal's files, once they have been asked for.
let terminalLoaded = null;

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
    suffix: remembered(),
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

// The end of the name this tab had before, or nothing. A browser may
// keep a page from its storage, and the tab then gets a new name.
function remembered() {
  try {
    return localStorage.getItem(SUFFIX_KEY) ?? "";
  } catch {
    return "";
  }
}

// Keep the end of the name the agent gave this tab.
function remember(name) {
  try {
    localStorage.setItem(SUFFIX_KEY, name.slice(-SUFFIX_LENGTH));
  } catch {
    // Without storage the tab is a new node each time.
  }
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

// An element with a class and, when given, text.
function make(tag, className, text) {
  const element = document.createElement(tag);
  element.className = className;
  if (text !== undefined) {
    element.textContent = text;
  }
  return element;
}

// One part of a pod's card: a line that names the part and the kubectl
// command a real node would show the same thing for.
function part(name, what, command) {
  const element = make("div", "part");
  const label = make("div", "label");
  label.append(make("b", "", name), make("span", "", what), make("code", "mono", command));
  element.append(label);
  return { element, label };
}

// Fetch the terminal's files, once: a style sheet and two scripts that
// put Terminal and FitAddon on the page. A container's page has them,
// so they are asked for from beside one.
function loadTerminal(base) {
  if (terminalLoaded !== null) {
    return terminalLoaded;
  }
  const style = document.createElement("link");
  style.rel = "stylesheet";
  style.href = base + TERMINAL_STYLE;
  document.head.append(style);

  // One script after the other: the second adds to what the first made.
  terminalLoaded = TERMINAL_SCRIPTS.reduce(
    (before, file) =>
      before.then(
        () =>
          new Promise((resolve, reject) => {
            const script = document.createElement("script");
            script.src = base + file;
            script.addEventListener("load", resolve);
            script.addEventListener("error", reject);
            document.head.append(script);
          }),
      ),
    Promise.resolve(),
  );
  terminalLoaded.catch(() => {
    terminalLoaded = null;
  });
  return terminalLoaded;
}

// A shell's part of a pod's card, for a pod that allows one: a button
// that opens a terminal on a second program in the pod's container.
// The agent starts the program with `tabc exec` and carries what is
// typed and printed over a socket, which is what a kubelet does for
// `kubectl exec`.
function shellPart(message, podName) {
  const { element, label } = part(
    "Shell",
    "a second program in the pod:",
    `kubectl exec -it ${podName} -- sh`,
  );
  const button = make("button", "button quiet", SHELL_OPEN);
  const screen = make("div", "terminal");
  const note = make("p", "note");
  screen.hidden = true;
  note.hidden = true;
  label.append(button);
  element.append(screen, note);

  const shell = {
    id: message.id,
    path: message.path,
    button,
    screen,
    note,
    socket: null,
    terminal: null,
    watcher: null,
  };
  button.addEventListener("click", () => {
    if (shell.socket !== null) {
      shell.socket.close();
      return;
    }
    openShell(shell);
  });
  return { element, shell };
}

// Say something under a shell's terminal, or nothing.
function shellNote(shell, text) {
  shell.note.textContent = text;
  shell.note.hidden = text === "";
}

// Open a pod's shell: a terminal on the page, and a socket to the agent
// that carries the terminal's keys one way and the program's output
// the other.
async function openShell(shell) {
  shell.button.disabled = true;
  shellNote(shell, "");
  try {
    await loadTerminal(shell.path);
  } catch {
    shell.button.disabled = false;
    shellNote(shell, "The terminal could not be loaded.");
    return;
  }

  const terminal = new window.Terminal(TERMINAL_OPTIONS);
  const fitter = new window.FitAddon.FitAddon();
  terminal.loadAddon(fitter);
  shell.screen.hidden = false;
  terminal.open(shell.screen);
  fitter.fit();
  shell.terminal = terminal;

  const address = new URL(SHELL_PATH + encodeURIComponent(shell.id), location.href);
  address.protocol = address.protocol.replace("http", "ws");
  const socket = new WebSocket(address);
  socket.binaryType = "arraybuffer";
  shell.socket = socket;

  // The agent is told the terminal's size first, and again whenever
  // the size changes. Keys go as bytes, sizes as text.
  const encoder = new TextEncoder();
  socket.addEventListener("open", () => {
    socket.send(JSON.stringify({ rows: terminal.rows, cols: terminal.cols }));
    shell.button.textContent = SHELL_CLOSE;
    shell.button.disabled = false;
    terminal.focus();
  });
  terminal.onData((keys) => {
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(encoder.encode(keys));
    }
  });
  terminal.onResize((size) => {
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ rows: size.rows, cols: size.cols }));
    }
  });
  socket.addEventListener("message", (event) => terminal.write(new Uint8Array(event.data)));

  // The agent says why as it closes the socket: the program ended, the
  // shell sat idle, or there was no shell to give.
  socket.addEventListener("close", (event) => {
    closeShell(shell, event.reason || "The shell closed.");
  });

  shell.watcher = new ResizeObserver(() => fitter.fit());
  shell.watcher.observe(shell.screen);
}

// Put a shell's part back as it was before the shell was opened, with
// a word on why the shell closed.
function closeShell(shell, reason) {
  shell.watcher?.disconnect();
  shell.terminal?.dispose();
  shell.watcher = null;
  shell.terminal = null;
  shell.socket = null;
  shell.screen.hidden = true;
  shell.button.textContent = SHELL_OPEN;
  shell.button.disabled = false;
  shellNote(shell, reason);
}

// Show a container: a frame on the container's page, which is the
// program's terminal, and under the frame a shell where the pod allows
// one.
function run(message) {
  stop(message);

  const pod = make("div", "pod");
  const title = make("div", "title");
  title.append(make("span", "mono", message.pod), make("span", "image mono", message.image));

  // The pod's name without its namespace, as kubectl takes one.
  const podName = message.pod.split("/").pop();
  const logs = part("Logs", "what the pod's program prints:", `kubectl logs ${podName}`);
  const frame = document.createElement("iframe");
  frame.src = message.path;
  frame.title = message.pod;
  logs.element.append(frame);
  pod.append(title, logs.element);

  // The shell, or a line that says why there is none.
  let shell = null;
  if (message.shell) {
    const made = shellPart(message, podName);
    shell = made.shell;
    pod.append(made.element);
  } else {
    pod.append(
      part(
        "Shell",
        "not allowed by this pod. A pod allows one with the annotation",
        SHELL_ANNOTATION,
      ).element,
    );
  }

  podsElement.append(pod);
  frames.set(message.id, { pod, shell });
  show(State.RUNNING, `Running ${message.pod}. The Linux below is booted in this tab.`);
  askTally(TALLY_FIRST_MS);
}

// Remove a container's frame.
function stop(message) {
  const shown = frames.get(message.id);
  if (!shown) {
    return;
  }
  shown.shell?.socket?.close();
  shown.pod.remove();
  frames.delete(message.id);
  if (frames.size === 0 && state === State.RUNNING) {
    show(State.IDLE, "Ready. Waiting for the scheduler to send a pod.");
  }
}

// Remove every frame: the tab is no longer a node.
function clear() {
  for (const shown of frames.values()) {
    shown.shell?.socket?.close();
    shown.pod.remove();
  }
  frames.clear();
  forgetTally();
}

// The address of something asked of this node's own pod, through the
// exposed Service the pod is behind.
function ofMyPod(what) {
  return `nodes/${encodeURIComponent(nodeName)}/apps/${what.app}/${what.path}`;
}

// Ask this node's pod how many requests the pod has answered and how
// many tiles it has rendered, and ask again later. The first answer is what shows the tally and the button:
// a cluster without the demo has no such pod, and the page then shows
// neither.
function askTally(after) {
  clearTimeout(tallyTimer);
  tallyTimer = setTimeout(async () => {
    let next = mineElement.hidden ? TALLY_FIRST_MS : TALLY_EVERY_MS;
    if (document.visibilityState === "visible" && frames.size > 0) {
      try {
        const response = await fetch(ofMyPod(TALLY), { cache: "no-store" });
        if (response.ok) {
          const tally = await response.json();
          answeredElement.textContent = String(tally.answered ?? 0);
          tilesElement.textContent = String(tally.tiles ?? 0);
          mineElement.hidden = false;
          next = TALLY_EVERY_MS;
        }
      } catch {
        // The pod is not up yet, or the network dropped: the next
        // question finds out.
      }
    }
    if (socket !== null) {
      askTally(next);
    }
  }, after);
}

// Stop asking for the tally and take it off the page: the tab is no
// longer a node.
function forgetTally() {
  clearTimeout(tallyTimer);
  mineElement.hidden = true;
  askAnswer.textContent = "";
}

// Send a request to this node's own pod, and say what the pod answered.
// The pod's terminal below prints a line for the same request.
async function askMine() {
  askButton.disabled = true;
  askAnswer.textContent = "asking";
  try {
    const response = await fetch(ofMyPod(ASK), { cache: "no-store" });
    if (!response.ok) {
      throw new Error((await response.text()).trim() || String(response.status));
    }
    const answer = await response.json();
    askAnswer.textContent = `visit ${answer.visit}, answered on this device`;
  } catch (error) {
    askAnswer.textContent = `no answer: ${error.message}`;
  }
  askButton.disabled = false;
  askTally(0);
}

// Write how long the tab has been a node.
function showAge() {
  if (joinedAt === 0) {
    return;
  }
  ageElement.textContent = duration((Date.now() - joinedAt) / MS_PER_SECOND);
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
        nodeName = message.node;
        joinedAt = Date.now();
        showAge();
        remember(message.node);
        watchElement.href = `/#node=${encodeURIComponent(message.node)}`;
        document.title = `${message.node} · tabernetes`;
        show(State.IDLE, "Ready. Waiting for the scheduler to send a pod.");
        stayAwake();
        break;
      case FromAgent.REFUSED:
        nameElement.textContent = "not a node";
        if (message.retry || keepsAsking) {
          // Turned away for now: the machine is busy, or this page was
          // opened to keep asking. The socket's closing asks again.
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
    joinedAt = 0;
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
askButton.addEventListener("click", askMine);
setInterval(showAge, AGE_EVERY_MS);
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
