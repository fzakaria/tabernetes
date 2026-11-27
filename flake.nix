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
      packages = self.packages.${system};

      # A program of the flake's as something `nix run` takes.
      app = program: {
        type = "app";
        program = pkgs.lib.getExe program;
      };
    in
    {
      packages.${system} = rec {
        # the agent: the site, and the kubelet of every tab
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
        deploy = app (
          import ./nix/deploy.nix {
            inherit pkgs;
            inherit (packages) bundle;
          }
        );

        # write the tables the site's globe is drawn from, into web/
        globe-data = app (import ./nix/globe-data.nix { inherit pkgs; });

        # draw the site's icon at the sizes browsers and phones ask for
        icons = app (import ./nix/icons.nix { inherit pkgs; });
      };

      checks.${system} = {
        # the Go tests run as the package builds, and the bundle is
        # what would be deployed
        inherit (packages) tabernetes bundle;
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
