{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.nomad-follower;
in {
  options = {
    services.nomad-follower = {
      enable = lib.mkEnableOption "Enable the Nomad follower";

      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.nomad-follower;
      };

      lokiUrl = lib.mkOption {
        type = lib.types.str;
        default = "http://monitoring:3100";
      };

      nomadNamespace = lib.mkOption {
        type = lib.types.str;
        default = "cicero";
      };

      allocPattern = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/nomad/alloc/%%s/alloc";
      };

      vaultAddr = lib.mkOption {
        type = lib.types.str;
        default = "http://127.0.0.1:8200";
      };

      nomadAddr = lib.mkOption {
        type = lib.types.str;
        default = "https://127.0.0.1:4646";
      };

      vaultTokenFile = lib.mkOption {
        type = lib.types.str;
        default = "/run/keys/vault-token";
      };
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.nomad-follower = {
      wantedBy = ["multi-user.target"];
      after = ["nomad.service"];

      path = [pkgs.vector];

      environment = {
        VAULT_ADDR = cfg.vaultAddr;
        NOMAD_ADDR = cfg.nomadAddr;
        VAULT_TOKEN_FILE = cfg.vaultTokenFile;
      };

      serviceConfig = {
        Restart = "on-failure";
        RestartSec = "10s";
        StateDirectory = "nomad-follower";
        WorkingDirectory = "/var/lib/nomad-follower";
        ExecStart = toString [
          "@${cfg.package}/bin/nomad-follower"
          "nomad-follower"
          "--state"
          "/var/lib/nomad-follower"
          "--alloc"
          cfg.allocPattern
          "--loki-url"
          cfg.lokiUrl
          "--namespace"
          cfg.nomadNamespace
        ];
      };
    };
  };
}
