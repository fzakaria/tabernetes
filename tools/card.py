#!/usr/bin/env python3
"""Draw the picture a link to the site is shown with, as an SVG.

The picture is 1200 by 630, which is what link previews ask for: the
site's mark, its name and what it is on the left, and on the right the
globe of the front page with a few nodes on it and a line from each to
the cluster's machine.

usage: card.py --mark web/mark.svg --land web/land.json --out card.svg
"""
import argparse
import json
import math
import re

# The picture's size, and the colours of the site's hero.
WIDTH, HEIGHT = 1200, 630
SKY = "#070b16"
INK = "#edf1ff"
MUTED = "#9aa6c8"
LAND = "150,180,245"
RIM = "#6ea0ff"
BUSY = "#6ee7a8"
HUB = "#ffd27a"

# The globe: where its middle is, how big, and the longitude and
# latitude that face the viewer.
GLOBE_X, GLOBE_Y, GLOBE_RADIUS = 905, 315, 245
FACING_LONGITUDE, FACING_LATITUDE = -45, 24

# The machine, on the west coast of North America, and the cities the
# picture's nodes are in: latitude, longitude.
MACHINE = (37.8, -122.4)
NODES = [(38.7, -9.1), (6.5, 3.4), (51.5, -0.1), (-23.5, -46.6), (40.7, -74.0), (19.4, -99.1), (48.9, 2.4)]

# How high an arc rises off the globe in its middle, as a part of the
# radius, and how many straight pieces an arc is drawn in.
ARC_LIFT = 0.25
ARC_PIECES = 40

# The mark's own size, from its viewBox, and how big the card draws it.
MARK_SIZE = 64
MARK_DRAWN = 92


def on_sphere(latitude, longitude):
    """A point of the unit sphere from a latitude and a longitude."""
    phi, lam = math.radians(latitude), math.radians(longitude)
    return (math.cos(phi) * math.sin(lam), math.sin(phi), math.cos(phi) * math.cos(lam))


def turned(point):
    """A point as the viewer sees it: across, up, and towards the viewer."""
    lam, phi = math.radians(-FACING_LONGITUDE), math.radians(FACING_LATITUDE)
    x = point[0] * math.cos(lam) + point[2] * math.sin(lam)
    z1 = -point[0] * math.sin(lam) + point[2] * math.cos(lam)
    y = point[1] * math.cos(phi) - z1 * math.sin(phi)
    z = point[1] * math.sin(phi) + z1 * math.cos(phi)
    return (x, y, z)


def at(point):
    """Where a seen point is on the picture."""
    return (GLOBE_X + point[0] * GLOBE_RADIUS, GLOBE_Y - point[1] * GLOBE_RADIUS)


def between(start, end, part):
    """The point a part of the way along the great circle between two."""
    dot = max(-1.0, min(1.0, sum(a * b for a, b in zip(start, end))))
    angle = math.acos(dot)
    a = math.sin((1 - part) * angle) / math.sin(angle)
    b = math.sin(part * angle) / math.sin(angle)
    return tuple(a * s + b * e for s, e in zip(start, end))


def arc(start, end):
    """The path of an arc between two points of the sphere, lifted in
    its middle, as an SVG path of the pieces in front of the globe."""
    commands = []
    pen_down = False
    for piece in range(ARC_PIECES + 1):
        part = piece / ARC_PIECES
        lift = 1 + ARC_LIFT * math.sin(math.pi * part)
        seen = turned(tuple(value * lift for value in between(start, end, part)))
        shows = seen[2] > 0 or seen[0] ** 2 + seen[1] ** 2 > 1
        if not shows:
            pen_down = False
            continue
        x, y = at(seen)
        commands.append(f"{'L' if pen_down else 'M'}{x:.1f},{y:.1f}")
        pen_down = True
    return " ".join(commands)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mark", required=True)
    parser.add_argument("--land", required=True)
    parser.add_argument("--out", required=True)
    arguments = parser.parse_args()

    # The mark, without its own <svg> wrapping, to draw inside the
    # card's.
    with open(arguments.mark) as file:
        mark = re.sub(r"</?svg[^>]*>", "", file.read())
    with open(arguments.land) as file:
        land = json.load(file)

    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{WIDTH}" height="{HEIGHT}" viewBox="0 0 {WIDTH} {HEIGHT}">',
        "<defs>",
        '<radialGradient id="glow" cx="78%" cy="45%" r="60%">',
        '<stop offset="0" stop-color="#14224a"/><stop offset="1" stop-color="#070b16" stop-opacity="0"/>',
        "</radialGradient>",
        '<radialGradient id="body" cx="38%" cy="32%" r="75%">',
        '<stop offset="0" stop-color="#16233f"/><stop offset="1" stop-color="#0a1122"/>',
        "</radialGradient>",
        "</defs>",
        f'<rect width="{WIDTH}" height="{HEIGHT}" fill="{SKY}"/>',
        f'<rect width="{WIDTH}" height="{HEIGHT}" fill="url(#glow)"/>',
        # The globe, and the land on the side that faces the viewer.
        f'<circle cx="{GLOBE_X}" cy="{GLOBE_Y}" r="{GLOBE_RADIUS}" fill="url(#body)" stroke="{RIM}" stroke-opacity="0.6" stroke-width="1.5"/>',
    ]
    for index in range(0, len(land), 2):
        seen = turned(on_sphere(land[index], land[index + 1]))
        if seen[2] <= 0:
            continue
        x, y = at(seen)
        parts.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="1.9" fill="rgba({LAND},{0.2 + 0.5 * seen[2]:.2f})"/>')

    # The arcs, the nodes, and the machine.
    machine = on_sphere(*MACHINE)
    for latitude, longitude in NODES:
        node = on_sphere(latitude, longitude)
        parts.append(f'<path d="{arc(node, machine)}" fill="none" stroke="{BUSY}" stroke-opacity="0.5" stroke-width="1.6"/>')
        seen = turned(node)
        if seen[2] > 0:
            x, y = at(seen)
            parts.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="9" fill="{BUSY}" fill-opacity="0.18"/>')
            parts.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="4.5" fill="{BUSY}"/>')
    seen = turned(machine)
    if seen[2] > 0:
        x, y = at(seen)
        parts.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="8" fill="none" stroke="{HUB}" stroke-width="2.5"/>')
        parts.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="3" fill="{HUB}"/>')

    # The mark, the name, and what the site is.
    scale = MARK_DRAWN / MARK_SIZE
    parts += [
        f'<g transform="translate(70,92) scale({scale})">{mark}</g>',
        f'<text x="182" y="160" font-family="Inter" font-weight="700" font-size="68" letter-spacing="-2" fill="{INK}">tabernetes</text>',
        f'<text font-family="Inter" font-weight="700" font-size="52" letter-spacing="-1" fill="{INK}">',
        '<tspan x="70" y="300">A Kubernetes cluster</tspan>',
        '<tspan x="70" y="364">whose nodes are</tspan>',
        '<tspan x="70" y="428">browser tabs</tspan>',
        "</text>",
        f'<text x="70" y="520" font-family="Inter" font-weight="400" font-size="30" fill="{MUTED}">Open the page and your phone is a node.</text>',
        "</svg>",
    ]
    with open(arguments.out, "w") as file:
        file.write("\n".join(parts))


if __name__ == "__main__":
    main()
