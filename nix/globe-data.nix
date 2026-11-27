# `nix run .#globe-data`: write web/land.json and web/zones.json, the
# two tables the site's globe is drawn from (tools/globe-data.py says
# what is in each). Both are checked in; this is how they were made.
#
# The land's outline is Natural Earth's, as the world-atlas package
# carries it, and the time zones are tzdata's.
{ pkgs }:
let
  worldAtlas = pkgs.fetchurl {
    url = "https://registry.npmjs.org/world-atlas/-/world-atlas-2.0.2.tgz";
    sha256 = "15svq19b91f59jadlmy2hwly8xrvphigy8zcmypfs06fy9jpfbh3";
  };

  # The one file of the package the tool reads.
  land = pkgs.runCommand "land-110m.json" { } ''
    tar -xzf ${worldAtlas} package/land-110m.json
    cp package/land-110m.json $out
  '';
in
pkgs.writeShellApplication {
  name = "tabernetes-globe-data";
  runtimeInputs = [ pkgs.python3 ];
  text = ''
    if [ ! -d web ]; then
      echo "run this from the repository's root" >&2
      exit 2
    fi
    python3 ${../tools/globe-data.py} \
      --land ${land} \
      --zone-tab ${pkgs.tzdata}/share/zoneinfo/zone.tab \
      --zone-links ${pkgs.tzdata}/share/zoneinfo/tzdata.zi \
      --out web
  '';
}
