package buildcfg

var (
	ClaudeCliPath       string
	CodexCliPath        string
	CircusCliPath       string
	OpencodeCliPath     string
	CrushCliPath        string
	ClownboxCliPath     string
	AgentsFile          string
	DisallowedToolsFile string
	SystemPromptAppendD string
	Version             string
	Commit              string
	ClaudeCodeVersion   string
	ClaudeCodeRev       string
	CodexVersion        string
	CodexRev            string
	LlamaServerPath     string
	// StdioBridgePath is the absolute path to the clown-stdio-bridge
	// binary in its own Nix store output, baked at build time. Used by
	// pluginhost.Desugar to rewrite stdioServers entries into httpServers
	// entries pointing at the bridge. Both clown-plugin-host and clown(1)
	// consume this. clown-stdio-bridge lives in a separate derivation,
	// so resolving from os.Executable() would land in the wrong
	// directory; baking the path is the only correct option for the Nix
	// layout. Empty in dev builds (go build, go run); stdioServers
	// requires the Nix-built artifact.
	StdioBridgePath string
	// DefaultProvider is the provider name used when neither
	// --provider nor CLOWN_PROVIDER is set. Empty falls back to
	// the historical "claude" default in main.go.
	DefaultProvider string
	// DefaultProfile is the profile name used when neither
	// --profile nor CLOWN_PROFILE is set and no explicit provider
	// is given. An unknown name produces the same error as an
	// explicit --profile would. Empty disables the build-time
	// default and restores the picker / hardcoded-provider flow.
	DefaultProfile string
	// PodmanPath is the absolute path to the podman binary, baked
	// at build time. Consumed by the --tent codepath to wrap the
	// provider in a container. Wired in on linux and darwin (on
	// darwin the binary is a thin client that proxies to a
	// podman-machine VM). Empty in dev builds; --tent fails fast
	// with a clear error when empty. See FDR-0007.
	PodmanPath string
	// PodmanMachineName is the podman connection name (= machine
	// name) to target via `--connection <name>` on every podman
	// invocation. Baked at build time by mkCircus / mkClownGo so a
	// downstream consumer (notably packages.dev, which targets the
	// local dev-loop machine) can bypass the user's eng-managed
	// podman-machine-default. Empty (the default) means no
	// `--connection` flag is added; podman picks its configured
	// default connection. The flag must come *before* the
	// subcommand in argv order.
	PodmanMachineName string
	// TentImageRef is the podman image reference (e.g.
	// "clown-tent:1.2.3") that --tent runs the provider inside.
	// Loaded on demand from TentImageTarball when not already
	// present in the local podman image store. Baked on every
	// platform; on darwin where TentImageTarball is empty, an
	// image-store miss surfaces as "image not present locally and
	// no tarball is wired in" instead of an opaque earlier failure.
	TentImageRef string
	// TentImageTarball is the absolute path to a docker-format
	// image tarball produced by dockerTools.buildImage. clown runs
	// `podman load -i <tarball>` on first --tent invocation if the
	// image is not already in the local store. Linux-only — the
	// darwin builder produces an unusable image (manifest claims
	// linux/arm64, content is mach-O). Where empty, --tent falls
	// back to building the image on demand via TentImageFlakeRef.
	// Empty in dev builds.
	TentImageTarball string
	// TentImageFlakeRef is the nix flake reference whose
	// `packages.<linux-system>.tent-image` output produces the tent
	// container image. Baked at build time as a nix store path
	// captured from `${self}`, so it's available to any clown built
	// from this flake until that store path is GC'd. Used by --tent
	// when TentImageTarball is empty (darwin) or when a future
	// profile selects a non-baked image (planned). Empty in dev
	// builds (go build, go run) — --tent build-on-miss is
	// unavailable there, and ensureTentImage surfaces a clear error.
	TentImageFlakeRef string
	// ClaudeTentCliPath is the absolute path to the unpatched
	// claude-code binary used inside tent. Sourced from
	// numtide/llm-agents.nix (self-contained binary distribution,
	// latest as of the flake input). Distinct from ClaudeCliPath,
	// which points at clown's managed-settings-patched claude-code
	// 2.1.111 used outside tent. Wired in on linux and darwin;
	// empty in dev builds, where --tent errors out clearly.
	// See FDR-0007.
	ClaudeTentCliPath string
)
