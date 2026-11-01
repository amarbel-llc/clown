package tent

// PodmanBackend drives `podman` directly (against podman-machine on
// darwin, or rootless podman on linux). The status-quo backend; every
// existing tent run uses this.
//
// Construction:
//
//	backend := tent.NewPodman(buildcfg.PodmanPath, buildcfg.PodmanMachineName)
type PodmanBackend struct {
	// Path is the absolute path to the podman binary, typically baked
	// at build time via buildcfg.PodmanPath.
	Path string

	// Connection selects a named podman connection (= machine name on
	// darwin) via `--connection <name>` before the subcommand. Empty
	// means use podman's default connection. Wired from
	// buildcfg.PodmanMachineName at construction time.
	Connection string
}

// NewPodman constructs a PodmanBackend. Path is required; connection
// may be empty to defer to podman's default.
func NewPodman(path, connection string) *PodmanBackend {
	return &PodmanBackend{
		Path:       path,
		Connection: connection,
	}
}

// Binary returns the podman binary path. Implements Backend.Binary.
func (p *PodmanBackend) Binary() string { return p.Path }

// ImageExistsArgs returns the argv for `podman [--connection <name>]
// image exists <ref>`.
func (p *PodmanBackend) ImageExistsArgs(imageRef string) (string, []string) {
	args := append(PodmanConnectionArgs(p.Connection), "image", "exists", imageRef)
	return p.Path, args
}

// LoadImageArgs returns the argv for `podman [--connection <name>]
// load -i <tarball>`.
func (p *PodmanBackend) LoadImageArgs(tarball string) (string, []string) {
	args := append(PodmanConnectionArgs(p.Connection), "load", "-i", tarball)
	return p.Path, args
}

// RunArgs returns the argv for `podman [--connection <name>] run ...
// <image> <claude> <claudeArgs>`. Mirrors what cmd/clown/main.go's
// tentExecutor.FormatArgs used to do directly; the connection flag
// goes ahead of the subcommand (where podman parses it) and the
// remainder is BuildArgs's mount-and-env block.
//
// First element of the returned slice is the podman binary path so
// the caller (tentExecutor.Binary + FormatArgs) can split it cleanly.
func (p *PodmanBackend) RunArgs(claudeBinary string, claudeArgs []string, opts Options) []string {
	// BuildArgs already emits the connection flag from opts.ConnectionName;
	// override it on the local opts copy so the Backend is the single
	// source of truth (not the host's env or accidental Options
	// construction).
	opts.ConnectionName = p.Connection
	args := BuildArgs(claudeBinary, claudeArgs, opts)
	return append([]string{p.Path}, args...)
}
