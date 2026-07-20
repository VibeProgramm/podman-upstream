# SocketActivationPort Feature - Final Specification Document

## Document Information

| Field | Value |
|-------|-------|
| **Document Title** | SocketActivationPort Feature - Final Specification |
| **Version** | 1.6 |
| **Status** | Final (revised) |
| **Component** | Podman Quadlet (pkg/systemd/quadlet/) |
| **Author** | Podman Contributors |
| **Date** | 2025 |

---

## Table of Contents

0. [v1 Scope: Single Port](#0-v1-scope-single-port)
1. [Feature Overview and Goals](#1-feature-overview-and-goals)
2. [Syntax Specification](#2-syntax-specification)
3. [Unit File Generation](#3-unit-file-generation)
4. [Dependency Chain](#4-dependency-chain)
5. [Port Allocation Algorithm](#5-port-allocation-algorithm)
6. [Port Collision Detection](#6-port-collision-detection)
7. [Security Hardening](#7-security-hardening)
8. [IPv6 Handling](#8-ipv6-handling)
9. [Template Unit Support](#9-template-unit-support)
10. [Validation Rules](#10-validation-rules)
11. [Integration with Existing Features](#11-integration-with-existing-features)
12. [Rootless/Root Compatibility](#12-rootlessroot-compatibility)
13. [Systemd Version Requirements](#13-systemd-version-requirements)
14. [Test Scenarios](#14-test-scenarios)
15. [Differences from OnlyOffice Pattern](#15-differences-from-onlyoffice-pattern)
16. [Limitations](#16-limitations)
17. [Backwards Compatibility](#17-backwards-compatibility)
18. [Documentation Updates](#18-documentation-updates)

---

## 0. v1 Scope: Single Port

The first implementation of `SocketActivationPort` supports **exactly one** `SocketActivationPort`
entry per quadlet unit. This deliberate limitation exists because the multi-port design in earlier
drafts was **incorrect**:

- A `Type=simple` service cannot run multiple `systemd-socket-proxyd` processes via stacked
  `ExecStart=` lines (systemd refuses to load such a unit).
- A single `systemd-socket-proxyd` accepts only **one** backend address; it cannot route different
  listen sockets to different backends.
- Multiple `ListenStream` entries in one `.socket` passed to one proxyd cannot be split per-port.

A correct multi-port design requires either one socket+proxy pair per port (more generated units) or a
template proxy instantiated per port. That is deferred to a follow-up change (see §16) so the initial
PR stays small, correct, and reviewable.

**What single-port v1 delivers:** on-demand container startup for one host port, with the correct
activation chain `socket → proxy → container` (see §3 and §4). Everything else in this document
(parsing, validation, IPv6 handling, templates, rootless/root, docs) applies; only the "multiple
entries" examples are out of scope for v1 and should be read as "future work".

> If more than one `SocketActivationPort` entry is given in v1, the generator emits a clear error
> (`SAP014`): `SocketActivationPort supports at most one entry in this version (multiple ports are planned)`.

---

## 1. Feature Overview and Goals

### 1.1 Purpose

The **SocketActivationPort** feature enables systemd socket activation for container ports in Podman Quadlet. When enabled, Podman generates additional systemd units (`.socket` and `-proxy.service`) alongside the existing `.service` unit, allowing containers to start on-demand when a client connects to the specified host port.

### 1.2 Goals

- **On-demand container startup**: Containers start only when a connection arrives, reducing resource consumption for infrequently accessed services. (v1 implements lazy *start* only — there is no automatic idle *shutdown*; see §16. An idle container keeps running after the first connection. Auto-stop on idle is deferred to a follow-up.)
- **Security hardening**: Internal proxy ports bind only to `127.0.0.1`, never to `0.0.0.0` or `[::]`
- **Docker-compatible syntax**: Reuse the existing `PublishPort` syntax parser for familiarity
- **Seamless integration**: Works alongside existing `PublishPort` and `ExposeHostPort` in the same unit
- **Template unit support**: Full support for systemd template units (`@` syntax)
- **Rootless and root mode**: Works in both rootless (user systemd) and root (system systemd) modes

### 1.3 Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                        User connects to host:8080                    │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│  systemd socket unit (web.socket)                                    │
│  ListenStream=8080   (Service=web-proxy.service)                     │
└─────────────────────────────────────────────────────────────────────┘
                                 │
                     Connection arrives → socket activates proxy
                                 │
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  systemd starts web-proxy.service                                    │
│  ExecStartPre=wait for 127.0.0.1:80 (container ready)               │
│  ExecStart=<proxyd-path> 127.0.0.1:80       │
│  Requires/After/PartOf=web.service  (container starts first)         │
└─────────────────────────────────────────────────────────────────────┘
                                 │
                     Proxies connection to 127.0.0.1:80
                                 │
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  systemd starts web.service (container)                              │
│  podman run --publish 127.0.0.1:80:80 ...                           │
└─────────────────────────────────────────────────────────────────────┘
```

### 1.4 Generated Units

For a quadlet file `web.container`, **three** systemd units are generated:

| Unit File | Purpose | Type |
|-----------|---------|------|
| `web.service` | Main container service (modified with `--publish 127.0.0.1:<internal>:<container>`) | Existing, modified |
| `web.socket` | systemd socket unit with `ListenStream` for the activated port; `Service=web-proxy.service` | **NEW** |
| `web-proxy.service` | Proxy service running `systemd-socket-proxyd`, activated by the socket, depends on the container | **NEW** |

> **v1 scope (see §0):** only a **single** `SocketActivationPort` entry is supported per unit in the
> first version. Multi-port support is deferred to a follow-up change (see §16). The single-port design
> is what makes the activation chain below correct and mergeable.

---

## 2. Syntax Specification

### 2.1 Key Name and Location

| Group | Key | Type | Multiple |
|-------|-----|------|----------|
| `[Container]` | `SocketActivationPort` | String | **No** (single entry per §0) |
| `[Pod]` | `SocketActivationPort` | String | **No** (single entry per §0) |
| `[Container]` | `SocketActivationPortOptions` | String array | Yes |
| `[Pod]` | `SocketActivationPortOptions` | String array | Yes |
| `[Container]` | `SocketActivationInternalPort` | Integer | No (single) |
| `[Pod]` | `SocketActivationInternalPort` | Integer | No (single) |

> **v1 scope (see §0):** `SocketActivationPort` accepts **exactly one** entry in `[Container]` and
> `[Pod]`. The "Multiple: Yes" wording from earlier drafts was wrong and is corrected here. A second
> `SocketActivationPort` entry in v1 is a generation error (`SAP014`, Appendix C). `[Kube]` does **not**
> support `SocketActivationPort` in v1 (see §11.4 / `SAP013`).

### 2.2 Accepted Formats

The syntax **reuses** `CreatePortBindings` from `pkg/specgenutil/util.go` for initial parsing. Note
that `CreatePortBindings` itself accepts port ranges, `/udp`, `/sctp`, and empty host ports; the
SAP-specific rejections in §2.3 are enforced by **explicit post-parse validation** after the parser
returns (see §10.2).

| Format | Description | Example |
|--------|-------------|---------|
| `hostPort:containerPort` | Basic IPv4 (all interfaces) | `8080:80` |
| `hostIP:hostPort:containerPort` | Explicit IPv4 address | `127.0.0.1:8443:443` |
| `0.0.0.0:hostPort:containerPort` | All IPv4 interfaces | `0.0.0.0:8080:80` |
| `[::]:hostPort:containerPort` | All IPv6 interfaces (dual-stack under bindv6only=0, see §8.1) | `[::]:8080:80` |
| `[::1]:hostPort:containerPort` | IPv6 loopback | `[::1]:8443:443` |
| `[2001:db8::1]:hostPort:containerPort` | Specific IPv6 address | `[2001:db8::1]:8080:80` |
| `hostPort:containerPort/tcp` | Explicit TCP protocol | `8080:80/tcp` |

### 2.3 Rejected Formats (Parse-Time Errors)

| Format | Reason | Error Message |
|--------|--------|---------------|
| `:8080:80` | Empty host port | `SocketActivationPort requires explicit host port (empty host port not allowed)` |
| `[::]:8080:80` (without port) | Empty host port with IPv6 | `SocketActivationPort requires explicit host port (empty host port not allowed)` |
| `8080-8090:80` | Port range | `SocketActivationPort does not support port ranges` |
| `8080:80/udp` | UDP protocol | `SocketActivationPort only supports TCP protocol` |
| `8080:80/sctp` | SCTP protocol | `SocketActivationPort only supports TCP protocol` |
| `0:80` | Port 0 invalid | `port numbers must be between 1 and 65535` |
| `65536:80` | Port > 65535 | `port numbers must be between 1 and 65535` |
| `8080:` | Missing container port | `must provide a non-empty container port to publish` |
| `invalid` | Invalid format | `invalid port format` |

### 2.4 SocketActivationPortOptions

| Key | Type | Description | Example |
|-----|------|-------------|---------|
| `SocketActivationPortOptions` | String array | Additional flags passed to `systemd-socket-proxyd` | `--timeout=30s --buffer-size=65536` |

Each array entry is read with the same helper Quadlet uses for other argument lists (e.g.
`LookupAllArgs` / `LookupAllStrv` + `escapeWords`), so quoting and escaping follow systemd `ExecStart`
rules — **not** an ad-hoc shell split. The resolved arguments are appended to the `ExecStart` line of
the proxy service as discrete argv elements.

**Supported `systemd-socket-proxyd` options:**
- `--timeout=SECONDS` - Connection timeout (default: 30s)
- `--buffer-size=BYTES` - Buffer size (default: 65536)
- `--verbose` - Verbose logging

> **Unknown-option validation:** options that are not recognized `systemd-socket-proxyd` flags are
> rejected at generation with `SAP017` (`SocketActivationPortOptions contains unknown option %q`). An
> unknown token passed straight to `ExecStart` would only fail at runtime and silently disable the
> proxy. Validate the set above (and the `--key=value` form) before emitting the unit.

---

## 3. Unit File Generation

### 3.1 Naming Convention

> **Naming rule:** the proxy service is named `<name>-proxy.service` so that the generated `.socket`
> unit can reference it via `Service=<name>-proxy.service`. For template units the proxy is a
> **template** named `<name>-proxy@.service` (instances `<name>-proxy@1.service`), which keeps a clean
> `%i` specifier for the socket name. The previously considered `web@-proxy.service` form is
> **invalid** — systemd would parse it as template `web` with instance `-proxy`, not as a proxy of
> `web@`.

| Input Quadlet | Generated Service | Generated Socket | Generated Proxy Service |
|---------------|-------------------|------------------|-------------------------|
| `web.container` | `web.service` | `web.socket` | `web-proxy.service` |
| `web@.container` | `web@.service` | `web@.socket` | `web-proxy@.service` |
| `app.kube` | `app.service` | `app.socket*` | `app-proxy.service*` |

> `*`: `[Kube]` units do **not** support `SocketActivationPort` in v1 (see §11.4 / `SAP013`), so these
> generated units are **not** produced for Kube inputs. The row is kept only to show what *would* be
> named; in v1 a `[Kube]` unit with `SocketActivationPort` is a generation error.
| `db.pod` | `db-pod.service` | `db.socket` | `db-proxy.service` |

### 3.2 Generated Unit Contents

#### 3.2.1 Modified Main Service Unit (`<name>.service`)

```ini
[Unit]
Description=Web container

[Service]
# ... existing ExecStart with ADDED --publish flags:
# Default internal port == container port (80), so --publish 127.0.0.1:80:80
ExecStart=/usr/bin/podman run --name systemd-%N \
  --publish 127.0.0.1:80:80 \
  myimage
```

> **IMPLEMENTATION HAZARD (C1/K.4):** Do **NOT** create a second `ExecStart=` line by calling
> `service.AddCmdline(ServiceGroup, "ExecStart", ...)` a second time. That produces two `ExecStart=`
> lines and systemd rejects the unit at load time. Instead, **append** `"--publish", "127.0.0.1:<internal>:<container>"`
> to the **existing** `podman.Args` argv slice **before** the single
> `service.AddCmdline(ServiceGroup, "ExecStart", podman.Args)` call at `quadlet.go:~941`. The
> slice already contains the main podman command; you are adding one more argument to it, not a
> second command line. See §K.4 for the exact insertion point relative to `handlePublishPorts` and
> `handlePodmanArgs`.

**Key modifications:**
- Adds `--publish 127.0.0.1:<internalPort>:<containerPort>` for the single SocketActivationPort entry.
  With the default internal port (equal to the container port), this is `--publish 127.0.0.1:80:80`
  for `SocketActivationPort=8080:80`. (An explicit `SocketActivationInternalPort` would change the
  first number only.)
- **No socket dependencies are added to the container service.** The container must be free to start
  and stop independently (including via `Restart=` and idle shutdown) without tearing down the socket.
  Adding `BindsTo=<name>.socket` here would create a fatal feedback loop: stopping/restarting the
  container would stop the socket and the proxy, so the next connection could never activate it.
- The container service is pulled in by the **proxy** service (see §3.2.3 / §4), not by the socket
  directly.
- **`Restart=on-failure` is set on the container service for SAP units** (Quadlet's `ConvertContainer`
  does not set a default `Restart`, so without this the container would not auto-recover from a crash and
  the socket→proxy→container chain would break after the first failure). This is independent of the
  proxy's `Restart=no` (§3.2.3).
  - **Precedence guard (K2):** set `Restart=on-failure` **only if** the user did not already set
    `Restart=` in `[Service]`. Use `if !service.HasKey(ServiceGroup, "Restart") { service.Set(ServiceGroup, "Restart", "on-failure") }`
    (mirroring `ConvertPod`'s existing pattern). Silently overwriting a user's `Restart=no` would
    disable their intended crash behavior.
  - **Philosophy note (V7):** silently changing the container's `Restart` is a deviation from Podman's
    "don't alter semantics the user didn't ask for" principle. For v1 the generator injects
    `Restart=on-failure` because without it a crashed container would break the socket→proxy→container
    chain after the first failure. This is **documented implicit behavior**; a future revision may
    expose it as an explicit `SocketActivationRestart=` key (or stop touching `Restart` and instead rely
    on the socket's `StartupRatePerSec` + re-activation). Document this injection prominently so users
    are not surprised.

> **Generator note:** the multi-unit emission is detailed in Appendix A.4 (the write/enable loop must
> emit+enable the `.socket` and `-proxy.service` too, not just the container service).

#### 3.2.2 Generated Socket Unit (`<name>.socket`)

```ini
[Unit]
Description=Web container socket

[Socket]
# The socket activates the PROXY service, NOT the container directly.
# Without Service=, systemd would activate the same-named web.service (the container),
# which would receive the listen fds and bypass the proxy entirely.
Service=web-proxy.service

# Name the passed fd. NOTE (K14): this is COSMETIC — systemd-socket-proxyd selects the listen fd by
# INDEX (fd 3), not by FileDescriptorName. FileDescriptorName does not gate or change which fd the
# proxy receives; it only aids human readability. Keep it, but do not rely on it for correctness.
FileDescriptorName=proxy

# One ListenStream for the activated port (see §8.1 for IPv6 / dual-stack rules)
ListenStream=8080

# Socket options for security
Accept=no
MaxConnections=64
KeepAlive=yes
# Bound per-connection activation cost when the backend is permanently down (F9/K15): rate-limit
# how often the socket may (re)activate the proxy. Prevents an unbounded per-connection probe storm.
StartupRatePerSec=10

[Install]
WantedBy=sockets.target
```

**Rules for ListenStream generation:**
- IPv4 `0.0.0.0:hostPort` or bare `hostPort` → `ListenStream=hostPort` (the bare numeric form is
  dual-stack under `bindv6only=0` via IPv6; `0.0.0.0:` itself is IPv4-only, so do **not** also emit
  `ListenStream=[::]:hostPort` for the same port — see §8.1).
- IPv4 `127.0.0.1:hostPort` → `ListenStream=127.0.0.1:hostPort`
- IPv6 `[::]:hostPort` → `ListenStream=[::]:hostPort` (all IPv6)
- IPv6 `[::1]:hostPort` → `ListenStream=[::1]:hostPort`

**Critical:** `Service=<name>-proxy.service` is **required**. A `.socket` with `Accept=no` and no
`Service=` activates the service with the same base name (here `web.service`, the container), which is
wrong. The proxy must be the activated unit so it receives the listening fds.

#### 3.2.3 Generated Proxy Service Unit (`<name>-proxy.service`)

```ini
[Unit]
Description=Web container socket proxy
# The proxy pulls up the container before/alongside accepting connections.
Requires=web.service
After=web.service
# A stopped (or manually stopped) container must not keep the proxy alive forever
# (see §4.1 / B3). PartOf the container means stopping the container also stops the proxy.
PartOf=web.service

[Service]
# Type=simple: systemd-socket-proxyd does NOT send sdnotify READY, so Type=notify would
# leave the unit in "starting" forever (and a paired ExecStartPost=systemd-notify --ready
# is doubly wrong — it only runs AFTER ExecStart, which for the persistent proxyd never
# exits, so --ready would never fire, or would fire prematurely while the backend is still
# down). The sole readiness gate is the ExecStartPre poll-loop below (B1). Keep Type=simple.
Type=simple
# Single backend — systemd-socket-proxyd takes exactly ONE target address.
# The listen fds are inherited from the .socket unit (Service= in §3.2.2).
#
# Readiness (B1): systemd-socket-proxyd does NOT retry a refused backend, so the first
# connections arriving while the container is still starting up would be dropped. We gate
# startup on the backend actually accepting on 127.0.0.1:<internal> BEFORE the proxy starts.
#
# The readiness gate is an ExecStartPre poll-loop that probes 127.0.0.1:<internal> until the
# container is listening (or a generous timeout elapses). The path to bash is resolved via
# exec.LookPath at generation time (NOT hardcoded; use <bash-path> as a placeholder below); if
# bash is absent the generator should substitute a minimal wait helper or error. The connect()
# done by the loop IS permitted by SystemCallFilter=@system-service (it allows socket+connect); do NOT
# add a stricter filter that would block it — see §7.1 (V11) for the rationale and the recommended
# fallback (socat/python) if a stricter filter is in force.
# Timeout is raised to ~120s to cover image pulls on first activation (was 30s).
#
# PORTABILITY CAVEAT (C4/H4): exec.LookPath resolves systemd-socket-proxyd and bash to absolute
# paths at generation time, baking them into the unit file. Generated units are therefore
# non-portable across systems with different filesystem layouts. If portability is required,
# set Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin in the
# proxy's [Service] and use bare command names (not resolved absolute paths). This
# trade-off is documented for operators.
ExecStartPre=<bash-path> -c 'for i in $(seq 1 600); do exec 3<>/dev/tcp/127.0.0.1/80 && exit 0; sleep 0.2; done; exit 1'
# NOTE: do NOT add ExecStartPost=systemd-notify --ready. proxyd never sends READY and
# ExecStartPost runs only after ExecStart exits, so it is dead/incorrect for a persistent proxy.
# The proxyd path is resolved via exec.LookPath at generation (never hardcoded; <proxyd-path> below).
ExecStart=<proxyd-path> 127.0.0.1:80
# With options:
# ExecStart=<proxyd-path> --timeout=30s --buffer-size=65536 127.0.0.1:80
#
# Restart=no: the proxy is socket-activated; it only runs while a connection is being
# proxied. If the container (backend) is down, restarting the proxy would create a
# restart-storm fed by incoming traffic (B2). The socket reactivates the proxy on the
# next connection once the container is back. The container itself keeps Restart=on-failure
# (subject to the user's own Restart= precedence, see §3.2.1). To bound the per-connection
# probe cost when the backend is permanently down, the socket rate-limits activations via
# StartupRatePerSec (see §3.2.2); do NOT set Restart=on-failure on the proxy.
Restart=no

# Security hardening — see §7.1. NOTE: MemoryDenyWriteExecute / RestrictNamespaces are
# intentionally NOT set here.
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
NoNewPrivileges=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=yes
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

# No [Install] section: the proxy is started by the socket via Service= (H7). Enabling the
# proxy directly would start it "in the void" without listening fds and it would exit immediately.
```

**Critical Rules:**
- **Exactly ONE `ExecStart`** — `systemd-socket-proxyd` handles only ONE target address per process, and
  a `Type=simple` unit may not have multiple `ExecStart=` lines (systemd refuses to load it). For a
  single port this is correct. Multi-port support (deferred to a follow-up, see §16) requires either
  one socket+proxy pair per port or a template proxy instantiated per port.
- **Internal ports always bind to `127.0.0.1`** — Never `0.0.0.0` or `[::]`.
- **The proxy `Requires`/`After`/`PartOf` the container service** (B3): `After=` guarantees the
  container is *started* before the proxy runs, and `PartOf=` guarantees that stopping the container
  also stops the proxy so a dead backend cannot leave the proxy listening and refusing connections.
   Because `systemd-socket-proxyd` does not retry a refused backend connection, the **readiness gate**
  (B1) is the `ExecStartPre` poll-loop in §3.2.3 — it probes `127.0.0.1:<internal>` until the container
  is actually *listening* before the proxy forwards the first connection. Do **not** use
  `Type=notify`/`ExecStartPost=systemd-notify --ready` (proxyd sends no READY; see §3.2.3 note).
- **`Restart=no` on the proxy (B2):** the proxy is driven by socket activation, not by a persistent
  `Restart=on-failure`. If the container backend is down, the proxy must not be restarted in a tight
  loop. The socket reactivates it on the next inbound connection once the backend is back. The
  container service has `Restart=on-failure` **explicitly set by the generator for SAP units** (Quadlet
  does not default it), so a crashing container is recovered independently.
- **Build `ExecStart` programmatically:** the proxy `ExecStart` MUST be assembled with
   `service.AddCmdline(ServiceGroup, "ExecStart", args)` where `args[0]` is the resolved path of
   `systemd-socket-proxyd` (looked up via `exec.LookPath`, **never hardcoded** as
   `<proxyd-path>`), followed by the resolved `SocketActivationPortOptions`
  tokens and the backend address `127.0.0.1:<internal>`. This keeps quoting/escaping correct and
  applies the SAP010 warning (H6, §10.2). The readiness `ExecStartPre` bash path is likewise resolved
  via `exec.LookPath` (or a generated wait helper), never hardcoded.
- **Security hardening directives** — Applied to proxy service for defense-in-depth; see §7.1 for the
  exact set and the rationale for omitting `MemoryDenyWriteExecute`/`RestrictNamespaces`.

---

## 4. Dependency Chain

### 4.1 Systemd Directives Summary

| Unit | Requires | After | PartOf | BindsTo | WantedBy |
|------|----------|-------|--------|---------|----------|
| `<name>.socket` | — | — | — | — | `sockets.target` (via `[Install]`) |
| `<name>-proxy.service` | `<name>.service` | `<name>.service` | `<name>.service` | — | — (enabled only via the socket) |
| `<name>.service` | — | — | — | — | — (no socket deps; user `[Install]` applies) |

> **Why the container has no socket dependencies:** if the container `BindsTo`/is `RequiredBy` the
> socket, stopping or restarting the container would tear down the socket and the proxy, breaking
> on-demand activation permanently. The socket activates the **proxy**, and the proxy depends on the
> container — the one-directional chain below.
>
> **Why the proxy has `PartOf=<name>.service` (B3):** systemd socket activation does not stop the proxy
> when the container stops. Without `PartOf=`, a `podman stop web` (or a container crash) leaves
> `web-proxy.service` running and listening on the socket; every subsequent connection is then refused
> and the container is never reactivated. `PartOf=` ensures stopping the container also stops the proxy,
> and the socket (which remains active) reactivates the proxy on the next connection. To stop everything
> (socket + proxy + container), mask/stop the `.socket` unit; stopping only the container keeps the
> socket alive for on-demand restart.
>
> **Known footgun (K5):** `systemctl stop web.socket` (and the associated proxy, via `PartOf`) leaves
> `web.service` (the container) **running and reachable only via loopback** — external traffic can no
> longer reach it because the socket that owned the host port is gone. The container keeps running until
> explicitly stopped (`systemctl stop web.service`). Document this: to fully quiesce a SAP unit, stop the
> `.service` as well; an idle, already-activated container is **not** auto-stopped in v1 (see §1.2/§16).
>
> > **`PartOf` propagation gaps (SEC-C2):** `PartOf=<name>.service` propagates **only explicit systemd
> > stop/restart operations** to the proxy. It does **not** cover:
> > 1. **SIGKILL / OOM-kill** of the container: systemd does not propagate `PartOf` when a unit
> >    transitions to `failed` via a signal. The proxy survives, listening on the inherited socket fd
> >    and forwarding connections to a dead backend.
> > 2. **Direct `podman stop <container>`** (not via `systemctl`): systemd sees the main PID exit but
> >    does not trigger `PartOf` propagation because the stop did not originate from systemd's unit
> >    manager. The proxy remains running.
> > 3. **Crash-loop exhaustion:** if `Restart=on-failure` exhausts its retries (systemd defaults:
> >    `StartLimitBurst=5` / `StartLimitIntervalSec=10s`), `web.service` enters `failed` state. `PartOf`
> >    does not propagate on failure — the proxy stays alive indefinitely.
> >
> > **Mitigation:** document these leak scenarios prominently. For SIGKILL/OOM cases, the proxy's next
> > `connect()` to `127.0.0.1:<internal>` will fail with ECONNREFUSED and the connection drops — the
> > socket will reactivate the proxy again on the next inbound connection. For permanent-backend failures,
> > the `StartupRatePerSec=10` limit on the socket (§3.2.2) bounds the re-activation storm.
> > `StopWhenUnneeded=yes` on the proxy is a future option but risks premature shutdown during
> > connection-idle windows.

### 4.2 Dependency Diagram

```
                    ┌─────────────────┐
   client ────────► │  <name>.socket  │   Service=<name>-proxy.service
                    │  (ListenStream) │   ListenStream=hostPort
                    └────────┬────────┘
                             │  connection activates the proxy (passes listen fds)
                             ▼
                   ┌─────────────────────┐
                   │ <name>-proxy.service│   Requires/After/PartOf <name>.service
                   │  systemd-socket-    │   forwards to 127.0.0.1:<internal>
                   │  proxyd 127.0.0.1:  │
                   │  <internal>         │
                   └──────────┬──────────┘
                              │  pulls up the container first
                              ▼
                  ┌─────────────────────┐
                  │   <name>.service    │   podman run --publish
                  │  (container runs)   │   127.0.0.1:<internal>:<containerPort>
                  │  listens on         │
                  │  127.0.0.1:<internal>│
                  └─────────────────────┘
```

**Activation flow on a connection to `hostPort`:**
1. systemd accepts the connection on `<name>.socket` and starts `<name>-proxy.service` (named via
   `Service=` in the socket), passing the listening fd(s).
2. The proxy's `Requires=`/`After= <name>.service` ensures the container is started (and is listening
   on `127.0.0.1:<internal>`) before the proxy forwards.
3. `systemd-socket-proxyd` proxies the connection from `hostPort` to `127.0.0.1:<internal>`, which
   Podman DNATs into the container's `<containerPort>`.

### 4.3 Template Unit Dependencies

For template units (`web@.container`), the proxy is a template `web-proxy@.service` and the socket is
`web@.socket`. The proxy references the **container** service and its own socket via `%i`:

**web@.service:** (no socket dependencies — see §4.1)
```ini
[Service]
ExecStart=/usr/bin/podman run --name systemd-%p_%i ...
```

**web@.socket:**
```ini
[Socket]
Service=web-proxy@%i.service
ListenStream=8080
```

**web-proxy@.service:**
```ini
[Unit]
Requires=web@%i.service
After=web@%i.service
PartOf=web@%i.service

[Service]
ExecStart=<proxyd-path> 127.0.0.1:80
```

**Instance `web@1` resolves to:**
- container: `web@1.service`
- socket: `web@1.socket` (activates `web-proxy@1.service`)
- proxy: `web-proxy@1.service` (Requires/After `web@1.service`)

---

## 5. Port Allocation Algorithm

### 5.1 Internal Port Selection

The internal proxy port is where the container publishes itself on the **host loopback**
(`127.0.0.1:<internal>`) and where `systemd-socket-proxyd` forwards to. It must never be reachable
from outside the host.

The internal port is chosen as follows, in priority order:

1. **Explicit override** — if the user sets a new key `SocketActivationInternalPort=<port>`, that value
   is used directly. It is validated to be 1–65535 **and must not collide** with any host port,
   container port, or `ExposeHostPort` in the same unit (see §5.2). If it collides, generation fails
   with `SAP008` (we do **not** silently search — an explicit value is a deliberate, fixed choice).
2. **Container port by default** — if no explicit override is given, the internal port **equals the
   container port** (`internalPort = containerPort`). Rationale: the container already listens on that
   port inside its network namespace, and binding it on `127.0.0.1` on the host is unambiguous and
   discoverable (`--publish 127.0.0.1:80:80`). This replaces the previous magic `+10000` offset,
   which collided with common dev ports (e.g. 18080) and was non-discoverable.
3. **Auto-allocation (fallback)** — if the chosen/default internal port is already taken within the
   unit (see §5.2), the generator searches upward from the default for the next free port in
   1–65535 and reports an error only if none is found.

> **Self-loop prohibition (H10):** the proxy forwards `hostPort → 127.0.0.1:<internal>`. If
> `<internal> == hostPort` (the proxy's backend port equals the socket's listening port), the proxy
> forwards connections straight back into the socket itself — an infinite loop. This is checked against
> the **computed** internal port (after the explicit-override / container-port-default rule below), so
> both `SocketActivationPort=8080:8080` (default internal = container port = 8080 == hostPort) **and**
> `SocketActivationPort=8080:80` + `SocketActivationInternalPort=8080` (explicit internal 8080 == hostPort)
> are rejected with `SAP015` (Appendix C). To expose a container whose app listens on 8080, use a
> different `hostPort` (e.g. `80:8080`) so the internal port (8080) differs from the host port (80).

> **Why not `hostPort + 10000`:** it is an arbitrary magic offset that collides with real services,
> exhausts for `hostPort > 55535`, and gives no runtime discoverability. Using the container port
> (or an explicit user port) is clearer and avoids a whole class of collisions.

### 5.2 Used Ports Tracking

The set of **forbidden internal ports** is initialized from everything that binds on the host or is
published into the container within the same unit:

1. All `PublishPort` **host ports** (they bind on the host).
2. All `PublishPort` **container ports** (they are published into the same container; an internal port
   equal to a published container port would create a double-bind inside the container).
3. All `ExposeHostPort` ports.
4. Any explicit `SocketActivationInternalPort` values.
5. The SAP **host port** itself (see §5.1/H10): the internal port must differ from the socket's
   listening `hostPort`, otherwise the proxy forwards back into the socket (self-loop, `SAP015`).

> **Previously critical bug:** the old algorithm only tracked host ports in `usedPorts`, so an internal
> port equal to a *container* port of another mapping went undetected and produced a double-bind. The
> set above includes container ports to prevent that. The internal port is also explicitly forbidden
> from equaling the SAP `hostPort` (rule 5) and from colliding with an explicit
> `SocketActivationInternalPort` (rule 4 → `SAP008`). Port **ranges** and **empty/auto** ports in
> `PublishPort` are resolved before this check (an auto port `:80` yields a random host port at runtime
> and is therefore excluded from the static `usedPorts` set, but its container port `80` is still
> included per rule 2).

### 5.3 Examples

| Scenario | Host:Container | Explicit Internal | Internal Port Result |
|----------|----------------|-------------------|----------------------|
| Single SAP, default | 8080:80 | none | 80 |
| Explicit internal | 8080:80 | `SocketActivationInternalPort=18080` | 18080 |
| SAP + PP host conflict | SAP 8080:80, PP host 80 | none | 80 conflicts with PP host 80 → search → 81 |
| SAP + PP container conflict | SAP 8080:80, PP `:80` (container 80) | none | 80 conflicts with PP container 80 → search → 81 |
| Exhausted | all 1–65535 used | none | **ERROR** |

### 5.4 Port Exhaustion Handling

- Internal port range: **1–65535**.
- The allocation loop must **return an error when it reaches the end of the range**; the loop bound is
  `candidate > 65535`, and the error is raised explicitly after the loop (the previous version's error
  was unreachable because the loop exited silently returning an invalid port).
- Error message: `no available internal port found for socket activation (exhausted port range)`

---

## 6. Port Collision Detection

### 6.1 Collision Rules

| Collision Type | Detection Time | Action |
|----------------|----------------|--------|
| Two `SocketActivationPort` with same **internal port** | Generation | Error: `internal port %d already in use by another port mapping` |
| `SocketActivationPort` internal port conflicts with `PublishPort` host port | Generation | Error: `internal port %d already in use by another port mapping` |
| `SocketActivationPort` internal port conflicts with `ExposeHostPort` | Generation | Error: `internal port %d already in use by another port mapping` |
| Two `SocketActivationPort` with same **host port** | Generation | Allowed (different internal ports allocated) |
| Host port already in use by another service | Runtime (systemd) | systemd socket activation fails with clear error |

### 6.2 Internal Port Uniqueness

**Within a single unit**, all internal ports must be unique across:
- `SocketActivationPort` entries (calculated internal ports)
- `PublishPort` entries (host ports **and** container ports)
- `ExposeHostPort` entries (container ports)
- the SAP `hostPort` itself (must differ from the internal port — see §5.1 / `SAP015`)

> **Duplicate host-port with `PublishPort` (K13):** if the SAP `hostPort` equals a `PublishPort` host
> port in the same unit, the `.socket` and the published port both try to own the same host port →
> generation error `SAP019` (`SocketActivationPort host port %d conflicts with PublishPort host port`).
> The host port is owned by the `.socket`; do not also `PublishPort` it.

### 6.3 Cross-Unit Collisions

- **Not detected at generation time** — different quadlet files generate independent units
- **Detected at runtime** — systemd will fail to start the second socket unit with "Address already in use"
- This is consistent with existing `PublishPort` behavior
- **Cross-unit SAP is worse than PublishPort (K24):** a colliding SAP `.socket` takes down
  `sockets.target` activation for the whole unit graph, not just one container. Document this prominently
  so operators avoid reusing a host port across SAP units.

---

## 7. Security Hardening

### 7.1 Proxy Service Hardening Directives

The generated `<name>-proxy.service` includes the following security directives:

| Directive | Value | Rationale |
|-----------|-------|-----------|
| `PrivateTmp` | `yes` | Isolated /tmp |
| `ProtectSystem` | `strict` | Read-only /usr, /boot, /etc |
| `ProtectHome` | `yes` | No access to home directories |
| `NoNewPrivileges` | `yes` | Prevent privilege escalation |
| `ProtectKernelTunables` | `yes` | No kernel sysctl changes |
| `ProtectKernelModules` | `yes` | No module loading |
| `ProtectControlGroups` | `yes` | No cgroup modifications |
| `RestrictAddressFamilies` | `AF_INET AF_INET6 AF_UNIX` | Limit socket families (no `AF_NETLINK`: proxyd needs no netlink; this also mirrors §3.2.3) |
| `RestrictRealtime` | `yes` | No real-time scheduling |
| `PrivateDevices` | `yes` | No access to `/dev` nodes |
| `RestrictSUIDSGID` | `yes` | No setuid/setgid execution |
| `LockPersonality` | `yes` | No personality changes |
| `SystemCallFilter` | `@system-service` | Minimal syscall set |
| `SystemCallErrorNumber` | `EPERM` | Denied syscalls return EPERM |

> **Hardening directives requiring per-distro testing (SEC-H1):** `RestrictNamespaces=yes` and
> `MemoryDenyWriteExecute=yes` are **not** applied by default because `systemd-socket-proxyd`
> is a multi-threaded dispatcher that forks per-connection helpers. **However**, stock
> `systemd-socket-proxyd` on mainstream Linux distributions (Fedora, Debian, Ubuntu, Arch)
> does **not** create secondary namespaces and does **not** JIT or W^X-violate — it uses
> `malloc()` without `mmap(PROT_WRITE|PROT_EXEC)`. The spec's v1 conservative default omits
> these two directives, but **operators are strongly encouraged to test** `RestrictNamespaces=yes`
> and `MemoryDenyWriteExecute=yes` and add them via a drop-in if their distro's
> `systemd-socket-proxyd` build tolerates them. If CI testing in Enforcing SELinux mode
> confirms both directives are safe on all target distros, a subsequent revision should
> enable them by default.

> **Readiness-gate risks (V11 — SEC-C1):** the `ExecStartPre` poll-loop (§3.2.3) uses `bash /dev/tcp`
> which has **multiple failure modes**:
> 1. `/dev/tcp` is a **bash compile-time feature** (`--enable-net-redirections`); bash builds without it
>    (common in minimal/embedded distros, Alpine) silently skip the redirect — the loop runs all 600
>    iterations and always exits 1 with no diagnostic.
> 2. Shell injection surface: user-controlled tokens (`SocketActivationPortOptions`) executed via
>    `bash -c` script string.
> 3. The poll runs under `SystemCallFilter=@system-service` / `NoNewPrivileges`; the `bash /dev/tcp`
>    trick performs `socket()`+`connect()`, which `@system-service` **permits**, but any operator-added
>    stricter filter (or a build where `@system-service` does not include `connect`) would make the probe
>    fail with `EPERM` — indistinguishable from a backend outage with no diagnostic.
> 4. Predictable 0.2s sleep interval creates a timing oracle; a malicious local user in rootless mode
>    can race the container for port binding on `127.0.0.1` during the 120s window.
>
> **Mitigation priority (strongly recommended for v1):**
> 1. **Preferred:** ship a **tiny compiled Go wait-helper** in the Podman/quadlet binary (no external
>    deps, immune to SystemCallFilter, provides actual error diagnostics). This is a v1 requirement,
>    not a future optimization — bash /dev/tcp is too fragile across distros.
> 2. If a Go helper is deferred, use **socat** (`socat -u /dev/null TCP:127.0.0.1:<port>,connect-timeout=120`)
>    or **python3** (`python3 -c 'import socket; socket.create_connection(("127.0.0.1",<port>), timeout=120)'`)
>    as the `ExecStartPre` probe — both provide actual error messages and are more widely available.
> 3. If `bash /dev/tcp` is **retained**, it must stay under `@system-service` (no stricter filter) and the
>    connect must be allowed; the generator MUST check `exec.LookPath("socat")` and `exec.LookPath("python3")`
>    at generation time and select the best available probe, falling back to bash only as a last resort.
> 4. Raise the poll timeout (≈120s) so first-activation image pulls do not time out. Document the EPERM
>    failure mode prominently so operators do not mistake a sandbox-blocked readiness probe for a backend
>    outage.

### 7.2 Internal Port Binding Restriction

**CRITICAL**: All internal proxy ports (`127.0.0.1:<internal>`, where `<internal>` is the container
port or the explicit `SocketActivationInternalPort`) **MUST** bind to `127.0.0.1` only.

| Binding | Allowed | Reason |
|---------|---------|--------|
| `127.0.0.1:<internal>` | ✅ Yes | Localhost only, not externally accessible |
| `0.0.0.0:<internal>` | ❌ No | Would expose internal proxy port |
| `[::]:<internal>` | ❌ No | Would expose internal proxy port on IPv6 |
| `[::1]:<internal>` | ❌ No | IPv6 loopback not needed; single stack sufficient |

### 7.3 Socket Activation Security Benefits

- **Reduced attack surface**: Container not running until connection arrives
- **No network exposure**: Internal ports only on localhost
- **systemd-managed lifecycle**: Socket/proxy/container are independently managed. The **container**
  service keeps `Restart=on-failure` (set by the generator for SAP units, see §3.2.1) so it recovers
  from crashes without tearing down the socket; the **proxy** keeps `Restart=no` (§3.2.3) because it is
  socket-driven and must not restart in a tight loop when the backend is down.

---

## 8. IPv6 Handling

### 8.1 Dual-Stack Auto-Generation

On Linux with the default `net.ipv6.bindv6only=0`, a bare `ListenStream=8080` already binds an IPv6
socket that **also accepts IPv4** (dual-stack). Therefore generating *both* `ListenStream=8080` and
`ListenStream=[::]:8080` is redundant (and explicitly generating `0.0.0.0:8080` alongside `[::]:8080`
would actually collide). The rules below avoid double-bind:

| Input | Generated ListenStream Lines | Address family |
|-------|------------------------------|----------------|
| `8080:80` | `ListenStream=8080` | **dual-stack** (IPv4 + IPv6) — bare form auto-binds both under `bindv6only=0` |
| `0.0.0.0:8080:80` | `ListenStream=8080` | IPv4-only source, emitted as bare `ListenStream=8080` which is dual-stack under `bindv6only=0`; do **not** add `0.0.0.0:` nor `[::]:` (would double-bind) |
| `127.0.0.1:8080:80` | `ListenStream=127.0.0.1:8080` | IPv4 only |
| `[::]:8080:80` | `ListenStream=[::]:8080` | **dual-stack** under `bindv6only=0` (also accepts IPv4); **not** IPv6-only unless `BindIPv6Only=yes` is set |
| `[::1]:8080:80` | `ListenStream=[::1]:8080` | IPv6 loopback only |

**Rule:** emit exactly **one** `ListenStream` per port. For "all interfaces" inputs (bare or `0.0.0.0`)
use the bare numeric form `ListenStream=hostPort`, which is dual-stack.

**Dual-stack caveat:** under the default `net.ipv6.bindv6only=0`, **both** `ListenStream=8080` and
`ListenStream=[::]:8080` accept IPv4 *and* IPv6. So `[::]:hostPort` is effectively dual-stack, not
"IPv6-only". To get true IPv6-only you must set `BindIPv6Only=yes` in the `[Socket]` section — otherwise
stating "[::] = IPv6 only" is incorrect. If a deployer wants IPv4-only, use `127.0.0.1:` or `0.0.0.0:`
explicitly. Do **not** generate `ListenStream=[::]:hostPort` together with `ListenStream=hostPort` — that
is a double-bind.

### 8.2 IPv6 Syntax Requirements

- **Brackets required**: `[::]:8080:80` — bare IPv6 without brackets is a parse error
- **Scope IDs not supported**: `[fe80::1%eth0]:8080:80` → rejected at parse time, but **not** by a dedicated `SAP016` check. `net.ParseIP` (called inside `CreatePortBindings`) fails on the `%` scope id first with `cannot parse "fe80::1%eth0" as an IP address`, so the SAP016 string is never emitted (see Appendix C). Treat scope-ID inputs as a hard parse error.
- **Mixed IPv4/IPv6 in same unit**: Allowed, generates appropriate ListenStream lines

### 8.3 Proxy Service IPv6

- Proxy service **always** connects to `127.0.0.1:<internalPort>` (IPv4 loopback)
- IPv6 loopback (`[::1]`) not used for internal proxy connections
- This is intentional: single-stack internal proxy simplifies configuration
- **Limitation (V16):** because the backend address is hardcoded to IPv4 `127.0.0.1`, an IPv6-only
  container backend (one listening solely on `[::1]` with no IPv4 loopback) is **not** reachable by the
  proxy in v1. The container app must listen on IPv4 loopback (or dual-stack) for socket activation to
  work. Document this; dual-stack backend binding is the safe choice.

---

## 9. Template Unit Support (@ Syntax)

### 9.1 Template Unit Naming

| Quadlet File | Template Service | Template Socket | Template Proxy |
|--------------|------------------|-----------------|----------------|
| `web@.container` | `web@.service` | `web@.socket` | `web-proxy@.service` |

> The proxy template is `web-proxy@.service` (prefix `web-proxy`), **not** `web@-proxy.service`. The
> latter would parse as template `web` with instance `-proxy` and never relate to `web@%i` instances.

### 9.2 Instance Unit Naming

| Instance | Service | Socket | Proxy Service |
|----------|---------|--------|---------------|
| `web@1.container` | `web@1.service` | `web@1.socket` | `web-proxy@1.service` |
| `web@prod.container` | `web@prod.service` | `web@prod.socket` | `web-proxy@prod.service` |

### 9.3 Specifier Resolution

Specifiers resolve **per the unit file that contains them** (man `systemd.unit`). For the proxy
template `web-proxy@.service`, `%p` is `web-proxy`, not `web`.

| Specifier | In Service (`web@.service`) | In Socket (`web@.socket`) | In Proxy (`web-proxy@.service`) |
|-----------|------------------------------|----------------------------|----------------------------------|
| `%N` | `web@1` | `web@1` | `web-proxy@1` |
| `%n` | `web@1.service` | `web@1.socket` | `web-proxy@1.service` |
| `%i` | `1` | `1` | `1` |
| `%p` | `web` | `web` | `web-proxy` |

### 9.4 Dependency Specifiers in Templates

```ini
# web@.service — no socket dependencies (see §4.1)
[Service]
ExecStart=/usr/bin/podman run --name systemd-%p_%i ...

# web@.socket
[Socket]
Service=web-proxy@%i.service
ListenStream=8080

[Install]
DefaultInstance=1

# web-proxy@.service
[Unit]
Requires=web@%i.service
After=web@%i.service
PartOf=web@%i.service

[Service]
ExecStart=<proxyd-path> 127.0.0.1:<internal>

# NOTE: the proxy template has NO [Install] WantedBy (see §3.2.3 / H7);
# it is activated exclusively via the socket's Service= (default-instanced into web-proxy@1.service),
# NOT enabled by enableServiceFile (which only symlinks WantedBy units).
```

### 9.5 Template Restrictions

- **Port numbers must be static** — `%i` or other specifiers in port specifications are **rejected at parse time**
- Error: `SocketActivationPort does not support systemd specifiers in port numbers`
- **Consequence:** every instance of a template binds the **same** host/container port. Per-instance
  ports are not supported in v1 (see §0/§16); use separate units or the multi-port follow-up design.
- **Shared host port across instances (H12):** because the `hostPort` is identical for every instance,
  only **one** instance can bind the socket's `ListenStream` at a time. With `DefaultInstance=1` enabled
  (§9.6), instantiating any *other* instance (e.g. `web@5`) collides on the same host port at runtime
  ("Address already in use", §6.3).
  - **Softened rule (K7, changed from v1.2):** do **not** hard-reject all templates. A template with
    `SocketActivationPort` is fully valid for the **single default instance** (`DefaultInstance=1`), since
    that one instance occupies the host port. The generator SHOULD:
     1. Generate the template units with `DefaultInstance=1` (§9.6) so only `web@1.socket` is enabled
        (symlinked); and
    2. Emit a **generation warning** (`SAP018 warning`) with a distinct message from the error case:
       `SocketActivationPort on a template unit with DefaultInstance=1: only the default instance
       is supported; other instances will collide on the same host port`. Do **not** fail generation
       for a single-instance template.
  - A *hard error* is reserved for the case where `DefaultInstance=` is **absent** and the template would
    be enabled as multiple instances by the caller's `[Install]` — there the collision is unavoidable, so
    reject with `SAP018 error` (`SocketActivationPort on a template unit requires per-instance ports,
    which are not supported in v1 (use DefaultInstance=1 to enable a single instance, or a non-template
    unit)`). SAP templates with `DefaultInstance=1` become supported now; multi-instance
    parameterized templates remain a follow-up.

### 9.6 Template Enablement (`DefaultInstance`, H11)

Quadlet's `enableServiceFile` (see §A.4) **ignores** the `[Install]` group of a non-instantiated
template unless `DefaultInstance=` is present. Therefore the generated `web@.socket` and
`web-proxy@.service` templates **must** include:

```ini
[Install]
DefaultInstance=1
```

in their `[Install]` sections.

> **Important correction (V3):** `DefaultInstance=1` rewrites/enables the **`.socket`** template into
> `web@1.socket` (which has `WantedBy=sockets.target`, so it is symlinked into `sockets.target.wants/`
> and activates at boot/login). It does **NOT** by itself symlink the **proxy** template: `enableServiceFile`
> (`main.go:252-264`) only creates wants/ symlinks for units that carry `WantedBy` / `RequiredBy` /
> `UpheldBy` / `Alias`, and the proxy deliberately has **no** `[Install]` `WantedBy` (see §3.2.3 / H7).
> The proxy `web-proxy@1.service` is pulled in **only** through the socket's `Service=web-proxy@1.service`
> activation — never directly enabled. **Do not** state in the spec that the proxy is "enabled as
> `web-proxy@1.service`"; it is *activated* by the socket, not *enabled* by `enableServiceFile`. Without
> `DefaultInstance=` the `.socket` template is generated but never symlinked into `sockets.target`, so
> the whole chain never activates. (The main `web@.service` template already requires the same
> `DefaultInstance=` treatment from the caller's `[Install]`.)

---

## 10. Validation Rules

### 10.1 Key & Syntax Validation

This section covers what is checked against the **raw quadlet key** (before port parsing). The
SAP-specific **port format** rejections (`SAP001`/`SAP002`/`SAP003`/`SAP004`/`SAP005`/`SAP006`) run
**after** `CreatePortBindings` returns (§10.2), because that parser itself accepts ranges, `/udp`, empty
host ports, and container-side ranges — it cannot enforce the SAP restrictions on its own.

| Rule | Check | Error Message |
|------|-------|---------------|
| Key in supported groups | `[Container]`, `[Pod]` only; `[Kube]` is **not** in `SupportedKeys`, so it is rejected by the generic unknown-key path (`unsupported key 'SocketActivationPort' in group 'Kube'`) **before** any SAP logic — `SAP013` is the *documented* name for this case, but the actual emitted string is the generic unknown-key error. Do not rely on the literal `SAP013` text being produced. | generic `unsupported key ...` |
| Repeated key → SAP014 | Quadlet allows a key to appear multiple times. The generator MUST read all entries with `LookupAll(...)` for `SocketActivationPort`; if more than one is present, emit `SAP014`. Using `Lookup` (last-wins) would silently hide the second entry and must NOT be used. | `SocketActivationPort supports at most one entry in this version (SAP014)` |
| Orphan `SocketActivationInternalPort` | If `SocketActivationInternalPort` is set but there is no `SocketActivationPort`, it is **ignored** (no error) — it is only meaningful together with SAP. Do not fail generation on the orphan key. | — (ignored) |
| Syntax matches PublishPort | Reuse `CreatePortBindings` parser; raw parser errors (e.g. `invalid port format`, `port numbers must be between 1 and 65535 (inclusive), got %d`) surface **verbatim** — they are NOT the SAP-coded messages. | parser error from underlying function |
| Host port not empty | `hostPort` field present and non-zero (post-parse, §10.2) | `SocketActivationPort requires explicit host port (empty host port not allowed)` |
| No port ranges | Single port only (post-parse, §10.2) | `SocketActivationPort does not support port ranges` |
| Protocol TCP only | `protocol` field empty or "tcp" (post-parse, §10.2); normalize case-insensitively so `/TCP` is accepted | `SocketActivationPort only supports TCP protocol` |
| Port range 1-65535 | Both host and container ports are validated by `CreatePortBindings`; the SAP-coded message is only used for the explicit SAP checks. | `port numbers must be between 1 and 65535` |
| Container port present | Non-empty container port (post-parse, §10.2) | `must provide a non-empty container port to publish` |
| No systemd specifiers in ports | Scan for `%` in port spec (post-parse, §10.2) | `SocketActivationPort does not support systemd specifiers in port numbers` |

> **LookupAll vs Lookup — implementation trap:** the existing code in `ConvertContainer` uses
> `unit.Lookup(group, KeyImage)` for most single-value keys. An LLM will naturally apply this pattern
> to `SocketActivationPort`. That is **wrong** — it must use `LookupAll` to detect duplicates and
> emit `SAP014`:
>
> ```go
> // WRONG (LLM will write this — matches the Lookup pattern used everywhere):
> sapRaw, hasSAP := container.Lookup(ContainerGroup, KeySocketActivationPort)
>
> // RIGHT (duplicate detection + SAP014):
> sapEntries := container.LookupAll(ContainerGroup, KeySocketActivationPort)
> if len(sapEntries) > 1 {
>     return nil, warnings, fmt.Errorf("SocketActivationPort supports at most one entry (SAP014)"), nil
> }
> sapRaw := sapEntries[0]
> ```
>
> `SocketActivationInternalPort` is single-valued — use plain `Lookup` for it.

> **Reclassification note:** `SAP001`/`SAP002`/`SAP003`/`SAP004`/`SAP005`/`SAP006` are **post-parse**
> checks in §10.2, not parser checks. The table above lists them here only as the logical validation
> point. (`SAP004` = port out of range 1–65535, surfaced either by `CreatePortBindings` or the explicit
> SAP check.)

### 10.2 Generation-Time Validation

| Rule | Check | Error Message |
|------|-------|---------------|
| Internal port uniqueness | Across SAP internal, PP host ports, PP container ports, EHP, and the SAP **hostPort** (self-loop guard) in unit | `internal port %d already in use by another port mapping` |
| Self-loop guard | computed `internalPort == hostPort` → reject (`SAP015`) | `SocketActivationPort host port %d equals internal port %d, which would create a proxy self-loop` |
| Internal port ≤ 65535 + exhaustion | Allocation loop must run on a plain `int` (NOT `uint16`); a `uint16` candidate that increments past 65535 would **wrap to 0** and silently return an invalid port. Bound the loop with `candidate > 65535` on an `int`, then raise the exhaustion error. | `no available internal port found for socket activation (exhausted port range)` |
| systemd-socket-proxyd exists | Check PATH at generation via `exec.LookPath`; if absent, emit **`SAP010` as a warning** (the 2nd return value, `warnings`), **NOT** as the fatal error (3rd return) — otherwise generation aborts and no units are written at all (main.go:~565). Use `warnings = errors.Join(warnings, fmt.Errorf("systemd-socket-proxyd not found in PATH"))` to accumulate. | `systemd-socket-proxyd not found in PATH` (**warning**, `SAP010` — see below) |
| Template port specifiers | No `%` in port specs | `SocketActivationPort does not support systemd specifiers in port numbers` |
| Post-parse rejections | After `CreatePortBindings`, verify: non-empty host port, no range, protocol is tcp, container port present, no container-side range | Reuse `SAP001`–`SAP006` error messages from Appendix C |
| Unknown `SocketActivationPortOptions` | Each option must be a recognized proxyd flag (`--timeout=`, `--buffer-size=`, `--verbose`) | `SocketActivationPortOptions contains unknown option %q` (`SAP017`) |
| Network mode | `Network=none`, or a resolved network whose published port is unreachable on the proxy's loopback (e.g. `container:<other>`) → `SAP012`; rootless `bridge`/`pasta` are reachable and do NOT trigger `SAP012`; `Network=host` → `SAP011` (warning + skip, no injected publish) | see `SAP011`/`SAP012` |
| SAP hostPort vs PublishPort hostPort | SAP `hostPort` equals a `PublishPort` host port in the same unit → `SAP019` (the host port is owned by the `.socket`, §6.2) | `SocketActivationPort host port %d conflicts with PublishPort host port` |

> **Post-parse validation is mandatory:** `CreatePortBindings` itself accepts port ranges, `/udp`,
> `/sctp`, and empty host ports (treated as random). The SAP-specific rejections (`SAP001`/`SAP002`/
> `SAP003`/`SAP005`/`SAP006`) must therefore be implemented as explicit checks **after** calling the
> parser, not relied upon from it. Note the parser's own messages (`invalid port format`,
> `port numbers must be between 1 and 65535 (inclusive), got %d`) differ from the SAP-coded text — wrap
> or map them if the tests assert the SAP codes.

### 10.3 Runtime Validation (systemd)

| Condition | Behavior |
|-----------|----------|
| Host port in use | Socket unit fails to start: "Address already in use" |
| Proxy service crashes | The proxy has `Restart=no` (set in §3.2.3, B2). It is not restarted by systemd; the socket reactivates it on the next inbound connection. A crash mid-proxy is surfaced via the socket's `MaxConnections`/accept path. |
| Container fails to start | systemd retries per the container service `Restart=on-failure` policy (explicitly set by the generator for SAP units — Quadlet does NOT set a default `Restart` for containers, so this must be added). The proxy `After=`/`PartOf=` the container, so the proxy waits for the container and stops with the container (B1/B3). |
| Internal (loopback) port in use | Proxy `ExecStartPre` wait-loop (§3.2.3, B1) fails after retries → proxy unit fails; the socket reactivates it on the next connection. |
| Backend down when connection arrives | Proxy `ExecStartPre` waits for the loopback port; if the container never listens, the proxy fails and the socket reactivates it later. No restart-storm (Restart=no). **Note (F9):** with `Restart=no`, every new inbound connection triggers a fresh activation attempt; if the backend is *permanently* down this is a per-connection probe (not a tight loop, but unbounded cost). The socket unit's own activation rate should be bounded (e.g. `MaxConnections`/`StartupRatePerSec` style limits) and/or the proxy given a `Restart=` with `RestartSec=` backoff if operational needs require auto-retry. Document this trade-off for operators. |

---

## 11. Integration with Existing Features

### 11.1 PublishPort Coexistence

| Feature | Interaction |
|---------|-------------|
| Both in same unit | ✅ Supported |
| Internal port allocation | SAP skips ports used by PP host ports |
| PP host port = SAP internal port | SAP allocates next free port |
| PP with auto port (`:80`) | PP gets random port; SAP unaffected |

**Example:**
```ini
[Container]
Image=myapp
PublishPort=8080:80          # Host 8080 → Container 80
SocketActivationPort=8443:443  # Host 8443 → Internal 443 (container port) → Container 443
```
Generated:
- `web.service`: `--publish 8080:80 --publish 127.0.0.1:443:443`
- `web.socket`: `ListenStream=8443` (dual-stack; `Service=web-proxy.service`)
- `web-proxy.service`: `ExecStart=... 127.0.0.1:443` (`Requires=`/`After=web.service`)

> **`SocketActivationPort` is NOT `PublishPort` (key UX point):** a port listed in
> `SocketActivationPort` is **socket-activated**, not published the way `PublishPort` publishes it. Do
> **not** also set `PublishPort` for the same host port — the activation supersedes it (the host port is
> owned by the `.socket`, and the container only publishes the internal `127.0.0.1:<internal>` loopback
> port). They may coexist for *different* ports (as in the example above: 8080 published normally,
> 8443 socket-activated). The man-page docs must state this explicitly.

### 11.2 ExposeHostPort Coexistence

| Feature | Interaction |
|---------|-------------|
| EHP ports | Added to `usedPorts` set for collision detection |
| EHP = `--expose` only | No host binding; container-internal only |
| SAP internal ≠ EHP port | Must be unique |

### 11.3 Network Mode Compatibility

| Network Mode | Supported | Notes |
|--------------|-----------|-------|
| `bridge` (default) | ✅ root and rootless | Works in **root** mode because the proxy runs in the host namespace and the container publishes `127.0.0.1:<internal>:<container>`, which Podman DNATs into the container. In **rootless** mode `bridge` **also works**: libpod sets up rootless port-forwarding (via `pasta`/`rootlessport`) onto the user loopback, so the proxy's user-netns can reach `127.0.0.1:<internal>` (libpod/networking_linux.go:~63-76). `SAP012` is therefore **not** emitted for a rootless `bridge`. `pasta` is of course also fine in rootless. |
| `host` | ⚠️ Warning, skip socket/proxy | With `Network=host` the container binds the port directly in the host namespace, so socket activation adds nothing. The generator emits a **warning** (`SAP011`, see Appendix C) and does **not** generate the `.socket`/`-proxy.service`. **Crucially**, it also must **not** inject the `127.0.0.1:<internal>:<container>` `--publish` into the container service (with `Network=host`, libpod *ignores* — does not reject — port mappings, so injecting them would be silently dropped and the internal port would never be published; the SAP block is fully skipped for the unit). |
| `none` | ❌ Unsupported (error) | With `Network=none` the container has only loopback and Podman cannot publish `127.0.0.1:<internal>` into it; the proxy (in the host/user netns) cannot reach the container's loopback. Generating socket activation here is invalid → error. |
| `container:<name>` | ❌ Unsupported (error, `SAP012`) | The proxy runs in the host/user netns and cannot reach `127.0.0.1:<internal>` inside the foreign container's netns — the published port is on the *target* container's loopback, invisible to the proxy (§11.3/K.3/SAP012). Note: `Network=container:<name>` is rejected at generation time. |
| Custom network | ✅ Yes | Via `Network=` key |
| `pasta` (rootless default) | ✅ Yes | pasta publishes the container port onto the host/user loopback, so the proxy in the user netns can reach `127.0.0.1:<internal>`. This is the working rootless path — and the **only** rootless network stack (slirp4netns was removed from Podman; see `ParseNetworkFlag`). |
| `slirp4netns` | ❌ Removed | `slirp4netns` is no longer a supported network mode in Podman (`ParseNetworkFlag` hard-errors on it). Listed only for historical context; it cannot be selected. |

> **Rootless loopback reachability (H1):** the proxy process runs in the **user** netns, not inside the
> container. For the proxy to reach `127.0.0.1:<internal>`, the container's published port must appear on
> the *same* loopback the proxy uses. `pasta` (rootless default) and `bridge` (both root and rootless,
> via libpod rootless port-forwarding) satisfy this. A rootless (or root) unit whose network resolves to
> a stack the proxy cannot reach — `Network=none`, or a `Network=` that publishes the port onto a
> loopback the proxy's netns cannot see (e.g. `container:<other>`) — is rejected with `SAP012` (see
> below). Bridge is **no longer** rejected in rootless (this was corrected: libpod forwards the published
> port onto the user loopback).

### 11.4 Pod and Kube Groups

| Group | Support | Notes |
|-------|---------|-------|
| `[Container]` | ✅ Full | Primary target |
| `[Pod]` | ✅ Full | Applies to the pod; the proxy `Requires`/`After`/`PartOf` the **pod** service (not a single container), and the activated port is published on the pod's infra container (`pod create --publish 127.0.0.1:<internal>:<container>`). |
| `[Kube]` | 🔜 Planned follow-up (not in v1) | `kube play` builds its own service/endpoint units and does not expose a hook to inject a per-container proxy+socket pair. The **v1 exclusion is a limitation, not a design verdict** — a follow-up change should add SAP for `[Kube]` by teaching `kube play` to emit the socket+proxy sidecars (owner: TBD). Until then v1 restricts `SocketActivationPort` to `[Container]` and `[Pod]`; the key is rejected in `[Kube]` via the **generic unknown-key path** (`unsupported key 'SocketActivationPort' in group 'Kube'`) before any SAP logic runs — see §10.1 / TN-14 / Appendix C (`SAP013` is the documented name but is never emitted verbatim). |

> **Pod activation note:** for a `[Pod]` unit the proxy must depend on the **pod** service
> (`db-pod.service`), not on an individual container. The activated host port is published on the pod's
> infra container. A template pod (`db@.pod`) follows the same `%i` resolution as §9.

### 11.5 Other Quadlet Features

| Feature | Compatible | Notes |
|---------|------------|-------|
| Volumes | ✅ | Independent |
| Secrets | ✅ | Independent |
| Health checks | ✅ | Runs after container starts |
| Auto-update | ✅ | Works with socket-activated containers |
| Resource limits | ✅ | Applied to service unit |
| Drop-ins | ✅ | systemd native |

---

## 12. Rootless/Root Compatibility

### 12.1 Rootless Mode (User systemd)

| Aspect | Behavior |
|--------|----------|
| Unit directories | `~/.config/containers/systemd/`, `$XDG_RUNTIME_DIR/containers/systemd/` |
| systemd instance | `systemctl --user` |
| systemd-socket-proxyd | Available in user PATH (typically `/usr/lib/systemd/`) |
| Port binding | Unprivileged ports (≥1024) by default. The *listening host port* is bound by the **systemd socket unit**, not by Podman/rootless. **Correction (K4):** simply adding `AmbientCapabilities=CAP_NET_BIND_SERVICE` to the `.socket` unit does **not** grant a privileged bind to the user manager — the user `systemd` instance performs the `bind()` itself, without the unit's ambient caps, so a privileged host port (e.g. 80) still fails under a normal user instance. The working levers are: (a) use an **unprivileged** host port (≥1024), or (b) raise the host sysctl `net.ipv4.ip_unprivileged_port_start=80` (root on the host), or (c) run under root systemd. Do **not** document `AmbientCapabilities` on the socket as a working rootless privileged-port mechanism. |
| Network requirement | In **rootless** mode the resolved network is typically `pasta` (the rootless default), but **`bridge` also works** because libpod configures rootless port-forwarding onto the user loopback (libpod/networking_linux.go:~63-76), so the proxy can reach `127.0.0.1:<internal>`. `SAP012` is **not** emitted for a rootless `bridge` or `pasta`. In **root** mode, `bridge` works (and `pasta` also works). The only rootless/root cases rejected at generation with `SAP012` are networks where the published `127.0.0.1:<internal>` is **unreachable by the proxy** — specifically `Network=none`, or a `Network=` value that maps to a stack with no loopback-reachable published port (e.g. `container:<other>` whose netns the proxy cannot see). `slirp4netns` is no longer selectable in Podman at all. |

> **Detection limitation (K6):** `SAP012` is emitted for explicitly unreachable networks
> (`Network=none`, or a `Network=` value that maps to a stack the proxy cannot reach, e.g.
> `container:<other>`). It can **not** be detected at generation for (a) `PodmanArgs=--network=...`
> overrides that the generator does not parse, or (b) any case where the runtime loopback-forwarding
> fails for an environment-specific reason. For those cases the misconfiguration surfaces at runtime as
> a loopback connect failure in the `ExecStartPre` poll-loop (§3.2.3) — still a clear failure, but not a
> generation error. Document this as a known gap rather than implying full detection. Note: rootless
> `bridge` and `pasta` are **reachable** and must NOT trigger `SAP012`.
| Generated units | Installed in user systemd directories |

### 12.2 Root Mode (System systemd)

| Aspect | Behavior |
|--------|----------|
| Unit directories | `/etc/containers/systemd/`, `/usr/share/containers/systemd/`, `/run/containers/systemd/` |
| systemd instance | `systemctl` (system) |
| systemd-socket-proxyd | Available in system PATH |
| Port binding | All ports (1-65535) |
| Generated units | Installed in system systemd directories |

### 12.3 Shared Requirements

- **systemd ≥ 240** (for `systemd-socket-proxyd` and socket activation features)
- **Podman ≥ 5.0** (quadlet generator version)
- **cgroup v2** (required for quadlet)

### 12.4 SELinux Considerations (H3)

On systems with SELinux in **Enforcing** mode (RHEL, Fedora, CentOS Stream), socket activation needs
extra policy so the proxy and container can use the loopback/internal port:

- **Labeled ports — usually NOT needed:** the internal `127.0.0.1:<internal>` port is **loopback**,
  and loopback ports normally require no `semanage port` label. The *host* `ListenStream` port is bound
  by systemd in the init context; for a typical port this also needs no extra label. Only if a deployer
  deliberately uses a non-reserved port in a context that SELinux restricts would `semanage port -a -t
  <type> -p tcp <port>` be relevant — this is the exception, not the rule. Do **not** blanket-advise
  `semanage port` for the loopback internal port.
- **`systemd-socket-proxyd` context:** the proxy runs under the `systemd_socket_proxyd_t` domain (or
  equivalent). If a custom policy blocks `name_connect`/`name_bind` to the internal port, the proxy
  fails at runtime with AVC denials. Capture and triage AVCs in Enforcing-mode CI (see §14.3); do not
  attempt to relax container labels to fix it.
- **Containers with `container_t`:** the standard container policy already permits connecting to
  loopback, so the proxy→container hop over `127.0.0.1:<internal>` typically works without changes.
- **Testing:** any SELinux-enabled CI must run the socket-activation BATS (§14.3) in Enforcing mode and
  capture AVCs, since the generator cannot detect SELinux at generation time.

---

## 13. Systemd Version Requirements

| Feature | Minimum systemd Version | Rationale |
|---------|------------------------|-----------|
| `systemd-socket-proxyd` | 240 | Introduced in systemd 240 |
| `ListenStream=[::]:port` | 232 | IPv6 socket support |
| `BindsTo=` | 209 | Standard dependency |
| `RestrictAddressFamilies=` | 231 | Security hardening |
| `SystemCallFilter=@system-service` | 232 | Security hardening |
| Template units with `%i` | 209 | Standard specifier |

**Recommended minimum**: **systemd 240** (RHEL 9, Fedora 34, Debian 12, Ubuntu 22.04+)

---

## 14. Test Scenarios

### 14.1 Positive Test Cases (Should Succeed)

| ID | Description | Quadlet Content | Expected Generated Units |
|----|-------------|-----------------|-------------------------|
| **TC-01** | Basic single port | `SocketActivationPort=8080:80` | `.service`, `.socket`, `-proxy.service` |
| **TC-03** | Explicit IPv4 | `SocketActivationPort=127.0.0.1:8080:80` | Socket binds 127.0.0.1 only |
| **TC-04** | Explicit IPv6 loopback | `SocketActivationPort=[::1]:8080:80` | Socket binds [::1] only |
| **TC-05** | All IPv6 interfaces | `SocketActivationPort=[::]:8080:80` | Socket binds [::]:8080 |
| **TC-06** | With options | `SocketActivationPort=8080:80`<br>`SocketActivationPortOptions=--timeout=30s` | Proxy ExecStart includes options |
| **TC-07** | Explicit internal port | `SocketActivationPort=8080:80`<br>`SocketActivationInternalPort=18080` | Container published on 127.0.0.1:18080 |
| **TC-08** | Template unit (single instance) | `web@.container` with `SocketActivationPort=8080:80` and `[Install] DefaultInstance=1` | Template units with `%i` specifiers; only instance `1` enabled (see §9.5, K7) |
| **TC-09** | Template instance | `web@1.container` | Instance units `web@1`, `web@1.socket`, `web-proxy@1` |
| **TC-10** | Coexist with PublishPort | `PublishPort=8080:80`<br>`SocketActivationPort=8443:443` | Both work, no port conflict |
| **TC-11** | Rootless mode | Any valid quadlet | Units in user systemd dir |
| **TC-12** | Root mode | Any valid quadlet | Units in system systemd dir |
| **TC-13** | Pod group | `[Pod]` with `SocketActivationPort` | Pod service + socket + proxy |
| **TC-15** | Internal port = container port | `SocketActivationPort=8080:80` | Internal port = 80 (`--publish 127.0.0.1:80:80`) |
| **TC-16** | Collision avoidance | `PublishPort=80:8080`<br>`SocketActivationPort=8080:80` | internal 80 collides with PP container 80 → search → 81 |

### 14.2 Negative Test Cases (Should Fail with Clear Errors)

| ID | Input | Expected Error |
|----|-------|----------------|
| **TN-01** | `SocketActivationPort=:8080:80` | `SocketActivationPort requires explicit host port (empty host port not allowed)` |
| **TN-02** | `SocketActivationPort=8080-8090:80` | `SocketActivationPort does not support port ranges` |
| **TN-03** | `SocketActivationPort=8080:80/udp` | `SocketActivationPort only supports TCP protocol` |
| **TN-04** | `SocketActivationPort=0:80` | `port numbers must be between 1 and 65535` |
| **TN-05** | `SocketActivationPort=65536:80` | `port numbers must be between 1 and 65535` |
| **TN-06** | `SocketActivationPort=8080:` | `must provide a non-empty container port to publish` |
| **TN-07** | `SocketActivationPort=invalid` | `invalid port format` |
| **TN-08** | Internal port collision | `internal port %d already in use by another port mapping` |
| **TN-09** | Port exhaustion | `no available internal port found for socket activation (exhausted port range)` |
| **TN-10** | Specifier in port | `SocketActivationPort does not support systemd specifiers in port numbers` |
| **TN-11** | UDP with IPv6 | `SocketActivationPort only supports TCP protocol` |
| **TN-12** | Multiple `SocketActivationPort` entries (v1 out of scope) | `SocketActivationPort=8080:80` + `SocketActivationPort=8443:443` | `SocketActivationPort supports at most one entry in this version (SAP014)` |
| **TN-13** | `internal == hostPort` self-loop | `SocketActivationPort=8080:8080` (or `8080:80` + `SocketActivationInternalPort=8080`) | `SAP015` proxy self-loop error |
| **TN-14** | `[Kube]` with SAP | `[Kube]` unit with `SocketActivationPort=...` | generation error — **assert the generic `unsupported key 'SocketActivationPort' in group 'Kube'` substring, NOT the literal `SAP013`** (per §10.1: `[Kube]` is not in `SupportedKeys`, so the generic unknown-key path fires before any SAP logic) |
| **TN-15** | rootless + `Network=none` | `Network=none` + SAP | `SAP012` unsupported |
| **TN-16** | IPv6 scope ID | `SocketActivationPort=[fe80::1%eth0]:8080:80` | parse error `cannot parse "fe80::1%eth0" as an IP address` (the dedicated `SAP016` string is unreachable — `net.ParseIP` fails first; see Appendix C) |
| **TN-17** | Template + SAP (per-instance ports unsupported) | `web@.container` with `SocketActivationPort=8080:80` | `SAP018` template rejection |
| **TN-18** | Unknown `SocketActivationPortOptions` | `SocketActivationPortOptions=--bogus` | `SAP017` unknown option |
| **TN-19** | `Network=host` + SAP | `Network=host` + `SocketActivationPort=8080:80` | `SAP011` warning, skip socket/proxy, no injected publish |
| **TN-20** | proxyd absent in PATH | generation in minimal env | `SAP010` **warning** (not error); units still generated |
| **TN-21** | explicit internal collides with PP host | `PublishPort=8080:80` + `SocketActivationInternalPort=8080` | `SAP008` internal port in use |

### 14.3 Integration Tests (BATS)

**File**: `test/system/274-podman-quadlet-socket-activation.bats` (use the next **free** number in
`test/system/`; `270-socket-activation.bats`, `271-tcp-cors-server.bats`, `272-system-connection.bats`,
and `273-remote-spot-check.bats` are **already taken**, so the Quadlet-SAP file must be numbered **274**
— verify with `ls test/system/` before picking).
Rootless/pasta/IPv6/SELinux cases must carry skip guards: `skip_if_remote` for any local-generator
test, `is_selinux_enabled || skip` for the hardening test, and a pasta availability check for the
rootless case.

```bash
@test "quadlet socket activation - basic" {
    # Create quadlet file with SocketActivationPort
    # Run quadlet generator (systemctl daemon-reload)
    # Verify 3 unit files generated
    # Start socket unit
    # Connect to host port
    # Verify container started on-demand
    # Verify connection proxied to container
}

@test "quadlet socket activation - template" {
    # Create template quadlet with DefaultInstance=1
    # Instantiate with @1
    # Verify instance units generated and enabled correctly
    # Test connection to instance
}

@test "quadlet socket activation - with options" {
    # SocketActivationPortOptions=--timeout=5s
    # Verify proxy service includes options
}

@test "quadlet socket activation - coexists with PublishPort" {
    # Both in same unit
    # Verify both work, no port conflicts
}

@test "quadlet socket activation - IPv6" {
    # SocketActivationPort=[::]:8080:80 (dual-stack) and [::1]:8080:80 (loopback)
    # Verify IPv6 socket created
    # Test IPv6 connection
}

@test "quadlet socket activation - rootless (pasta)" {
    # Run as non-root user with Network=pasta
    # Verify user systemd units work and loopback is reachable
}

@test "quadlet socket activation - security hardening" {
    # Verify proxy service has security directives
    # Check PrivateTmp=yes, ProtectSystem=strict, etc. (and NOT MemoryDenyWriteExecute/RestrictNamespaces)
}
```

### 14.4 Unit Tests (Go `testing` package)

Quadlet does **not** use Ginkgo; its tests are plain Go `testing`. Place SAP unit tests next to the
generator under `pkg/systemd/quadlet/` (e.g. `quadlet_socket_activation_test.go`), and generator-API
tests (multi-unit return) under `cmd/quadlet/main_test.go`. All use `testing.T` + table-driven cases.

```go
func TestSocketActivationPort(t *testing.T) {
    cases := []struct {
        name    string
        content string
        wantErr string // "" if success; error code prefix like "SAP001"
    }{
        {"basic", "[Container]\nImage=x\nSocketActivationPort=8080:80\n", ""},
        // The following four use SUBSTRING matching against the parser's own error text, NOT the
        // literal SAP codes (see §10.1: CreatePortBindings emits its own messages). If the generator
        // wraps/maps them to SAP001..SAP006 (recommended in §10.2), assert the SAP code instead. One
        // source of truth — keep §10.1, §14.4, and Appendix C aligned.
        {"empty host", "[Container]\nImage=x\nSocketActivationPort=:8080:80\n", "empty host port"},
        {"range", "[Container]\nImage=x\nSocketActivationPort=8080-8090:80\n", "does not support port ranges"},
        {"udp", "[Container]\nImage=x\nSocketActivationPort=8080:80/udp\n", "only supports TCP"},
        {"self loop default", "[Container]\nImage=x\nSocketActivationPort=8080:8080\n", "SAP015"},
        {"self loop explicit", "[Container]\nImage=x\nSocketActivationPort=8080:80\nSocketActivationInternalPort=8080\n", "SAP015"},
        // [Kube] is rejected by the generic unknown-key path BEFORE any SAP logic — assert the generic
        // substring, NOT the literal "SAP013" (per §10.1).
        {"kube", "[Kube]\nYAML=...\nSocketActivationPort=8080:80\n", "unsupported key"},
        // Template+SAP is rejected at generation (SAP018), see §9.5 for the softened rule (DefaultInstance=1 allowed).
        {"template", "[Container]\nImage=x\nSocketActivationPort=8080:80\n# (template unit name web@.container)\n", "SAP018"},
        {"unknown option", "[Container]\nImage=x\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--bogus\n", "SAP017"},
        {"host net", "[Container]\nImage=x\nNetwork=host\nSocketActivationPort=8080:80\n", "SAP011"},
        {"explicit internal collide", "[Container]\nImage=x\nPublishPort=8080:80\nSocketActivationPort=9090:80\nSocketActivationInternalPort=8080\n", "SAP008"},
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            // parse + generate, assert error code prefix or generated units
        })
    }
}

// TestConvertReturnsMultiUnit (cmd/quadlet/main_test.go): assert that generating a unit with
// SocketActivationPort writes+enables THREE files (service, socket, proxy) via the loop in §A.4.
// This is the critical C1 coverage — without it the .socket is never symlinked.
```

> **Note:** the previous draft suggested Ginkgo and a fixed `test/system/255` filename — both are wrong.
> Quadlet tests use stdlib `testing`; and `255` is already allocated, as are `270-socket-activation.bats`,
> `271-tcp-cors-server.bats`, `272-system-connection.bats`, and `273-remote-spot-check.bats` (different
> suites), so the Quadlet-SAP BATS must use **274** (`274-podman-quadlet-socket-activation.bats`). There
> is currently **no** `Convert*` test harness in the tree; SAP adds the first generator-level tests,
> placed in `pkg/systemd/quadlet/` and `cmd/quadlet/main_test.go`.

---

## 15. Differences from OnlyOffice Pattern

### 15.1 OnlyOffice Pattern (Reference)

OnlyOffice uses a **manual** socket activation setup:
- User creates `.socket` and `.service` files manually
- No proxy service — container binds directly to socket FD
- Container must support `LISTEN_FDS` / systemd socket activation natively
- No port translation — host port = container port

### 15.2 Podman SocketActivationPort Differences

| Aspect | OnlyOffice Pattern | Podman SocketActivationPort |
|--------|-------------------|----------------------------|
| **Proxy** | None (direct FD passing) | `systemd-socket-proxyd` |
| **Port mapping** | Host port = Container port | Host port → Internal (container port, or explicit `SocketActivationInternalPort`) → Container port |
| **Container requirement** | Must support socket activation | Any container (no socket activation support needed) |
| **Setup** | Manual unit files | Automatic from Quadlet |
| **Security** | Container sees socket FD | Container sees localhost:internalPort |
| **Protocol** | Any (TCP/UDP/Unix) | TCP only (proxyd limitation) |
| **Port flexibility** | Fixed mapping | Automatic collision avoidance |

### 15.3 Why This Design?

1. **Universal compatibility**: Works with ANY container image, not just socket-activation-aware ones
2. **Security isolation**: Internal proxy ports never exposed externally
3. **Collision avoidance**: Automatic port allocation prevents conflicts
4. **Quadlet-native**: Declarative, generated from single source file
5. **systemd best practices**: Uses `systemd-socket-proxyd` as designed

---

## 16. Limitations

| Limitation | Reason | Workaround |
|------------|--------|------------|
| **Single port per unit (v1)** | Correct multi-port requires per-port socket+proxy units or a template proxy; see §0 | Use multiple `.container` units, or wait for the multi-port follow-up |
| **TCP only** | `systemd-socket-proxyd` only supports TCP | Use manual socket activation for UDP |
| **No port ranges** | systemd sockets don't support ranges | Specify each port individually |
| **No empty host port** | Socket activation requires fixed known port | Use explicit port; auto-assignment not possible |
| **No UDP** | `systemd-socket-proxyd` TCP only | Manual socket units with `ListenDatagram` |
| **Static ports only** | Template specifiers not allowed in ports | Use fixed ports per instance |
| **No SCTP** | Proxyd limitation | Not supported |
| **No Unix socket activation** | Different mechanism | Use manual `.socket` with `ListenStream=%t/...` |
| **Rootless privileged host ports** | The socket unit (not Podman) binds the host port; user systemd can't grant it | Use unprivileged host ports, or set `net.ipv4.ip_unprivileged_port_start` at the host level |
| **No idle-shutdown (v1)** | Only lazy *start* is implemented; an idle container keeps running after the first connection (see §1.2) | Stop the socket/container manually, or wait for the follow-up idle-shutdown feature |
| **Mandatory proxy (v1)** | The v1 design always inserts a `systemd-socket-proxyd` hop even when `hostPort == containerPort`, where direct fd-passing to the container would be possible (see §15.1 OnlyOffice pattern). Podman already has `pkg/systemd/activation.go` for `LISTEN_PID`/`LISTEN_FDS` handoff. | **v2 roadmap (V8):** hybrid generation — direct fd activation (socket → container, no proxy) when `hostPort == containerPort`; keep the `systemd-socket-proxyd` proxy only for `hostPort != containerPort` (port translation). v1 ships the proxy-only design from §3 for simplicity and reviewability. |

---

## 17. Backwards Compatibility

### 17.1 Guarantees

| Scenario | Compatibility |
|----------|---------------|
| Existing quadlet files without `SocketActivationPort` | ✅ Fully compatible — no changes |
| Existing `PublishPort` usage | ✅ Unchanged behavior |
| Existing `ExposeHostPort` usage | ✅ Unchanged behavior |
| Template units without SAP | ✅ Unchanged |
| Rootless/root mode | ✅ Both work as before |
| Auto-update | ✅ Works with SAP units |
| Drop-in files | ✅ systemd native |

### 17.2 Migration Path

No migration needed. Feature is **opt-in** via new keys.

### 17.3 Deprecation Policy

No deprecations. This is a purely additive feature.

---

## 18. Documentation Updates

### 18.1 Files to Update

| File | Section | Changes |
|------|---------|---------|
| `docs/source/markdown/podman-container.unit.5.md.in` | `[Container]` keys table | Add `SocketActivationPort`, `SocketActivationPortOptions`, and `SocketActivationInternalPort` rows (3 keys, alphabetical order between `ShmSize` and `StartWithPod` — see §18.2) |
| `docs/source/markdown/podman-container.unit.5.md.in` | Key descriptions | Add detailed documentation for both keys |
| `docs/source/markdown/podman-pod.unit.5.md.in` | `[Pod]` keys table | Add all three keys: `SocketActivationPort`, `SocketActivationPortOptions`, `SocketActivationInternalPort` (alphabetical order between `ShmSize` and `SubGIDMap`) |
| `docs/source/markdown/podman-pod.unit.5.md.in` | Key descriptions | Add detailed documentation |
| `docs/source/markdown/podman-systemd.unit.5.md` | Main quadlet doc | Cross-reference new keys |
| `docs/source/markdown/podman-quadlet-basic-usage.7.md` | Examples | Add socket activation example |
| `docs/tutorials/socket_activation.md` | Tutorial | Add Quadlet socket activation section |

### 18.2 Documentation Content Requirements

Each key documentation must include:

1. **Syntax table** with all accepted formats
2. **Rejected formats table** with error messages
3. **Generated unit files** example (all 3 units)
4. **Dependency diagram** or description
5. **Internal port allocation** explanation
6. **Security notes** (127.0.0.1 binding, hardening)
7. **IPv6 handling** (dual-stack auto-generation)
8. **Template unit** usage
9. **Coexistence with PublishPort/ExposeHostPort**
10. **Rootless/root notes**
11. **Limitations** section

> **Doc mechanism correction (K9/V10):** the syntax/rejected-format tables are **not** auto-generated.
> They are produced by a hand-written **`docs/source/markdown/options/socket-activation-port.md`**
> (mirroring `docs/source/markdown/options/publish.md` — note the full path under `docs/source/markdown/`,
> **not** `pkg/systemd/quadlet/options/`) and pulled into the man pages via the `@@option
> quadlet:socket-activation-port` include directive in `podman-container.unit.5.md.in` /
> `podman-pod.unit.5.md.in`. The man-page author must **create** the file under `docs/source/markdown/options/`
> and add the `@@option` include line next to the key's description (e.g. alongside the `PublishPort`
> description, **~line 95** of `podman-container.unit.5.md.in`). **Alphabetical ordering:** the
> `@@option quadlet:socket-activation-port` line must be inserted **after** `@@option quadlet:shm-size`
> (~line 340 of `podman-container.unit.5.md.in`) and **before** `### StartWithPod=` to satisfy the
> `hack/xref-quadlet-docs` validation script that runs in CI (`make validatepr`); do not expect a generated table. The §18.1
> row for "Syntax table" / "Rejected formats table" means "write
> `docs/source/markdown/options/socket-activation-port.md` and reference it through `@@option`", not
> "run a generator".
> **Key checklist:** state explicitly that `SocketActivationPort` is **not** `PublishPort` — the host
> port is owned by the `.socket`, and the container only publishes the internal `127.0.0.1:<internal>`
> loopback port.

### 18.3 Example Quadlet File for Documentation

```ini
# web.container - Socket-activated web server
[Unit]
Description=Socket-activated web container

[Container]
Image=docker.io/library/nginx:alpine
# Socket activation on port 8080 (v1: single port only)
SocketActivationPort=8080:80
# Proxy options
SocketActivationPortOptions=--timeout=30s

[Install]
# Root: multi-user.target. Rootless user units should NOT list default.target here
# (the user manager does not activate it the same way); use multi-user.target only.
WantedBy=multi-user.target
```

### 18.4 Generated Units for Documentation

Show all three generated units with annotations explaining each directive.

---

## Appendix A: Constants and Keys Reference

### A.1 New Constants in `pkg/systemd/quadlet/quadlet.go`

```go
const (
    KeySocketActivationPort        = "SocketActivationPort"
    KeySocketActivationPortOptions = "SocketActivationPortOptions"
    KeySocketActivationInternalPort = "SocketActivationInternalPort"
)
```

### A.2 Supported Keys Maps Updated

```go
// ContainerGroup SupportedKeys
KeySocketActivationPort:         true,
KeySocketActivationPortOptions:  true,
KeySocketActivationInternalPort: true,

// PodGroup SupportedKeys
KeySocketActivationPort:         true,
KeySocketActivationPortOptions:  true,
KeySocketActivationInternalPort: true,
```

> **v1 scope (mandatory registration step, V6):** the three keys **MUST** be added to `ContainerGroup`
> and `PodGroup` `SupportedKeys` (shown above), otherwise Quadlet rejects them via the generic
> unknown-key path before any SAP logic runs (and `SocketActivationPort` would never reach §10.1).
> `SocketActivationPort` is **not** registered in `KubeGroup` SupportedKeys (see §11.4 / `SAP013`), so a
> `[Kube]` unit is rejected by that same generic path — this is the documented `SAP013` case, but the
> emitted string is the generic `unsupported key 'SocketActivationPort' in group 'Kube'` (do not assert
> the literal `SAP013` text; see §10.1 / TN-14).

### A.3 Unit File Suffixes

```go
const (
    SocketUnitSuffix   = ".socket"
    ProxyServiceSuffix = "-proxy.service"
)
```

> **Naming note:** the proxy suffix `-proxy.service` is derived via `getServiceName`-style base
> concatenation (`web` + `-proxy` → `web-proxy.service`). It is a Podman-generated artifact and must
> **not** be registered in `unitsInfoMap` (unlike pod/volume/network units), since it is not a
> Quadlet-managed resource.

### A.4 Generator API Change (Mandatory)

Quadlet's `Convert*` functions already return `(*parser.UnitFile, error, error)` (the second `error`
is the warnings channel), and `cmd/quadlet/main.go` writes **and enables exactly one** unit per input
via `generateServiceFile(service)` + `enableServiceFile(outputPath, service)` (main.go:529-585). The
real blocker is **not** the return type — it is that the write/enable loop only ever touches the single
`main service`. Supporting this feature requires generating **three** units (`.service`, `.socket`,
`-proxy.service`) from one quadlet input and emitting+enabling all of them.

> **Signature consistency (V1 — fixes the earlier broken K.1):** the existing `(svc, warnings, err)`
> ordering is **preserved**; extras are appended **last** as a 4th return value:
> `(*parser.UnitFile, error, error, []*parser.UnitFile)`. The earlier draft inserted extras at position
> 2, which would shift `warnings`/`err` and break every existing call site and test. Keep position 1=svc,
> 2=warnings, 3=err, 4=extras.

> **Call-site precision (K11):** the signature change touches exactly **two** converters —
> `ConvertContainer` (quadlet.go:~601, a free function, **not** a method) and `ConvertPod`
> (quadlet.go:~1606, likewise a free function) — each called once from the `main.go` generation loop
> (main.go:~538 and main.go:~555). It is **not** a tree-wide signature rewrite: kube/image/volume/network
> converters keep `(svc, warnings, err)`. The earlier draft's "5 call sites" count was inaccurate; the
> only call sites that change are the two `Convert*` invocations plus the loop body that emits the
> returned extras.
>
> **Converter inventory (mandatory correction):** the signature change applies to `ConvertContainer` and
> `ConvertPod` **only**. `ConvertArtifact` (quadlet.go:~2406) returns `(svc, error)` — a **2-value**
> signature with **no** warnings channel and **no** extras; it is **out of scope** for this change and
> must NOT be widened. Note also that `ConvertContainer` has ~21 and `ConvertPod` has ~5 internal
> `return` statements (all currently `(svc, nil, nil)`-style 3-value returns); **every one** of those
> ~26 internal returns must be widened to the 4-value form `(svc, warnings, err, nil)` (or the
> appropriate extras slice), not just the two call sites in `main.go`. A partial edit that only changes
> the call sites will not compile.

**Required changes:**

1. **Return the extra units.** Change the affected `Convert*` (container, pod) to return an additional
   slice of generated units **as the last (4th) return value**: `(*parser.UnitFile, error, error, []*parser.UnitFile)`. Keep the
   existing `(service, warnings, err)` ordering convention — `extras` goes **after** `err`, not before
   it. Do **not** rewrite the signatures of the
   unrelated converters (kube/image/volume/network) — and **do not** touch `ConvertArtifact`
   (quadlet.go:~2406), which returns `(svc, error)` (2 values, no warnings/extras) and is out of scope.
   The change is localized to `ConvertContainer` and `ConvertPod`. Every existing `quadlet_test.go` call
   site that reads two `error` returns keeps working unchanged; the new unit tests for SAP read the 4th
   value. Remember all **~26 internal `return` statements** inside `ConvertContainer`/~21 and
   `ConvertPod`/~5 must also be widened to 4 values (see K.11).
2. **Set `Filename` and `Path` on every generated unit.** `parser.NewUnitFile()` leaves `Filename == ""`;
   each socket/proxy unit MUST have `.Filename` (e.g. `web.socket`, `web-proxy.service`) and `.Path`
   (`path.Join(outputPath, filename)`) set explicitly, otherwise `generateServiceFile`/`enableServiceFile`
   misbehave. This is a manual invariant — there is no `NewUnitFile` constructor that sets the name.
 3. **Update the `main.go` generation loop to emit AND enable every returned unit.** The loop must, after
    the existing `service`, iterate the extra slice and call `generateServiceFile(extra)` **and**
    `enableServiceFile(outputPath, extra)` for each. **This is the critical fix (C1):** today only
    `service` is enabled, so the `.socket` never gets a `sockets.target.wants/` symlink and the feature
    silently never activates. The proxy unit has no `WantedBy` (see §3.2.3), so `enableServiceFile` is a
    no-op for it — correct. The `.socket` has `WantedBy=sockets.target` (§3.2.2), so it is symlinked and
    activates at boot/login.
>
> **SILENT FAILURE WARNING:** If the implementer forgets to call `enableServiceFile(outputPath, socketUnit)`,
> the `.socket` is **written to disk** (via `generateServiceFile`) but **never symlinked** into
> `sockets.target.wants/`. The entire socket activation chain is invisible to systemd — no error, no log,
> just a feature that does nothing. The compiled code builds and passes unit tests (which check only the
> generated ini text), but the BATS integration test will fail with `systemctl start <name>.socket`
> returning exit code 1 because the unit is not found. This is the #1 silent failure mode for this
> feature.
4. **Template enablement.** For template inputs, `enableServiceFile` ignores `[Install]` unless
   `DefaultInstance=` is set (main.go:252-258). The generated `web@.socket` / `web-proxy@.service`
   templates carry `DefaultInstance=1` (§9.6), so they are rewritten to `web@1.socket` /
   `web-proxy@1.service` and symlinked correctly.
5. **The new units must NOT be registered in `unitsInfoMap`** (unlike pod/volume/network units), since
   they are synthetic artifacts with no source quadlet file. They are built by direct unit construction
   (filename + `[Unit]`/`[Socket]`/`[Service]` groups), not via the `unitsInfoMap` cross-reference path.

> **Implementation note:** there is currently **no** `Convert*` test harness in the tree. SAP adds the
> first generator-level tests — place them in `pkg/systemd/quadlet/` (assert the generated socket/proxy
> contents) and add a `TestConvertReturnsMultiUnit` in `cmd/quadlet/main_test.go` verifying the loop
> writes+enables all three files. See §14.4.

---

## Appendix B: Data Structures

### B.1 SocketActivationPortSpec

Reuse `go.podman.io/common/libnetwork/types`.`PortMapping` directly rather than defining a
parallel struct — `CreatePortBindings` already returns `[]types.PortMapping`, so the SAP spec is just a
thin wrapper carrying the resolved internal port:

```go
// Reuses containers/common's types.PortMapping (HostIP, HostPort, ContainerPort,
// Range, Protocol, ...). SAP adds:
type SocketActivationPortSpec struct {
    types.PortMapping        // HostIP, HostPort, ContainerPort, Range(0), Protocol("tcp")
    InternalPort      int    // resolved 127.0.0.1:<internal> (see §5.1); use int (NOT uint16) so the
                             // allocation loop can detect exhaustion without wraparound (§10.2)
    RawSpec           string // original string for error messages
}
```

### B.2 SocketActivationConfig

```go
type SocketActivationConfig struct {
    Ports         []SocketActivationPortSpec
    Options       []string  // From SocketActivationPortOptions
    InternalPorts []int     // Calculated during generation
}
```

---

## Appendix K: Implementation Steps (LLM-oneshot checklist)

This appendix gives the precise code-insertion points so an implementer (or LLM) can apply v1.3 without
guessing. All file:line references are against the current tree and must be re-verified at implementation
time.

### K.0 Mandatory Imports (add to `pkg/systemd/quadlet/quadlet.go`)

The SAP implementation adds new dependencies that are NOT currently imported by the quadlet package.
**All** of the following must be added to the import block at `quadlet.go:~12`:

```go
import (
    // ... existing imports ...
    "net"                                          // NEW: net.ParseIP (ListenStream IPv6 bracketing)
    "os/exec"                                      // NEW: exec.LookPath("systemd-socket-proxyd", "bash")
    "strconv"                                      // NEW: port number string conversion

    "go.podman.io/common/libnetwork/types"        // NEW: types.PortMapping (from CreatePortBindings)
    "go.podman.io/podman/v6/pkg/specgenutil"       // NEW: CreatePortBindings (NOT specgenutilexternal)
)
```

> **Critical:** the import for `CreatePortBindings` is `go.podman.io/podman/v6/pkg/specgenutil`
> (package `specgenutil`), **NOT** `go.podman.io/podman/v6/pkg/specgenutilexternal`. The quadlet
> package currently imports only `specgenutilexternal` (for `FindMountType`). These are **different
> packages**. `specgenutil` contains `CreatePortBindings`; `specgenutilexternal` does not.

### K.1 Signature

Keep the existing `(svc, warnings, err)` convention and append the extras slice **after** the existing
return values (so the existing ordering of `service`, `warnings`, `err` is preserved and existing call
sites that read `warnings` as the 2nd return value keep working):

```go
// ConvertContainer / ConvertPod (free functions, NOT methods on a Converter type):
func ConvertContainer(...) (*parser.UnitFile, error, error, []*parser.UnitFile)
//                  service           warnings  err    extras
```

Positional legend (must stay consistent across K.1, §A.4, `cmd/quadlet/main.go`, and every
`pkg/systemd/quadlet/quadlet_test.go` that calls `ConvertContainer`/`ConvertPod`):

| Pos | Name | Type | Meaning |
|-----|------|------|---------|
| 1 | service | `*parser.UnitFile` | the main (container/pod) service |
| 2 | warnings | `error` | non-fatal warnings (existing channel) |
| 3 | err | `error` | fatal error (existing channel) |
| 4 | extras | `[]*parser.UnitFile` | generated `.socket` / `-proxy.service` |

Return `(svc, nil, nil, nil)` for non-SAP units (extras empty). The caller must treat position 2 as
**warnings** and position 3 as the fatal **error**; extras is the last return. Do **not** change the
kube/image/volume/network converter signatures. The 4th-return-position convention is the **single
canonical form** — there is no alternate "position 2" variant. (Earlier drafts floated inserting extras
at position 2, but that would silently shift `warnings`/`err` and break every existing call site and
test; it is withdrawn.)

### K.2 SAP detection & parsing (post-parse, §10.1/§10.2)

1. In `ConvertContainer`/`ConvertPod`, after the existing `PublishPort`/`ExposeHostPort` handling, read
   the SAP key with `LookupAll` (NOT `Lookup`) so a second entry triggers `SAP014` (§10.1).
2. Parse each entry with `CreatePortBindings` (reuse `pkg/specgenutil/util.go`). Then enforce the SAP
   post-parse rejections from Appendix C: empty host port → `SAP001`, range → `SAP002`, non-tcp →
   `SAP003`, out-of-range → `SAP004`, missing container port → `SAP005`, unparseable → `SAP006`,
   `%` specifier → `SAP007`. **Decision (K1):** either wrap `CreatePortBindings`' raw errors into these
   SAP strings here, or assert the raw strings in tests — pick one and keep §10.1/§14.4/C consistent.
3. Build `usedPorts` via `CreatePortBindings(PublishPort)` + `ExposeHostPort` + explicit internal +
   the SAP hostPort (§5.2). Run uniqueness (`SAP008`) and self-loop (`SAP015`) checks.
4. Resolve `SocketActivationInternalPort` (override) or default to container port (§5.1). Allocate
   upward on collision (int loop, `candidate > 65535` → `SAP009`, §5.4).

### K.3 Network resolver (§11.3 / §10.2 / K6)

- If `Network=host` → `SAP011` warning, skip socket/proxy, do **not** inject `--publish` (libpod
  *ignores* host-net port mappings; wording corrected from the old "rejects", K22).
- If `Network=none` → `SAP012` error.
- A `Network=` value that resolves to a stack the proxy cannot reach on the loopback (e.g.
  `container:<other>` with a netns the proxy's netns cannot see) → `SAP012` error. Rootless `bridge` and
  `pasta` are reachable (libpod forwards the published port onto the user loopback) and must **not**
  trigger `SAP012`. `PodmanArgs=--network=` overrides are NOT detectable at generation → documented gap
  (K6).

### K.4 Main service mutation (§3.2.1 / K2)

> **CRITICAL (C1):** do **NOT** emit the `--publish` as a *second* `ExecStart`. The existing
> `ConvertContainer` already builds the `podman run` `ExecStart` via `service.AddCmdline(ServiceGroup,
> "ExecStart", podman.Args, ...)` (quadlet.go:~941) where `podman.Args` is the assembled argv slice.
> Adding a fresh `ExecStart` with `AddCmdline` would create a **second** `ExecStart=` line, and a
> `Type=simple` unit with two `ExecStart=` is rejected by systemd at load time. Instead, **append**
> `--publish 127.0.0.1:<internal>:<containerPort>` to the existing `podman.Args` slice **before** the
> call to `service.AddCmdline(ServiceGroup, "ExecStart", podman.Args, ...)`. This keeps a single
> `ExecStart` with the publish flag folded into the same `podman run` invocation.

- Append `--publish 127.0.0.1:<internal>:<containerPort>` to the **existing** `podman.Args` argv slice
  (the same slice later passed to `service.AddCmdline(ServiceGroup, "ExecStart", podman.Args, ...)` at
  quadlet.go:~941) — do **not** call `AddCmdline` for a new ExecStart.
- Set `Restart=on-failure` **only if** `!service.HasKey(ServiceGroup, "Restart")`.

### K.5 Extra units (§3.2.2 / §3.2.3)

Build two `parser.NewUnitFile()` units (`web.socket`, `web-proxy.service`), set `.Filename` and
`.Path` explicitly, populate groups per §3.2.2/§3.2.3 (note: `Type=simple`, no `ExecStartPost`,
`RestrictAddressFamilies` without `AF_NETLINK`, §7.1). `ExecStart` for the proxy MUST be assembled with
`service.AddCmdline` after `exec.LookPath` of `systemd-socket-proxyd` (never hardcoded). Add
`FileDescriptorName=proxy` as a cosmetic label (K14). Add `StartupRatePerSec=` to the socket (K15/F9).

### K.6 Enable loop (§A.4 / C1)

In `cmd/quadlet/main.go` generation loop, after writing `service`, iterate the returned extras and call
`generateServiceFile(extra)` **and** `enableServiceFile(outputPath, extra)` for each. Template units
carry `DefaultInstance=1` so `enableServiceFile` rewrites+symlinks them (§9.6).

### K.7 Final hardening (§7.1)

After all units built, finalize the proxy's `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` (no
`AF_NETLINK`). Ensure `MemoryDenyWriteExecute`/`RestrictNamespaces` are absent.

### K.8 Tests (§14.4 / §14.3)

- Go unit tests in `pkg/systemd/quadlet/quadlet_socket_activation_test.go` (stdlib `testing`) — assert
  generated unit contents + error substring/code per §14.4.
- `TestConvertReturnsMultiUnit` in `cmd/quadlet/main_test.go` (writes+enables 3 files).
- BATS in `test/system/274-podman-quadlet-socket-activation.bats` (next free after `270`..`273`) with
  `skip_if_remote`, SELinux and pasta guards.

---

## Appendix C: Error Message Catalog

| Error Code | Message | Context |
|------------|---------|---------|
| `SAP001` | `SocketActivationPort requires explicit host port (empty host port not allowed)` | Parse: empty host port |
| `SAP002` | `SocketActivationPort does not support port ranges` | Parse: range detected |
| `SAP003` | `SocketActivationPort only supports TCP protocol` | Parse: UDP/SCTP specified |
| `SAP004` | `port numbers must be between 1 and 65535` | Parse: port out of range |
| `SAP005` | `must provide a non-empty container port to publish` | Parse: missing container port |
| `SAP006` | `invalid port format` | Parse: unparseable |
| `SAP007` | `SocketActivationPort does not support systemd specifiers in port numbers` | Parse: `%` in port spec |
| `SAP008` | `internal port %d already in use by another port mapping` | Generation: collision |
| `SAP009` | `no available internal port found for socket activation (exhausted port range)` | Generation: exhaustion |
| `SAP010` | `systemd-socket-proxyd not found in PATH` | Generation: missing binary (**warning** — not fatal). A hard error here would break `quadlet generate` in minimal/chroot/CI images that build units without running them. The feature simply won't work at runtime if the binary is absent; document the runtime requirement clearly (§12.3). |
| `SAP011` | `SocketActivationPort ignored: Network=host does not support socket activation` | Generation: warning, skip socket/proxy generation (libpod *ignores* host-net port mappings, so the injected `--publish` would be silently dropped — do not inject it) |
| `SAP012` | `SocketActivationPort requires a network whose published port is reachable on the loopback where the proxy runs; Network=none unsupported` | Generation: error when `Network=none`, or when the resolved network publishes the container port onto a loopback the proxy cannot reach (e.g. `container:<other>`). Rootless `bridge` and `pasta` **are** reachable (libpod rootless port-forwarding) and do NOT trigger `SAP012`. |
| `SAP013` | *Not generated in this implementation.* A `[Kube]` unit with `SocketActivationPort` is rejected earlier by `checkForUnknownKeys` (quadlet.go:~562), which fires **before** any SAP logic runs and emits the **generic** `unsupported key 'SocketActivationPort' in group 'Kube'` error. The literal `SAP013` string shown here is the *documented* name for the case, but it is **never emitted verbatim** — assert the generic substring in tests (see §10.1 / TN-14). | Parse: key present in `[Kube]` (handled by generic unknown-key path) |
| `SAP014` | `SocketActivationPort supports at most one entry in this version (multiple ports are planned)` | Generation: more than one `SocketActivationPort` entry (§0) |
| `SAP015` | `SocketActivationPort host port %d equals internal port %d, which would create a proxy self-loop` | Generation: computed `internalPort == hostPort` (covers `hostPort==containerPort` default, and explicit `SocketActivationInternalPort==hostPort`) |
| `SAP016` | *Unreachable in this implementation.* The host IP is parsed by `net.ParseIP` (via `CreatePortBindings`/`pkg/specgenutil/util.go`) **before** any SAP-specific scope-ID check runs; a string containing a `%` scope ID (e.g. `[fe80::1%eth0]`) makes `net.ParseIP` return nil and the parser aborts earlier with `cannot parse "fe80::1%eth0" as an IP address`, so the SAP016 string is **never emitted**. The documented name is retained for completeness; to actually emit `SAP016` a pre-parse scope-ID guard would be required (deferred). Tests should assert the parse failure, not the literal `SAP016` text. | Parse: link-local scope ID in host IP (surfaced earlier by `net.ParseIP`) |
| `SAP017` | `SocketActivationPortOptions contains unknown option %q` | Generation: unrecognized proxyd flag |
| `SAP018` | **Warning:** `SocketActivationPort on a template unit with DefaultInstance=1: only the default instance is supported; other instances will collide on the same host port`<br>**Error:** `SocketActivationPort on a template unit requires per-instance ports, which are not supported in v1 (use DefaultInstance=1 to enable a single instance, or a non-template unit)` | Generation: warning if template has `DefaultInstance=1` (§9.5), fatal error if `DefaultInstance=` is absent |
| `SAP019` | `SocketActivationPort host port %d conflicts with PublishPort host port` | Generation: SAP `hostPort` equals a `PublishPort` host port in the same unit (the host port is owned by the `.socket`, §6.2) |

---

## Appendix D: Revision History

| Version | Date | Author | Changes |
|---------|------|--------|---------|
| 1.0 | 2025 | Podman Contributors | Initial final specification |
| 1.1 | 2025 | Podman Contributors | Post-review revision. Fixed runtime blockers: (B1) proxy `ExecStartPre` readiness wait-loop; (B2) proxy `Restart=no` + container `Restart=on-failure`; (B3) proxy `PartOf=container` to stop leak on container stop; (B4) corrected §1.2/§16 — v1 does lazy start only, no idle-shutdown. High-severity: (H1/H2) rootless loopback requires `pasta`, `slirp4netns`/`Network=none` unsupported, `Network=host` warns+skips; (H3) added SELinux §12.4; (H4) removed `MemoryDenyWriteExecute`/`RestrictNamespaces` from proxy default; (H5) `SAP010` unified to warning; (H6) `ExecStart` via `AddCmdline` with `exec.LookPath`-resolved proxyd; (H7) dropped `WantedBy` from proxy; (H8) `[Kube]` excluded from v1 (`SAP013`); (H9) completed `usedPorts` (host+container+EHP+internal+hostPort); (H10) rejected `hostPort==containerPort` self-loop (`SAP015`); (H11) template `DefaultInstance=1`; (H12) documented shared-hostPort template limit. Medium: §2.1 multiple→single, explicit-internal conflict=error, §3.2.1 default internal=container port, §8.1 corrected dual-stack `[::]:`/`[::1]:` semantics + `BindIPv6Only`, scope-ID `SAP016`, tests moved to stdlib `testing` (not Ginkgo) + BATS number ≥256, `CAP_NET_BIND_SERVICE`, `SocketActivationPortOptions` unknown-option `SAP017`, Appendix B.1 → `types.PortMapping`. |
| 1.2 | 2025 | Podman Contributors | 14-agent verification pass. **Critical:** (C1) rewrote §A.4 — `main.go` MUST call `enableServiceFile` for every generated unit + set `.Filename`/`.Path` (socket was never symlinked before); (C2) container `Restart=on-failure` now explicitly set by generator for SAP units (Quadlet has no container default); (C3) `SAP015` now checks `hostPort == computedInternal` (covers explicit-internal self-loop); (C4) removed obsolete `slirp4netns` (deleted upstream) — pasta is the only rootless stack. **High:** (A.1) proxy readiness via `Type=notify` + `ExecStartPost=systemd-notify --ready` with `ExecStartPre` poll fallback (cap 30s, `exec.LookPath` bash); (A.6/A.11) `SAP012` error for rootless non-pasta/`Network=none`, `SAP011` also suppresses injected publish; (A.4) corrected that `Convert*` already returns `(svc, error, error)`. **Medium:** (A.7) §12.4 no longer advises `semanage` for loopback; (A.9) `[Kube]` relabeled "planned follow-up"; (F1/F3/F4/F7/F9/F12) §10.1 reclassified as key/syntax validation, `SAP013` reachability noted, repeated-key→`SAP014` via `LookupAll`, uint16-wrap guard on exhaustion loop, orphan `SocketActivationInternalPort` ignored; (F1/F9/F10) added `FileDescriptorName=`, per-connection activation backoff note, `AF_NETLINK`; (UX) added "SAP ≠ PublishPort" callout; (templates) `systemd-%p_%i` for template names, `SAP018` rejects template+SAP at generation; (tests) added TN-17..TN-21 + unit cases for B1/B2/B3/SAP017/SAP011/SAP010/multi-unit API; (B.1) reuse `types.PortMapping`. |
| 1.3 | 2025 | Podman Contributors | 16-agent implementation-readiness pass. **Proxy redesign (K3):** dropped incorrect `Type=notify` + `ExecStartPost=systemd-notify --ready` (proxyd sends no READY) → `Type=simple` + `ExecStartPre` poll-loop only; poll timeout raised 30s→120s; removed `AF_NETLINK` from `RestrictAddressFamilies` (§7.1). **Restart guard (K2):** `Restart=on-failure` set only if user did not already set `Restart=`. **Tests (K1):** §14.4 cases assert parser substrings (not literal SAP codes) for empty/range/udp and the generic `unsupported key` for `[Kube]`. **Rootless (K4):** corrected that `AmbientCapabilities=CAP_NET_BIND_SERVICE` on the socket does NOT grant a privileged bind to a user manager; use unprivileged ports or host sysctl. **Footgun (K5):** documented that `stop web.socket` orphans the container (loopback-only, still running). **SAP012 (K6):** documented detection gap for implicit rootless pasta / `PodmanArgs=--network=`. **SAP018 (K7):** softened — template + `DefaultInstance=1` is allowed (warning), hard error only without `DefaultInstance=`. **Docs (K8/K9):** BATS numbered **273+** (not 255/256; `270-socket-activation.bats` taken) with skip guards; §18.2 corrected that tables are hand-written `options/socket-activation-port.md` pulled via `@@option`. **Code refs (K10/K11):** import path `go.podman.io/common/libnetwork/types`; signature change is exactly 2 converters (container+pod) + loop, not "5 call sites". **New (K12):** Appendix K implementation checklist. **Medium/Low:** `FileDescriptorName` documented cosmetic (K14); `SAP019` for SAP host-port vs `PublishPort` host-port conflict (K13); socket `StartupRatePerSec` for per-connection backoff (K15/F9); `SAP011` wording "ignores" not "rejects" host-net mappings (K22); `cross-unit` SAP worse-than-PublishPort note (K24); §2.2 `[::]:` labeled dual-stack; template TC-08/09 now single-instance positive; §18.3 `WantedBy` drops `default.target` for rootless. |
| 1.4 | 2025 | Podman Contributors | Critical + high defect fixes (cross-verified by 4 verification agents). **V1 (signature):** fixed broken K.1 — `Convert*` extras appended as **4th** return (`(svc, warnings, err, extras)`), preserving the existing `(svc, err, warnings)` call sites; §A.4 + K.1 now agree and include a positional legend. **V2:** §7.3 no longer claims the proxy uses `Restart=on-failure` (proxy stays `Restart=no` per §3.2.3; only the container uses `on-failure`). **V3:** §9.4/§9.6 corrected — `DefaultInstance=1` enables the **socket** only; the proxy is *activated* via the socket's `Service=`, never symlinked by `enableServiceFile` (no `WantedBy`). **V4:** TN-14 now asserts the generic `unsupported key` substring, not literal `SAP013` (consistent with §10.1). **V5:** removed all hardcoded `/usr/lib/systemd/systemd-socket-proxyd` / `/usr/bin/bash` paths from examples (§1.3/§3.2.3/§4.3) → `<proxyd-path>`/`<bash-path>` placeholders, consistent with K.5 `exec.LookPath`. **V6:** §A.2/§2.1 strengthened — key registration in `ContainerGroup`/`PodGroup` `SupportedKeys` is a mandatory step; `KubeGroup` intentionally excluded. **V7:** §3.2.1 documents the implicit `Restart=on-failure` injection as a philosophy caveat (future `SocketActivationRestart=` key). **V8:** §16 records mandatory-proxy as a v1 limitation with a v2 hybrid (direct fd) roadmap. **Medium:** V9 BATS numbered **274** (270–273 taken); V10 docs path corrected to `docs/source/markdown/options/socket-activation-port.md` (not `pkg/systemd/quadlet/options/`); V11 §7.1 documents the readiness-gate `SystemCallFilter`/`EPERM` risk + socat/python fallback; V12 §11.3/§12.1 no longer claim rootless `bridge` works (only pasta guaranteed); V14 §3.2.3 clarifies `@system-service` *permits* the probe connect; V15 §8.1 `0.0.0.0` is IPv4-only (bare form is the dual-stack one); V16 §8.3 documents IPv6-only backend unreachable (proxy hardcoded to `127.0.0.1`); V17 §10.1 now lists `SAP004`; V18 §B.1 `InternalPort` changed `uint16`→`int`. |
| 1.5 | 2025 | Podman Contributors | Critical + High defect fixes (verified against tree). **C1:** §K.4 corrected — `--publish` must be appended to the existing `podman.Args` argv slice *before* the single `AddCmdline(ExecStart, ...)` at quadlet.go:~941, not added as a second `ExecStart` (which systemd rejects). **C2:** §3.1 pod naming unified to `db-pod.service` (matches `getServiceName`/`GetPodServiceName`). **C3:** K.1 stale "position 2" variant removed — 4th-return is canonical; §A.4 + K.1 now single-source. **K.1 signature:** `func (c *Converter) ConvertContainer` → free function `func ConvertContainer(...)` (real signature at quadlet.go:~601). **§A.4/K.11:** added `ConvertArtifact` (quadlet.go:~2406, returns `(svc, error)`, 2 values, out of scope) and noted all ~26 internal `return` statements in `ConvertContainer`(~21)+`ConvertPod`(~5) must be widened to 4 values, not just the 2 call sites. **Rootless bridge:** §11.3/§12.1/H1/K.3/SAP012 corrected — rootless `bridge` **works** (libpod rootless port-forwarding, networking_linux.go:~63-76); `SAP012` now only for `Network=none` or a stack the proxy cannot reach locally (e.g. `container:<other>`). **SAP013:** Appendix C + §11.4 — never emitted; `[Kube]` rejected by `checkForUnknownKeys` generic `unsupported key` (assert substring, not literal). **SAP016:** Appendix C + §8.2/TN-16 — unreachable; `net.ParseIP` fails earlier with `cannot parse ... as an IP address`. **SAP010:** §10.2 — must be emitted on the **warnings** channel (2nd return), not the fatal error, else generation aborts. |
| 1.6 | 2026 | Podman Contributors | Critical + High fixes from 12-agent cross-verification pass. **Critical:** (C1) §11.3 table — `Network=container:<name>` changed from ✅ Yes to ❌ Unsupported (error, SAP012) — proxy runs in host/user netns, cannot see the target container's loopback. (C2) §10.2 validation table — added missing `SAP019` row (hostPort vs PublishPort conflict). (C3) §18.2 — corrected man-page insertion line from ~281 to ~95; added alphabetical ordering note (must go after `@@option quadlet:shm-size` to satisfy `xref-quadlet-docs` CI check). (C4) §3.2.3 — documented `exec.LookPath` portability tradeoff (baked absolute paths make generated units non-portable; use `Environment=PATH=...` for portability). **High:** (H1) §7.1 V11 — rewrote readiness-gate risk section with explicit bash `/dev/tcp` failure modes (compile-time feature, shell injection, timing oracle); strongly recommends compiled Go wait-helper or socat/python3 fallback. (H2) §4.1 — documented `PartOf` propagation gaps (SIGKILL/OOM-kill, direct `podman stop`, crash-loop exhaustion). (H3) §7.1 — added `RestrictRealtime=yes`, `PrivateDevices=yes`, `RestrictSUIDSGID=yes` to proxy hardening; rewritten note about `RestrictNamespaces`/`MemoryDenyWriteExecute` — these are now recommended to be tested per-distro and enabled by default in a future revision if CI confirms safety. (H4) §10.1 — added explicit `LookupAll` vs `Lookup` code example (WRONG/RIGHT pair) to prevent LLM from defaulting to last-wins `Lookup`. (H5) §K.0 — new mandatory imports section with exact Go import paths (`specgenutil`, NOT `specgenutilexternal`; `libnetwork/types`; `os/exec`; `net`; `strconv`). (H6) §10.2 — SAP010 cell now shows `errors.Join(warnings, ...)` accumulation pattern. (H7) §3.2.1 — moved C1/K.4 `IMPLEMENTATION HAZARD` warning directly after the service template (was buried 1200+ lines later in K.4). (H8) §A.4 — added `SILENT FAILURE WARNING` emphasis: forgetting `enableServiceFile` on the `.socket` makes the feature silently non-functional (no compile error, no log, just nothing happens). (H9) §9.5 + Appendix C SAP018 — split into distinct warning and error message strings (warning: `...with DefaultInstance=1: only the default instance is supported`; error: `...requires per-instance ports...`). (H10) §18.1 — added `SocketActivationInternalPort` as the third key requiring documentation (was omitted). |

---

**END OF SPECIFICATION**
