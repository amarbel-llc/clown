package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// modelUsage is printed on any `juggler model` usage error — missing or
// unknown subcommand, or a missing required flag on `add`/`remove`.
const modelUsage = "usage: juggler model <list|add <name> --style <anthropic|openai-compat|decisions> --url <url> (--token <token> | --token-file <path>) [--model <upstream-id>]|remove <name>>"

// cmdModel dispatches the `juggler model` subcommand family: the unified
// (local + remote) model registry surface, distinct from the legacy
// `juggler models` (plural, local-GGUF-only) command.
func cmdModel(cli *rm.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 1
	}
	switch args[0] {
	case "list":
		return cmdModelList(cli)
	case "add":
		return cmdModelAdd(cli, args[1:])
	case "remove":
		return cmdModelRemove(cli, args[1:])
	default:
		fmt.Fprintln(os.Stderr, modelUsage)
		return 1
	}
}

// cmdModelList asks juggler for the unified (local + remote) model view
// and prints a NAME/KIND/STYLE table. Unlike cmdModels (plural), this
// always prints the header even for an empty result — it's a status
// table, not a script-friendly name stream.
func cmdModelList(cli *rm.Client) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cli.ListModels(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "juggler: model list: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tSTYLE")
	for _, m := range res.Models {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Name, m.Kind, m.Style)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "juggler: model list: %v\n", err)
		return 1
	}
	return 0
}

// cmdModelAdd parses argv for `juggler model add <name> --style <style>
// --url <url> (--token <token> | --token-file <path>) [--model <id>]` and
// issues an AddRemoteModel RPC. The leading name is peeled first; the flags
// (space and `--flag=value` forms alike) then go through a flag.FlagSet.
// style is validated against rm.RemoteStyles before any RPC call.
func cmdModelAdd(cli *rm.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 1
	}
	name := args[0]

	var style, url, token, tokenFile, modelID string
	fs := flag.NewFlagSet("model add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&style, "style", "", "remote style ("+strings.Join(rm.RemoteStyles(), ", ")+")")
	fs.StringVar(&url, "url", "", "endpoint base URL")
	fs.StringVar(&token, "token", "", "bearer token, literal or ${VAR}")
	fs.StringVar(&tokenFile, "token-file", "", "file holding the bearer token")
	fs.StringVar(&modelID, "model", "", "upstream model id the entry aliases")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "juggler: unexpected argument %q\n%s\n", fs.Arg(0), modelUsage)
		return 1
	}
	if style == "" || url == "" || (token == "" && tokenFile == "") {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 1
	}
	if token != "" && tokenFile != "" {
		fmt.Fprintln(os.Stderr, "juggler: --token and --token-file are mutually exclusive")
		return 1
	}
	if !rm.IsRemoteStyle(style) {
		fmt.Fprintf(os.Stderr, "juggler: --style must be one of %s, got %q\n", strings.Join(rm.RemoteStyles(), ", "), style)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cli.AddRemoteModel(ctx, rm.AddRemoteModelParams{
		Name:      name,
		Style:     style,
		URL:       url,
		Token:     token,
		TokenFile: tokenFile,
		ModelID:   modelID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "juggler: model add: %v\n", err)
		return 1
	}
	fmt.Printf("juggler: registered remote model %q\n", name)
	return 0
}

// cmdModelRemove issues a RemoveRemoteModel RPC for the given name.
func cmdModelRemove(cli *rm.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 1
	}
	name := args[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cli.RemoveRemoteModel(ctx, rm.RemoveRemoteModelParams{Name: name}); err != nil {
		fmt.Fprintf(os.Stderr, "juggler: model remove: %v\n", err)
		return 1
	}
	fmt.Printf("juggler: removed remote model %q\n", name)
	return 0
}
