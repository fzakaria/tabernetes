# `nix run .#icons`: draw the site's icon at the sizes a browser or a
# phone wants as a picture, from web/mark.svg, and put the mark where
# the README shows it. The pictures are checked in; this is how they
# were made, and what to run after the mark changes.
{ pkgs }:
pkgs.writeShellApplication {
  name = "tabernetes-icons";
  runtimeInputs = [ pkgs.librsvg ];
  text = ''
    if [ ! -f web/mark.svg ]; then
      echo "run this from the repository's root" >&2
      exit 2
    fi

    # A browser's tab, for one that does not take an SVG, and the size
    # a link preview and an installed page ask for.
    rsvg-convert --width 32 --height 32 web/mark.svg --output web/icon-32.png
    rsvg-convert --width 512 --height 512 web/mark.svg --output web/icon-512.png

    # An iPhone's home screen cuts the corners itself, and fills what a
    # picture leaves clear with black. So the picture it gets has the
    # sky right to the corners.
    square=$(mktemp --suffix=.svg)
    sed 's/rx="14"/rx="0"/' web/mark.svg > "$square"
    rsvg-convert --width 180 --height 180 "$square" --output web/icon-180.png
    rm "$square"

    cp web/mark.svg docs/img/mark.svg
    ls -l web/icon-*.png docs/img/mark.svg
  '';
}
