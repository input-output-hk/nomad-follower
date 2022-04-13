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
      enable = lib.mkEnableOption "Enable the Nomad follower to send nomad logs to Loki";

      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.nomad-follower;
        description = ''
          The package used to start nomad-follower.
        '';
      };

      lokiUrl = lib.mkOption {
        type = lib.types.str;
        default = "http://monitoring:3100";
        description = ''
          Logs will be sent to Loki at this address.
        '';
      };

      prometheusUrl = lib.mkOption {
        type = lib.types.str;
        default = "http://monitoring:8428/api/v1/write";
        description = ''
          Metrics will be sent to VictoriaMetrics (or Prometheus)
        '';
      };

      nomadNamespace = lib.mkOption {
        type = lib.types.str;
        default = "*";
        description = ''
          Only match allocations in this Nomad Namespace
        '';
      };

      allocPattern = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/nomad/alloc/%%s/alloc";
        description = ''
          A pattern used to find the log files in allocations.
          Please note that this is subject to three levels of interpolation:

          * First level is systemd, it will replace all `%` prefixed strings as
            described in the systemd.unit(5) manpage under the SPECIFIERS
            section. That's why the default uses `%%s` to escape.
          * Second level is in Go, where we replace a single `%s` with the id
            of the allocation.
          * Finally, this is also a glob, so you can use Go glob patterns to
            match multiple directories (helpful for dev mode)
        '';
      };

      nomadAddr = lib.mkOption {
        type = lib.types.str;
        default = "https://127.0.0.1:4646";
        description = ''
          Location of a running Nomad client instance
        '';
      };

      nomadTokenFile = lib.mkOption {
        type = lib.types.str;
        default = "/run/keys/nomad-follower-token";
        description = ''
          Location of a file containing the Nomad token
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.nomad-follower = {
      wantedBy = ["multi-user.target"];
      after = ["nomad.service"];

      path = [pkgs.vector];

      environment = {
        NOMAD_ADDR = cfg.nomadAddr;
      };

      serviceConfig = {
        Restart = "on-failure";
        RestartSec = "10s";
        StateDirectory = "nomad-follower";
        WorkingDirectory = "/var/lib/nomad-follower";
        ExecStart = toString ([
          "@${cfg.package}/bin/nomad-follower"
          "nomad-follower"
          "--state"
          "/var/lib/nomad-follower"
        ] ++ lib.optionals (cfg.nomadTokenFile != "") [
          "--token-file"
          cfg.nomadTokenFile
        ] ++ [
          "--alloc"
          cfg.allocPattern
          "--loki-url"
          cfg.lokiUrl
          "--prometheus-url"
          cfg.prometheusUrl
          "--namespace"
          cfg.nomadNamespace
        ]);
        ExecReload = toString ["${pkgs.coreutils}/bin/kill" "-HUP" "$MAINPID"];
      };
    };
  };
}
