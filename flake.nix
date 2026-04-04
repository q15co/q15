{
  description = "Optional q15 development shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
  };

  outputs = {nixpkgs, ...}: let
    systems = [
      "x86_64-linux"
      "aarch64-linux"
      "x86_64-darwin"
      "aarch64-darwin"
    ];

    forAllSystems = nixpkgs.lib.genAttrs systems;
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
          go_1_25
          nodejs_24
          python312
        ];

        env = {
          GO111MODULE = "on";
          GOPROXY = "https://proxy.golang.org,direct";
          GOSUMDB = "sum.golang.org";
          CGO_ENABLED = "0";
        };
      };
    });
  };
}
