{
  lib,
  pkgs,
  inputs,
}:
pkgs.nixosTest {
  name = "nomad-follower";

  testScript = ''
    client.systemctl("is-system-running --wait")
    client.wait_for_unit("nomad-follower")
  '';

  nodes = {
    client = {
      imports = [inputs.self.nixosModules.nomad-follower];
      services.nomad-follower = {
        enable = true;
        package = pkgs.nomad-follower;
      };
    };
  };
}
