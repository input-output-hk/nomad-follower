{
  description = "Flake for nomad-follower";

  inputs = {
    devshell.url = "github:numtide/devshell";
    inclusive.url = "github:input-output-hk/nix-inclusive";
    nixpkgs.url = "github:nixos/nixpkgs/nixpkgs-unstable";
    utils.url = "github:kreisys/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    utils,
    devshell,
    ...
  } @ inputs:
    utils.lib.simpleFlake {
      systems = ["x86_64-linux"];
      inherit nixpkgs;

      preOverlays = [devshell.overlay];

      overlay = final: prev: {
        nomad-follower = prev.callPackage ./package.nix {
          inclusive = inputs.inclusive.lib.inclusive;
          rev = self.rev or "dirty";
        };
      };

      packages = {nomad-follower} @ pkgs:
        pkgs
        // {
          defaultPackage = nomad-follower;
        };

      hydraJobs = {
        nomad-follower,
        callPackage,
      }: {
        inherit nomad-follower;
        test = callPackage ./test.nix {inherit inputs;};
      };

      nixosModules.nomad-follower = import ./module.nix;

      devShell = {devshell}: devshell.fromTOML ./devshell.toml;
    };
}
