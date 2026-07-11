package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	rm "github.com/amarbel-llc/clown/internal/juggler"
)

// modelUsage is printed on any `juggler model` usage error — missing or
// unknown subcommand, or a missing required flag on `add`/`remove`.
const modelUsage = "usage: juggler model <list|add <name> --style <anthropic|openai-compat> --url <url> --token <token>|remove <name>>"

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
// --url <url> --token <token>` and issues an AddRemoteModel RPC. All
// three flags are required; both space and `--flag=value` forms are
// accepted (matching cmdStart's --alias/--bind convention). style is
// validated against the "anthropic"/"openai-compat" enum before any RPC
// call is attempted.
func cmdModelAdd(cli *rm.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 1
	}
	name := args[0]
	rest := args[1:]

	var style, url, token string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--style":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "juggler: --style requires an argument")
				return 1
			}
			style = rest[i+1]
			i++
		case strings.HasPrefix(a, "--style="):
			style = strings.TrimPrefix(a, "--style=")
		case a == "--url":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "juggler: --url requires an argument")
				return 1
			}
			url = rest[i+1]
			i++
		case strings.HasPrefix(a, "--url="):
			url = strings.TrimPrefix(a, "--url=")
		case a == "--token":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "juggler: --token requires an argument")
				return 1
			}
			token = rest[i+1]
			i++
		case strings.HasPrefix(a, "--token="):
			token = strings.TrimPrefix(a, "--token=")
		default:
			fmt.Fprintf(os.Stderr, "juggler: unknown flag %q\n", a)
			return 1
		}
	}
	if style == "" || url == "" || token == "" {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 1
	}
	if style != "anthropic" && style != "openai-compat" {
		fmt.Fprintf(os.Stderr, "juggler: --style must be \"anthropic\" or \"openai-compat\", got %q\n", style)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cli.AddRemoteModel(ctx, rm.AddRemoteModelParams{
		Name:  name,
		Style: style,
		URL:   url,
		Token: token,
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
