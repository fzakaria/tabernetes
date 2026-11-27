# `nix fmt`. The tree wrapper, matching trynix and seenix: one formatting
# step covers every language in the tree. gofmt for the agent, prettier
# for the site's pages, and for markdown at the width the docs are
# written to.
{ pkgs }:
pkgs.nixfmt-tree.override {
  runtimeInputs = [
    pkgs.go
    pkgs.prettier
  ];
  settings = {
    formatter.gofmt = {
      command = "gofmt";
      options = [ "-w" ];
      includes = [ "*.go" ];
    };
    formatter.prettier = {
      command = "prettier";
      options = [
        "--write"
        "--print-width"
        "100"
      ];
      includes = [
        "*.js"
        "*.html"
        "*.css"
      ];
    };
    # proseWrap stays at its default of preserving the author's line
    # breaks: these files are hand-wrapped prose, and reflowing would make
    # every future diff a whole-file diff.
    formatter.prettier-markdown = {
      command = "prettier";
      options = [
        "--write"
        "--print-width"
        "80"
      ];
      includes = [ "*.md" ];
    };
  };
}
