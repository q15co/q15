# systemd and Quadlet own the stack lifecycle; podman is read, never written

Status: accepted

The stack is a set of user units rendered as Quadlet files, carrying literal pinned image tags. The
`up`, `down`, `status` and `logs` verbs are thin wrappers over `systemctl --user`, read-only
`podman` and `journalctl`: nothing opens the podman socket, and nothing writes through the Go
bindings. A container runtime is a state machine the operator already has a tool for, and a second
controller inside the CLI would be a second answer to "what is running", which is the failure this
avoids.

## Consequences

- The stack comes back after a reboot without an operator logging in, because the lifecycle belongs
  to systemd rather than to the CLI.
- `podman auto-update` is deliberately not used. The four q15 images always carry the same tag, so
  they move as one set through a single command instead of drifting apart.
- Anything systemd cannot express has to be written as a unit file and reloaded rather than patched
  at runtime. That constrains the verbs, which is why none of them tries to be clever.
- Rendering depends on Quadlet, so the supported podman floor is a real compatibility axis and not a
  documentation detail.

Evidence: [#215](https://github.com/q15co/q15/issues/215),
[#219](https://github.com/q15co/q15/issues/219), [#220](https://github.com/q15co/q15/issues/220).
