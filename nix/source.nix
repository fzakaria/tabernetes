# The Go sources and the pages they embed, and nothing else: a change to
# the docs, the examples or the deployment rebuilds nothing.
{ lib }:
lib.fileset.toSource {
  root = ../.;
  fileset = lib.fileset.unions [
    ../go.mod
    ../go.sum
    ../cmd
    ../internal
    ../web
  ];
}
