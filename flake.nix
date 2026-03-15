{
  description = "q15 Telegram shell agent";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = {
    self,
    nixpkgs,
  }: let
    systems = ["x86_64-linux"];
    forAllSystems = f:
      nixpkgs.lib.genAttrs systems (
        system:
          f {
            inherit system;
            pkgs = import nixpkgs {inherit system;};
          }
      );
  in {
    packages = forAllSystems (
      {pkgs, ...}: let
        version =
          if self ? rev && self.rev != null
          then self.shortRev
          else "dirty";

        q15Agent = pkgs.buildGoModule {
          pname = "q15-agent";
          inherit version;
          src = ./.;
          modRoot = "systems/agent";
          subPackages = ["."];
          vendorHash = "sha256-m0dFIsRsenClg2dkk07G28fyrhpeLQcNmxzEj0NiLP8=";
          env = {
            GOWORK = "off";
          };
          postInstall = ''
            mv "$out/bin/agent" "$out/bin/q15"
          '';
        };

        q15ExecService = pkgs.buildGoModule {
          pname = "q15-exec-service";
          inherit version;
          src = ./.;
          modRoot = "systems/exec-service";
          subPackages = ["."];
          vendorHash = "sha256-haplAug8zOkavCfCUq6aJ/fKGfx5ZIoNXOWRoIjk87Q=";
          env = {
            GOWORK = "off";
          };
          postInstall = ''
            mv "$out/bin/exec-service" "$out/bin/q15-exec-service"
          '';
        };

        q15ProxyService = pkgs.buildGoModule {
          pname = "q15-proxy-service";
          inherit version;
          src = ./.;
          modRoot = "systems/proxy-service";
          subPackages = ["."];
          vendorHash = "sha256-xsDafC/FEZA6fM7MHM/RUQl9LGRrUdS/bmQTRpd2Qn0=";
          env = {
            GOWORK = "off";
          };
          postInstall = ''
            mv "$out/bin/proxy-service" "$out/bin/q15-proxy-service"
          '';
        };
        q15Package = pkgs.symlinkJoin {
          name = "q15-${version}";
          paths = [
            q15Agent
            q15ExecService
            q15ProxyService
          ];
        };
      in {
        q15-agent = q15Agent;
        q15-exec-service = q15ExecService;
        q15-proxy-service = q15ProxyService;
        q15 = q15Package;
        default = q15Package;
      }
    );

    apps = forAllSystems (
      {system, ...}: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/q15";
        };
        q15 = {
          type = "app";
          program = "${self.packages.${system}.q15}/bin/q15";
        };
        q15-exec-service = {
          type = "app";
          program = "${self.packages.${system}.q15}/bin/q15-exec-service";
        };
        q15-proxy-service = {
          type = "app";
          program = "${self.packages.${system}.q15}/bin/q15-proxy-service";
        };
      }
    );
  };
}
