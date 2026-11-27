# Everything the cluster's machine needs, as one directory to copy
# over: the agent, tabc, the files of tabc's every container's page
# loads, the deployment's own files, and the examples the deployment
# runs as its demo.
#
# tabc comes from its release build, which is the binary that knows no
# paths of the machine that built it. The share is the same build's, so
# the pages tabc serves and the kernel the agent serves beside them are
# one version.
{
  pkgs,
  tabernetes,
  tabcRelease,
  tabcShare,
}:
pkgs.runCommand "tabernetes-bundle-${tabernetes.version}" { } ''
  mkdir -p $out/bin unpack
  cp ${tabernetes}/bin/tabernetes $out/bin/

  tar -xzf ${tabcRelease}/tabc-linux-amd64.tar.gz -C unpack
  cp unpack/tabc-linux-amd64/tabc $out/bin/
  cp -r unpack/tabc-linux-amd64/licenses $out/licenses
  cp unpack/tabc-linux-amd64/LICENSE $out/licenses/tabc-LICENSE

  # Copied as files: the share's own tree is links into the store, which
  # the machine does not have.
  cp -rL ${tabcShare} $out/share
  cp -r ${../deploy} $out/deploy
  cp -r ${../examples} $out/examples
  chmod -R u+w $out
  chmod +x $out/deploy/*.sh
''
