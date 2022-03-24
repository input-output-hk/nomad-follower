job "sleep" {
  datacenters = ["dc1"]
  group "sleep" {
    task "sleep" {
      driver = "podman"
      config {
        image = "docker://spaster/alpine-sleep"
      }
    }
  }
}
