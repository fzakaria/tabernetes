// How the site writes times, sizes and places.

// The lengths of time a duration is given in, in seconds, largest
// first.
const UNITS = [
  ["d", 86400],
  ["h", 3600],
  ["m", 60],
  ["s", 1],
];

// The sizes a count of bytes is given in, largest first.
const SIZES = [
  ["GB", 1e9],
  ["MB", 1e6],
  ["kB", 1e3],
];

// What stands where there is nothing to say, and what the agent writes
// where a browser said nothing.
export const NONE = "-";
const UNKNOWN = "unknown";

// The first letter of a regional indicator, which is what a flag is
// spelled in: two of them, one for each letter of a country's code.
const FLAG_BASE = 0x1f1e6;
const LETTER_A = 65;

// A duration as kubectl writes an age: the largest unit that fits, and
// the next one after it when the first is small, so that "1h" and
// "1h50m" do not read the same.
export function duration(seconds) {
  seconds = Math.max(0, Math.floor(seconds));
  for (const [index, [unit, length]] of UNITS.entries()) {
    if (seconds < length) {
      continue;
    }
    const whole = Math.floor(seconds / length);
    const next = UNITS[index + 1];
    if (whole >= 10 || !next) {
      return `${whole}${unit}`;
    }
    const rest = Math.floor((seconds - whole * length) / next[1]);
    return rest > 0 ? `${whole}${unit}${rest}${next[0]}` : `${whole}${unit}`;
  }
  return "0s";
}

// A count of bytes.
export function bytes(count) {
  for (const [unit, size] of SIZES) {
    if (count >= size) {
      const amount = count / size;
      return `${amount >= 100 ? amount.toFixed(0) : amount.toFixed(1)} ${unit}`;
    }
  }
  return `${count} B`;
}

// A number of seconds with one decimal, for a boot.
export function seconds(value) {
  return `${value.toFixed(1)} s`;
}

// A whole number with separators.
export function count(value) {
  return Math.round(value).toLocaleString("en-US");
}

// A label's value as text, or nothing when the browser gave none.
export function known(value) {
  return value && value !== UNKNOWN ? value.replaceAll("-", " ") : "";
}

// What a node's device is called: its model, or its platform.
export function device(node) {
  return known(node.model) || known(node.platform) || "a browser";
}

// The city a time zone is named after: the last part of the name.
export function city(zone) {
  if (!zone || !zone.includes("/")) {
    return "";
  }
  return zone.split("/").pop().replaceAll("_", " ");
}

// A country's flag, from its two-letter code.
export function flag(country) {
  if (!/^[A-Z]{2}$/.test(country ?? "")) {
    return "";
  }
  return String.fromCodePoint(
    ...[...country].map((letter) => FLAG_BASE + letter.charCodeAt(0) - LETTER_A),
  );
}

// The middle value of a list of numbers, or null for an empty list.
export function median(values) {
  if (values.length === 0) {
    return null;
  }
  const sorted = [...values].sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 1 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2;
}

// A number from a name, the same every time for the same name, between
// 0 and 1. The globe uses it to set nodes of one city a little apart.
export function scatter(name, salt) {
  let hash = 2166136261 ^ salt;
  for (const char of name) {
    hash = Math.imul(hash ^ char.charCodeAt(0), 16777619);
  }
  return ((hash >>> 0) % 10000) / 10000;
}
