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
	// provider in a container. Empty in dev builds; --tent fails
	// fast with a clear error when empty. See FDR-0007.
	PodmanPath string
	// TentImageRef is the podman image reference (e.g.
	// "clown-tent:1.2.3") that --tent runs the provider inside.
	// Loaded on demand from TentImageTarball when not already
	// present in the local podman image store. Empty in dev builds.
	TentImageRef string
	// TentImageTarball is the absolute path to a docker-format
	// image tarball produced by dockerTools.buildImage. clown runs
	// `podman load -i <tarball>` on first --tent invocation if the
	// image is not already in the local store. Empty in dev builds.
	TentImageTarball string
	// ClaudeTentCliPath is the absolute path to the unpatched
	// claude-code binary used inside tent. Sourced from
	// numtide/llm-agents.nix (self-contained binary distribution,
	// latest as of the flake input). Distinct from ClaudeCliPath,
	// which points at clown's managed-settings-patched claude-code
	// 2.1.111 used outside tent. Empty on non-linux builds; --tent
	// errors out clearly when empty. See FDR-0007.
	ClaudeTentCliPath string
)
