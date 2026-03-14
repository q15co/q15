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
          vendorHash = "sha256-4grjVEpT9dUESNctj8ndUFlISwoJO3N+6/46+gZclP4=";
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
          vendorHash = "sha256-+VZjMRW9gEfhoIf0+z2hlXiZ95iyl9qxCeuc7N9hEN8=";
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
          vendorHash = "sha256-qUfHD4a1cD8tmilkfrF1shD+2C77uB8J97oavm7aFsA=";
          env = {
            GOWORK = "off";
          };
          postInstall = ''
            mv "$out/bin/proxy-service" "$out/bin/q15-proxy-service"
          '';
        };

        q15SandboxHelper = pkgs.buildGoModule {
          pname = "q15-sandbox-helper";
          inherit version;
          src = ./.;
          modRoot = "systems/sandbox-helper";
          subPackages = ["."];
          vendorHash = "sha256-n/NuOY9KC9/4XD25OmMaxlK8Ys8zdXsxcSkjoskG7ho=";
          tags = [
            "containers_image_openpgp"
            "exclude_graphdriver_btrfs"
          ];
          env = {
            GOWORK = "off";
          };
          preBuild = ''
            export CGO_ENABLED=1
          '';
          postInstall = ''
            mv "$out/bin/sandbox-helper" "$out/bin/q15-sandbox-helper"
          '';
        };
        q15Package = pkgs.symlinkJoin {
          name = "q15-${version}";
          paths = [
            q15Agent
            q15ExecService
            q15ProxyService
            q15SandboxHelper
          ];
        };
      in {
        q15-agent = q15Agent;
        q15-exec-service = q15ExecService;
        q15-proxy-service = q15ProxyService;
        q15-sandbox-helper = q15SandboxHelper;
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
