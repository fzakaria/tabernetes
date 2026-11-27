# `nix run .#deploy -- <ssh host> <public address>`: copy the bundle to
# a machine and run the machine's own setup with it.
#
# MAX_NODES, MAX_NODES_PER_ADDRESS, RESIDENTS, ZONE and DEMO in the
# environment are passed on to deploy/setup.sh, which says what each
# is. A setting that is not named stays as the machine last had it.
{ pkgs, bundle }:
pkgs.writeShellApplication {
  name = "tabernetes-deploy";
  runtimeInputs = [
    pkgs.openssh
    pkgs.rsync
  ];
  text = ''
    if [ "$#" -ne 2 ]; then
      echo "usage: deploy <ssh host> <public address>" >&2
      exit 2
    fi
    host=$1
    address=$2

    # Files are compared by their bytes: everything in the store has the
    # same date, so a rebuilt binary of the same size would otherwise
    # pass for the old one.
    rsync --archive --checksum --delete --chmod=u+w --rsync-path="sudo rsync" \
      ${bundle}/ "$host":/opt/tabernetes/

    # Only the settings this run names are passed on: the machine keeps
    # the rest as they were last set (deploy/setup.sh).
    settings=()
    for name in MAX_NODES MAX_NODES_PER_ADDRESS RESIDENTS ZONE DEMO; do
      if [ -n "''${!name-}" ]; then
        settings+=("$name=''${!name}")
      fi
    done

    # The settings are expanded here, on purpose: they are this
    # command's own.
    # shellcheck disable=SC2029
    ssh "$host" sudo "''${settings[@]}" /opt/tabernetes/deploy/setup.sh "$address"
  '';
}
