{
  description = "Flake for nomad-follower";

  inputs = {
    devshell.url = "github:numtide/devshell";
    inclusive.url = "github:input-output-hk/nix-inclusive";
    nixpkgs.url = "github:nixos/nixpkgs/nixpkgs-unstable";
    utils.url = "github:kreisys/flake-utils";
  };

  outputs = { self, nixpkgs, utils, devshell, ... }@inputs:
    utils.lib.simpleFlake {
      systems = [ "x86_64-linux" ];
      inherit nixpkgs;

      preOverlays = [ devshell.overlay ];

      overlay = final: prev: {
        nomad-follower = prev.buildGoModule rec {
          pname = "nomad-follower";
          version = "2021.11.11.001";
          vendorSha256 = "sha256-VhsjoBAMzbEcFoFQiJ5FF+MowFZevx03xOp2L1j8CFQ=";

          src = inputs.inclusive.lib.inclusive ./. [
            ./allocations.go
            ./events.go
            ./go.mod
            ./go.sum
            ./main.go
          ];

          CGO_ENABLED = "0";
          GOOS = "linux";

          ldflags = [
            "-s"
            "-w"
            "-extldflags"
            "-static"
            "-X main.buildVersion=${version} -X main.buildCommit=${
              self.rev or "dirty"
            }"
          ];
        };
      };

      packages = { nomad-follower }@pkgs:
        pkgs // {
          defaultPackage = nomad-follower;
        };

      hydraJobs = { nomad-follower }@pkgs: pkgs;

      devShell = { devshell }: devshell.fromTOML ./devshell.toml;
    };
}
