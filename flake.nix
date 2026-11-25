{
  description = "Optional q15 development shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
    # Go 1.27 never reached nixos-25.11, so the toolchain comes from unstable
    # while everything else stays on the release channel.
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = {
    nixpkgs,
    nixpkgs-unstable,
    ...
  }: let
    toolVersions = builtins.readFile ./scripts/tool-versions.sh;
    nodeMajor = builtins.head (builtins.match ".*NODE_VERSION=([0-9]+)\\.[0-9]+\\.[0-9]+.*" toolVersions);
    systems = [
      "x86_64-linux"
      "aarch64-linux"
      "x86_64-darwin"
      "aarch64-darwin"
    ];

    forAllSystems = nixpkgs.lib.genAttrs systems;
    go = system: nixpkgs-unstable.legacyPackages.${system}.go_1_27;
  in {
    devShells = forAllSystems (system: let
      pkgs = import nixpkgs {inherit system;};
    in {
      default = pkgs.mkShell {
        packages = with pkgs; [
          bashInteractive
          curl
          git
          gnumake
          (go system)
          pkgs."nodejs_${nodeMajor}"
          python312
        ];

        env = {
          GO111MODULE = "on";
          GOPROXY = "https://proxy.golang.org,direct";
          GOSUMDB = "sum.golang.org";
          CGO_ENABLED = "0";
        };
        # project-setup installs exact Node/pnpm versions from the shared manifest.
        shellHook = ''
          export PATH="$PWD/.tools/bin:$PATH"
        '';
      };
    });
  };
}
