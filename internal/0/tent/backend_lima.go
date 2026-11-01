package tent

// LimaBackend drives a Lima VM via `limactl shell <machine> -- sudo
// nerdctl ...`. Picked as a parallel-VM-capable alternative to
// PodmanBackend; see clown#99 and zz-pocs/tent-lima/ for the spike
// that validated this shape.
//
// Construction:
//
//	backend := tent.NewLima(buildcfg.LimactlPath, buildcfg.PodmanMachineName)
//
// (PodmanMachineName is reused as the Lima instance name; the ldflag
// is misnamed in the Lima-backend case and should be renamed to
// TentMachineName when the TOML profile system lands.)
type LimaBackend struct {
	// LimactlPath is the absolute path to the limactl binary,
	// typically baked at build time via buildcfg.LimactlPath.
	LimactlPath string

	// Machine is the Lima instance name (`limactl create --name=...`)
	// to target. Required — Lima has no implicit "default"
	// connection like podman; every shell invocation needs an
	// explicit name. Wired from buildcfg.PodmanMachineName at
	// construction time.
	Machine string
}

// NewLima constructs a LimaBackend. Both arguments are required.
func NewLima(limactlPath, machine string) *LimaBackend {
	return &LimaBackend{
		LimactlPath: limactlPath,
		Machine:     machine,
	}
}

// Binary returns the limactl binary path. Implements Backend.Binary.
func (l *LimaBackend) Binary() string { return l.LimactlPath }

// shellPrefix is the argv prefix for any in-VM command:
//
//	limactl shell <machine> -- sudo
//
// `sudo` is required because Lima's bundled containerd runs as the
// system daemon (config: containerd.system: true in
// nix/hm/tent-backend-lima.nix); user-mode nerdctl can't reach it.
// `--` after the machine name separates limactl flags from the
// in-VM command.
func (l *LimaBackend) shellPrefix() []string {
	return []string{"shell", l.Machine, "--", "sudo"}
}

// ImageExistsArgs returns the argv for
// `limactl shell <name> -- sudo nerdctl image inspect <ref>`. nerdctl
// has no `image exists`; `image inspect` exits non-zero when the
// reference is absent, which is the same contract Podman's
// `image exists` provides.
func (l *LimaBackend) ImageExistsArgs(imageRef string) (string, []string) {
	args := append(l.shellPrefix(), "nerdctl", "image", "inspect", imageRef)
	return l.LimactlPath, args
}

// LoadImageArgs returns the argv for
// `limactl shell <name> -- sudo nerdctl load -i <tarball>`. The
// tarball path is interpreted *inside the VM*; the
// /nix/store:/nix/store:ro mount (declared in the Lima yaml — see
// nix/hm/tent-backend-lima.nix) makes the host's nix-store paths
// reachable inside the VM at the same path.
func (l *LimaBackend) LoadImageArgs(tarball string) (string, []string) {
	args := append(l.shellPrefix(), "nerdctl", "load", "-i", tarball)
	return l.LimactlPath, args
}

// RunArgs returns the argv for `limactl shell <name> -- sudo nerdctl
// run ... <image> <claude> <claudeArgs>`. Delegates the mount/env
// shape to BuildArgs (backend-agnostic; same flags work on nerdctl)
// but suppresses BuildArgs's `--connection` prefix (podman-only) by
// zeroing opts.ConnectionName before the call.
//
// First element of the returned slice is the limactl binary path so
// the caller (tentExecutor.Binary + FormatArgs) can split it cleanly.
func (l *LimaBackend) RunArgs(claudeBinary string, claudeArgs []string, opts Options) []string {
	// Suppress the podman-only `--connection` prefix; nerdctl has no
	// equivalent (it talks to the in-VM containerd directly, with no
	// remote-connection concept). The machine identity already comes
	// from `limactl shell <name>`.
	opts.ConnectionName = ""
	containerArgs := BuildArgs(claudeBinary, claudeArgs, opts)
	args := append(l.shellPrefix(), "nerdctl")
	args = append(args, containerArgs...)
	return append([]string{l.LimactlPath}, args...)
}
