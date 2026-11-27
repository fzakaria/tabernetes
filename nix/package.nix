# The agent (cmd/tabernetes) as one static file: the site's pages are
# embedded, and the binary links against nothing, so the file runs on
# whatever Linux the cluster's machine has.
{ pkgs }:
let
  go = import ./go.nix;
  lib = pkgs.lib;
in
pkgs.buildGoModule {
  pname = "tabernetes";
  inherit (go) version vendorHash;

  src = import ./source.nix { inherit lib; };
  subPackages = [ "cmd/tabernetes" ];

  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${go.version}"
  ];

  # Every package's tests, not only the command's.
  checkPhase = ''
    runHook preCheck
    go test ./...
    runHook postCheck
  '';

  meta = {
    description = "The kubelet of a Kubernetes cluster whose nodes are browser tabs";
    homepage = "https://github.com/fzakaria/tabernetes";
    license = lib.licenses.mit;
    mainProgram = "tabernetes";
    platforms = lib.platforms.linux;
  };
}
