{
  pkgs,
  lib,
  ...
}: let
  mdformatPkg = pkgs.python3.withPackages (ps: [
    ps.mdformat
    ps.mdformat-gfm
    ps.mdformat-frontmatter
  ]);
in {
  cachix.enable = false;
  git-hooks.package = pkgs.prek;

  languages.go.enable = true;
  languages.go.package = pkgs.go_1_25;

  devcontainer.enable = true;

  packages = with pkgs;
    [
      gnumake
      git

      actionlint
      shellcheck
      shfmt
      alejandra
      deadnix
      statix

      mdformatPkg
    ]
    ++ lib.optionals (pkgs ? passt) [pkgs.passt];

  git-hooks.hooks = {
    check-added-large-files.enable = true;
    check-merge-conflicts.enable = true;
    check-json.enable = true;
    check-yaml.enable = true;
    end-of-file-fixer.enable = true;
    trim-trailing-whitespace.enable = true;

    markdownlint = {
      enable = true;
      args = ["--fix"];
      settings.configuration = {
        MD013 = {
          line_length = 100;
          code_blocks = false;
          headings = false;
          tables = false;
        };
      };
    };
    mdformat = {
      enable = true;
      package = mdformatPkg;
      args = [
        "--extensions"
        "gfm"
        "--extensions"
        "frontmatter"
        "--wrap"
        "100"
      ];
    };

    alejandra.enable = true;
    deadnix.enable = true;
    statix.enable = true;

    shellcheck.enable = true;
    shfmt.enable = true;
    actionlint.enable = true;

    gofmt.enable = true;
    golines.enable = true;

    govet.enable = true;
    staticcheck.enable = true;
    revive.enable = true;
    golangci-lint = {
      enable = true;
      pass_filenames = false;
    };

    gotest.enable = false;
    q15-go-test = {
      enable = true;
      name = "q15-go-test";
      entry = "make test";
      language = "system";
      pass_filenames = false;
      always_run = true;
    };
  };

  env = {
    GO111MODULE = "on";
    GOPROXY = "https://proxy.golang.org,direct";
    GOSUMDB = "sum.golang.org";
    CGO_ENABLED = "0";
  };

  enterShell = ''
    echo "📚 See AGENTS.md for project context and conventions"
  '';

  scripts = {
    build.exec = "make build";
    test.exec = "make test";
    run.exec = "make run";
  };
}
