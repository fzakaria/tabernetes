#!/usr/bin/env python3
"""Write the two tables the site's globe is drawn from.

    nix run .#globe-data

land.json is where the land is: points spread evenly over the sphere,
keeping the ones on land, which the globe draws as dots. The outline
comes from Natural Earth's 1:110m land polygons, as the world-atlas
package carries them.

zones.json is where each time zone's city is, and the country it is in.
A tab says which time zone its browser is in, and the globe puts the
tab's node at that city, which is as near as the site knows where
anyone is. The table comes from tzdata: zone.tab has a zone's country
and coordinates, and tzdata.zi the older names that mean the same zone,
which browsers still report (Asia/Calcutta for Asia/Kolkata).

Both files are checked in. This only has to run again when the dots'
spacing changes, or a time zone is made.
"""

import argparse
import json
import math
import os

# How far apart the globe's dots are, in degrees along a meridian, and
# the latitudes they are laid between. The poles are left out: Natural
# Earth's outline stops short of the south one, and a dot row there
# would be a single dot.
DOT_SPACING_DEGREES = 2.5
DOT_LATITUDE_LIMIT = 85.0

# Coordinates are written to this many decimal places: a tenth of a
# degree is about 11 km, finer than a dot or a city needs.
DECIMALS = 1

# tzdata.zi's lines that give a zone another name start with this.
LINK = "L"


def rings_of(topology):
    """Every ring of the land's polygons, as lists of (lon, lat).

    A TopoJSON arc is a list of steps from the point before, in whole
    numbers that the transform scales into degrees. A ring names its arcs
    by number, and a negative number is the arc walked backwards."""
    scale, translate = topology["transform"]["scale"], topology["transform"]["translate"]
    arcs = []
    for steps in topology["arcs"]:
        x = y = 0
        points = []
        for dx, dy in steps:
            x, y = x + dx, y + dy
            points.append((x * scale[0] + translate[0], y * scale[1] + translate[1]))
        arcs.append(points)

    rings = []
    for geometry in topology["objects"]["land"]["geometries"]:
        polygons = geometry["arcs"] if geometry["type"] == "MultiPolygon" else [geometry["arcs"]]
        for polygon in polygons:
            for ring in polygon:
                points = []
                for index in ring:
                    arc = arcs[index] if index >= 0 else arcs[~index][::-1]
                    # An arc starts where the one before ended.
                    points.extend(arc if not points else arc[1:])
                rings.append(points)
    return rings


def boxed(rings):
    """Each ring with the box around it, so that a point far from a ring
    is not tested against every edge of it."""
    result = []
    for ring in rings:
        lons = [lon for lon, _ in ring]
        lats = [lat for _, lat in ring]
        result.append((min(lons), max(lons), min(lats), max(lats), ring))
    return result


def on_land(lon, lat, rings):
    """Whether a point is inside an odd number of rings, which is inside
    a polygon and not in one of its holes."""
    inside = False
    for west, east, south, north, ring in rings:
        if not (west <= lon <= east and south <= lat <= north):
            continue
        previous = ring[-1]
        for point in ring:
            (x1, y1), (x2, y2) = previous, point
            if (y1 > lat) != (y2 > lat) and lon < (x2 - x1) * (lat - y1) / (y2 - y1) + x1:
                inside = not inside
            previous = point
    return inside


def land_dots(topology):
    """The land, as a flat list: latitude, longitude, latitude, ...

    Rows of dots are DOT_SPACING_DEGREES apart, and the dots of a row
    are that far apart on the ground, so a row near a pole has fewer."""
    rings = boxed(rings_of(topology))
    dots = []
    rows = int(2 * DOT_LATITUDE_LIMIT / DOT_SPACING_DEGREES) + 1
    for row in range(rows):
        lat = -DOT_LATITUDE_LIMIT + row * DOT_SPACING_DEGREES
        count = max(1, round(360 * math.cos(math.radians(lat)) / DOT_SPACING_DEGREES))
        for column in range(count):
            lon = -180 + (column + 0.5) * 360 / count
            if on_land(lon, lat, rings):
                dots.extend([round(lat, DECIMALS), round(lon, DECIMALS)])
    return dots


def degrees(text):
    """A coordinate of zone.tab, such as +3431 or -1220815, in degrees:
    a sign, then degrees, minutes and sometimes seconds. Latitudes have
    two digits of degrees and longitudes three, so the minutes are found
    from the end."""
    sign = -1 if text[0] == "-" else 1
    digits = text[1:]
    seconds = 0
    if len(digits) in (6, 7):
        seconds = int(digits[-2:])
        digits = digits[:-2]
    minutes = int(digits[-2:])
    whole = int(digits[:-2])
    return sign * (whole + minutes / 60 + seconds / 3600)


def zones(zone_tab, zone_links):
    """Each time zone's latitude, longitude and country code."""
    table = {}
    for line in zone_tab.splitlines():
        if not line or line.startswith("#"):
            continue
        country, coordinates, name = line.split("\t")[:3]

        # The latitude ends where the longitude's sign begins.
        split = max(coordinates.rfind("+"), coordinates.rfind("-"))
        latitude = round(degrees(coordinates[:split]), DECIMALS)
        longitude = round(degrees(coordinates[split:]), DECIMALS)
        table[name] = [latitude, longitude, country]

    # The other names of the same zones.
    for line in zone_links.splitlines():
        parts = line.split()
        if len(parts) == 3 and parts[0] == LINK and parts[1] in table:
            table.setdefault(parts[2], table[parts[1]])
    return dict(sorted(table.items()))


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--land", required=True, help="world-atlas's land-110m.json")
    parser.add_argument("--zone-tab", required=True, help="tzdata's zone.tab")
    parser.add_argument("--zone-links", required=True, help="tzdata's tzdata.zi")
    parser.add_argument("--out", required=True, help="the directory to write land.json and zones.json in")
    args = parser.parse_args()

    with open(args.land) as source:
        dots = land_dots(json.load(source))
    with open(args.zone_tab) as tab, open(args.zone_links) as links:
        table = zones(tab.read(), links.read())

    # One line each, with nothing between the numbers: these are fetched
    # by every visitor.
    for name, value in (("land.json", dots), ("zones.json", table)):
        path = os.path.join(args.out, name)
        with open(path, "w") as out:
            json.dump(value, out, separators=(",", ":"))
            out.write("\n")
        print(f"{path}: {os.path.getsize(path)} bytes")
    print(f"{len(dots) // 2} dots of land, {len(table)} time zones")


if __name__ == "__main__":
    main()
