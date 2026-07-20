####> This option file is used in:
####>   podman podman-container.unit.5.md.in, podman-pod.unit.5.md.in
####> If file is edited, make sure the changes
####> are applicable to all of those.
### `SocketActivationPort=[hostIP:]hostPort:containerPort`

Enable systemd socket activation for the container or pod. When specified, Quadlet generates
a `.socket` unit and a `-proxy.service` unit alongside the main `.service` unit.

The socket listens on `hostPort` (or `hostIP:hostPort`). On an incoming connection, systemd activates
the proxy service (`systemd-socket-proxyd`), which forwards to `127.0.0.1:<internalPort>`. Podman DNAT
forwards `127.0.0.1:<internalPort>` into the container's `containerPort`.

Multiple `SocketActivationPort` entries are supported — each generates its own socket+proxy pair with
per-port naming (`<name>-<port>.socket` / `<name>-<port>-proxy.service`).

The internal port is auto-allocated starting from `max(1024, containerPort)` for rootless safety.
Sequential allocations are used for multiple entries to avoid collisions.

Only TCP is supported. Port ranges and empty host ports are rejected.

Example:
```
[Container]
Image=docker.io/library/nginx:alpine
SocketActivationPort=8080:80
SocketActivationPort=8443:443
```
