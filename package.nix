{
  buildGoModule,
  inclusive,
  rev,
}: let
  final = package "sha256-mBKpbFAQBioHlk9LJEMV2Uuls6KNP3ob4eU1MwMb67M=";
  package = vendorSha256:
    buildGoModule rec {
      pname = "nomad-follower";
      version = "2022.07.12.001";
      inherit vendorSha256;

      passthru.invalidHash =
        package "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

      src = inclusive ./. [
        ./go.mod
        ./go.sum

        ./allocations.go
        ./events.go
        ./main.go
      ];

      CGO_ENABLED = "0";
      GOOS = "linux";

      ldflags = [
        "-s"
        "-w"
        "-extldflags"
        "-static"
        "-X main.buildVersion=${version} -X main.buildCommit=${rev}"
      ];
    };
in
  final
