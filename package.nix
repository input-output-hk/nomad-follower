{
  buildGoModule,
  inclusive,
  rev,
}: let
  final = package "sha256-ELNQydQqAFp0POL/WEMBBdBOZuu+qXSbD922ku/ySac=";
  package = vendorSha256:
    buildGoModule rec {
      pname = "nomad-follower";
      version = "2022.03.04.003";
      inherit vendorSha256;

      passthru.invalidHash =
        package "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

      src = inclusive ./. [
        ./go.mod
        ./go.sum

        ./allocations.go
        ./events.go
        ./token.go
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
          rev
        }"
      ];
    };
in
  final
