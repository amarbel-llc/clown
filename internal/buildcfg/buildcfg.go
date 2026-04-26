package buildcfg

var (
	ClaudeCliPath       string
	CodexCliPath        string
	CircusCliPath       string
	OpencodeCliPath     string
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
	DefaultModelPath    string
	CircusModelName     string
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
)
