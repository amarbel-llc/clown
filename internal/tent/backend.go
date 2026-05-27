package tent

// Backend abstracts the container runtime that drives `clown --tent`.
// Today there are two implementations:
//
//   - Podman: the status quo. Runs `podman --connection <name> run/
//     image exists/load` directly against a podman-machine on darwin
//     or rootless podman on linux.
//   - Lima:   prefixes every command with `limactl shell <name> --
//     sudo nerdctl ...` to drive Lima's bundled containerd. Picked up
//     post-spike (zz-pocs/tent-lima/) when parallel-VM support or
//     built-in SSH agent forwarding outweighs the podman-machine
//     dev-loop story.
//
// *Why this interface is small:* a future TOML profile-driven backend
// selector (clown's planned --profile work; see
// docs/plans/2026-04-23-profiles-design.md) will replace today's
// build-time `TentBackend` ldflag with runtime selection. Keep the
// interface minimal so that migration is a config-file change plus a
// resolution-sink swap — not a Go-API redesign. If you find yourself
// reaching for a fourth method here, consider whether it belongs in a
// separate concern (lifecycle goes in the home-manager module;
// container introspection goes elsewhere).
type Backend interface {
	// ImageExistsArgs returns the (cmdPath, args) tuple that, when
	// run via exec.Command, exits zero iff the named image is
	// present in the backend's image store. For Podman this is
	// `podman image exists <ref>`; for Lima it's `limactl shell ...
	// -- sudo nerdctl image inspect <ref>` (nerdctl has no `image
	// exists` — `inspect` exits non-zero when absent).
	ImageExistsArgs(imageRef string) (cmdPath string, args []string)

	// LoadImageArgs returns the (cmdPath, args) tuple to load a
	// docker/OCI tarball into the backend's image store. For Podman:
	// `podman load -i <tarball>`. For Lima: `limactl shell ... -- sudo
	// nerdctl load -i <tarball>`. The tarball path is interpreted on
	// the backend side (inside the VM for Lima); for both backends
	// the path must be reachable through the bind-mounted /nix/store.
	LoadImageArgs(tarball string) (cmdPath string, args []string)

	// RunArgs returns the full argv (binary path FIRST, then args)
	// to spawn the tent container running `claudeBinary
	// claudeArgs...`. The result is consumed by tentExecutor.Binary
	// (which takes argv[0]) and tentExecutor.FormatArgs (which takes
	// argv[1:]).
	//
	// Both backends delegate to BuildArgs for the actual mount/env
	// shape — that logic is backend-agnostic and lives in tent.go.
	// What differs is the prefix: Podman emits its binary path +
	// connection flag + "run ..."; Lima emits limactl + "shell <name>
	// -- sudo nerdctl run ...".
	RunArgs(claudeBinary string, claudeArgs []string, opts Options) []string

	// Binary returns the absolute path to the backend's CLI tool
	// (podman or limactl). Consumed by tentExecutor.Binary() and by
	// the preflight error in newTentExecutor.
	Binary() string
}
