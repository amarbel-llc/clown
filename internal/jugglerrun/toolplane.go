package jugglerrun

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"code.linenisgreat.com/clown/internal/jugglerloop"
	"code.linenisgreat.com/clown/internal/jugglertools"
	"code.linenisgreat.com/clown/internal/pluginhost"
)

// MCPToolExecutor adapts the one moxy upstream's client to the loop's
// ToolExecutor. The two packages share the error taxonomy: a returned error
// is a transport failure (the run ends with tool_error), isError is a
// tool-level failure the model sees.
type MCPToolExecutor struct {
	Client *jugglertools.Client
}

func (e MCPToolExecutor) Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, bool, error) {
	return e.Client.CallTool(ctx, name, args)
}

// LoopToolSpecs converts the upstream's tools to the loop's specs; the field
// sets match one to one.
func LoopToolSpecs(specs []jugglertools.ToolSpec) []jugglerloop.ToolSpec {
	out := make([]jugglerloop.ToolSpec, len(specs))
	for i, s := range specs {
		out[i] = jugglerloop.ToolSpec{Name: s.Name, Description: s.Description, InputSchema: s.InputSchema, Kind: s.Kind}
	}
	return out
}

// ConnectMoxy initializes the MCP session with the moxy upstream at url and
// lists its tools.
func ConnectMoxy(ctx context.Context, url string) ([]jugglerloop.ToolSpec, jugglerloop.ToolExecutor, error) {
	client := jugglertools.New(url)
	if err := client.Initialize(ctx); err != nil {
		return nil, nil, fmt.Errorf("moxy upstream %s: %w", url, err)
	}
	specs, err := client.ListTools(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("moxy upstream %s: %w", url, err)
	}
	return LoopToolSpecs(specs), MCPToolExecutor{Client: client}, nil
}

// MoxyBinEnv overrides the moxy binary `juggler run` launches.
const MoxyBinEnv = "JUGGLER_MOXY_BIN"

// MoxyNameTemplate is the advertised-name template juggler requires: moxy's
// default "{server}.{tool}" contains a dot, which neither the Anthropic nor
// the OpenAI tool-name grammar (^[a-zA-Z0-9_-]{1,64}$) accepts. Tool names
// therefore look like "ring_create_issue" in the ledger and the brief's
// tools allowlist alike.
const MoxyNameTemplate = "{server}_{tool}"

// moxyHandshakeTimeout bounds moxy's startup until its handshake line.
const moxyHandshakeTimeout = 60 * time.Second

// ToolPlaneLauncher starts the agent's one moxy upstream from its inline
// moxyfile and returns its MCP URL and a stop function.
type ToolPlaneLauncher interface {
	Launch(ctx context.Context, dir, moxyfile string) (url string, stop func(), err error)
}

// ExecMoxy launches `moxy serve-http --name-template '{server}_{tool}'` with
// CWD = dir after writing the brief's moxyfile to dir/moxyfile. moxyfile(5)
// merges ~/.config/moxy/moxyfile first and then every directory from HOME
// down to CWD, so the agent's file is the innermost layer and the service
// user's own moxyfiles are the ceiling. With no --listen moxy binds an
// ephemeral loopback port and prints the clown-plugin handshake on stdout,
// parsed with pluginhost.ParseHandshake. moxy (and every tool server it
// starts, the agent scope) gets the environment minus the principal
// (StripPrincipalEnv).
type ExecMoxy struct {
	Bin    string
	Stderr io.Writer
}

func (m ExecMoxy) Launch(ctx context.Context, dir, moxyfile string) (string, func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, "moxyfile"), []byte(moxyfile)); err != nil {
		return "", nil, fmt.Errorf("writing the agent moxyfile: %w", err)
	}
	cmd := exec.Command(m.Bin, "serve-http", "--name-template", MoxyNameTemplate)
	cmd.Dir = dir
	cmd.Env = StripPrincipalEnv(os.Environ())
	cmd.Stderr = m.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("starting %s: %w", m.Bin, err)
	}
	stop := func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	lines := make(chan string, 1)
	go func() {
		r := bufio.NewReader(stdout)
		line, err := r.ReadString('\n')
		if err == nil || line != "" {
			lines <- line
		}
		close(lines)
		_, _ = io.Copy(io.Discard, r) // keep moxy's stdout drained
	}()
	timer := time.NewTimer(moxyHandshakeTimeout)
	defer timer.Stop()
	select {
	case line, ok := <-lines:
		if !ok {
			stop()
			return "", nil, fmt.Errorf("%s exited before its handshake", m.Bin)
		}
		hs, err := pluginhost.ParseHandshake(line)
		if err != nil {
			stop()
			return "", nil, fmt.Errorf("%s: %w", m.Bin, err)
		}
		return hs.URL(), stop, nil
	case <-timer.C:
		stop()
		return "", nil, fmt.Errorf("%s printed no handshake within %s", m.Bin, moxyHandshakeTimeout)
	case <-ctx.Done():
		stop()
		return "", nil, ctx.Err()
	}
}

// FilterToolsToAllowlist keeps exactly the allowlisted tools (deny by
// default). A listed name the upstream does not advertise is returned in
// missing, so a typo fails fast instead of silently narrowing the toolset.
func FilterToolsToAllowlist(specs []jugglerloop.ToolSpec, allow []string) (kept []jugglerloop.ToolSpec, missing []string) {
	advertised := map[string]bool{}
	for _, s := range specs {
		advertised[s.Name] = true
	}
	allowed := map[string]bool{}
	for _, name := range allow {
		allowed[name] = true
		if !advertised[name] {
			missing = append(missing, name)
		}
	}
	for _, s := range specs {
		if allowed[s.Name] {
			kept = append(kept, s)
		}
	}
	return kept, missing
}

// ExtractURIsFromMCPContent is the loop's URI extractor for MCP results.
// CallTool returns the CallToolResult's content array, so the loop's default
// top-level-field rule is applied to each text block that parses as JSON;
// resource_link blocks contribute their uri and embedded resource blocks
// their resource.uri. Non-array content falls back to the default rule. The
// result is never nil.
func ExtractURIsFromMCPContent(tool string, content json.RawMessage) []string {
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		URI      string `json:"uri"`
		Resource *struct {
			URI string `json:"uri"`
		} `json:"resource"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return jugglerloop.ExtractURIsFromTopLevelFields(tool, content)
	}
	uris := []string{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			uris = append(uris, jugglerloop.ExtractURIsFromTopLevelFields(tool, json.RawMessage(b.Text))...)
		case "resource_link":
			if b.URI != "" {
				uris = append(uris, b.URI)
			}
		case "resource":
			if b.Resource != nil && b.Resource.URI != "" {
				uris = append(uris, b.Resource.URI)
			}
		}
	}
	return uris
}
