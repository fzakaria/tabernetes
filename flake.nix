{
  description = "A Kubernetes cluster whose nodes are browser tabs";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

    # The container runtime: each pod's container is a `tabc run` whose
    # page a tab loads.
    tabc.url = "github:fzakaria/tabc";
    tabc.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      tabc,
    }:
    let
      # The agent drives network namespaces and CNI plugins, so the
      # agent is Linux's alone, and the cluster's machine is x86-64.
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in
    {
      packages.${system} = rec {
        # the agent
        tabernetes = import ./nix/package.nix { inherit pkgs; };
        default = tabernetes;

        # the agent, tabc and the deployment's files, as the directory
        # the cluster's machine gets
        bundle = import ./nix/bundle.nix {
          inherit pkgs tabernetes;
          tabcRelease = tabc.packages.${system}.release;
          tabcShare = tabc.packages.${system}.share;
        };
      };

      apps.${system} = {
        # copy the bundle to a machine and set the machine up:
        # `nix run .#deploy -- tabernetes.exe.xyz https://tabernetes.exe.xyz`
        # MAX_NODES and MAX_NODES_PER_ADDRESS in the environment set
        # the agent's limits (deploy/setup.sh).
        deploy = {
          type = "app";
          program = "${
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

                # The bundle, then the machine's own setup with it. Files
                # are compared by their bytes: everything in the store has
                # the same date, so a rebuilt binary of the same size
                # would otherwise pass for the old one.
                rsync --archive --checksum --delete --chmod=u+w --rsync-path="sudo rsync" \
                  ${self.packages.${system}.bundle}/ "$host":/opt/tabernetes/
                # The address is expanded here, on purpose: it is this
                # command's own argument.
                # shellcheck disable=SC2029
                ssh "$host" sudo \
                  MAX_NODES="''${MAX_NODES:-100}" \
                  MAX_NODES_PER_ADDRESS="''${MAX_NODES_PER_ADDRESS:-4}" \
                  /opt/tabernetes/deploy/setup.sh "$address"
              '';
            }
          }/bin/tabernetes-deploy";
        };
      };

      checks.${system} = {
        # the Go tests run as the package builds
        inherit (self.packages.${system}) tabernetes;
      };

      formatter.${system} = import ./nix/formatter.nix { inherit pkgs; };

      devShells.${system}.default = pkgs.mkShell {
        packages = [
          pkgs.go
          pkgs.gopls
          pkgs.kubectl
        ];
      };
    };
}
