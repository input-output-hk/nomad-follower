# nomad-follower

Log forwarder for aggregating allocation logs from nomad worker agents.

## Running the application

Run the application on each worker in a nomad cluster.

* Requires the same permissions as Nomad to be able to read logs.

```
nix run github:input-output-hk/nomad-follower \
  --state /var/lib/nomad-follower \
  --alloc /var/lib/nomad/alloc \
  --loki-url http://127.0.0.1:3100
```

## Features

* Use Nomad Event Stream to discover allocations being started/stopped
* Update Vector configuration with new allocations
* Vector sends logs directly to Loki
