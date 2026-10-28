# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.8] - 2026-10-29

### Fixed
- A managed floating IP the spec no longer asks for, because its region was removed or its
  `count` lowered, is released instead of staying assigned to the old gateway node: `Retain`
  unassigns it and lists it in the new `status.retainedFloatingIPs`, `Delete` deletes it. BYO
  addresses are never touched.
- The finalizer under `reclaimPolicy: Delete` deletes every managed address labelled for the
  gateway, including those a lowered spec had already retained.

## [0.2.7] - 2026-10-22

### Fixed
- Egress on Cilium 1.20: the policy is rendered in `egressIP` mode, one gateway per floating IP,
  instead of `interface` mode on `egr0`, which black-holed all egress. Pods are spread across
  every floating IP and fail over between them (ADR-0009).
- A gateway node that is not `Ready` leaves the policy at once, in the controller and the agent,
  so a dead gateway stops receiving traffic before its Node object is removed.

### Added
- A datapath end-to-end test on kind with a real Cilium, on every supported minor and the next
  pre-release (ADR-0010).

## [0.2.6] - 2026-08-27

### Changed
- The CRD and the RBAC in the chart are generated from the API types and the kubebuilder
  markers, and the chart keeps one manifest per file.

## [0.2.5] - 2026-08-18

### Fixed
- The agent no longer forces `net.ipv4.conf.all.rp_filter`, which broke pod networking
  (ADR-0004).

## [0.2.4] - 2026-08-18

### Fixed
- The agent runs as root, so `CAP_NET_ADMIN` is in its effective set (ADR-0003).

## [0.2.3] - 2026-08-18

### Fixed
- The agent runs in the `spc_t` SELinux domain, so it can create `egr0` where SELinux is
  enforcing, and `egr0` is a dummy link again (ADR-0005).

## [0.2.2] - 2026-08-18

### Changed
- `egr0` is a veth instead of a dummy link, a workaround for a creation failure later traced to
  SELinux.

## [0.2.1] - 2026-08-14

### Changed
- Managed floating IP names are `<cr>-<location>-<index>`, without a forced `egress-` prefix.

## [0.2.0] - 2026-08-13

### Added
- Floating IPs in several regions (`spec.managed.regions`), with one agent StatefulSet per region.

## [0.1.0] - 2026-08-13

### Added
- `HetznerEgressGateway`: fixed egress source IPs for selected pods from Hetzner Cloud floating
  IPs, managed or bring-your-own, with a Cilium egress gateway or host-routing backend.

[Unreleased]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.8...HEAD
[0.2.8]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.7...v0.2.8
[0.2.7]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.6...v0.2.7
[0.2.6]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.5...v0.2.6
[0.2.5]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.4...v0.2.5
[0.2.4]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.3...v0.2.4
[0.2.3]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/maarlab-rethinking/hcloud-egress-gateway-controller/releases/tag/v0.1.0
