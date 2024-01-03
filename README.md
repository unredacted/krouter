# krouter
A program to manage GRE tunnels and static routes on Linux

### Requirements

A config file that exists at `/etc/krouter/config.yml`

### Example configuration

```
program_settings:
  log_file_path: "/var/log/krouter/gre.log"
  logging:
    info: true
    error: true
    debug: false

gre_tunnels:
  - name: "gre1"
    local_ip: "192.168.1.1"
    remote_ip: "10.0.0.1"
    tunnel_ip: "10.0.0.2"
    subnet_mask: "30"
  - name: "gre2"
    local_ip: "192.168.1.2"
    remote_ip: "10.0.0.2"
    tunnel_ip: "10.0.0.3"
    subnet_mask: "30"

static_routes:
  - destination: "192.168.2.0/24"
    gateway: "192.168.1.254"
  - destination: "192.168.3.0/24"
    gateway: "192.168.1.254"

ecmp_routes:
  - route: "default"
    table: "GRE"
    nexthops:
      - dev: "gre1"
        via: "10.0.40.5"
        weight: 1
      - dev: "gre2"
        via: "10.0.41.5"
        weight: 1
```