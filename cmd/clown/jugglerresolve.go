package main

import (
	"context"
	"fmt"
	"time"

	"code.linenisgreat.com/clown/internal/juggler"
)

// resolveViaJuggler asks the juggler daemon to resolve modelName to a
// runnable endpoint. The 90s timeout accommodates a local model that
// needs to be started (mirrors cmd/juggler's own cmdStart timeout);
// remote resolution returns near-instantly.
func resolveViaJuggler(modelName string) (juggler.ResolveModelResult, error) {
	socket, err := juggler.SocketPath()
	if err != nil {
		return juggler.ResolveModelResult{}, err
	}
	cli, err := juggler.NewClient(socket)
	if err != nil {
		return juggler.ResolveModelResult{}, fmt.Errorf(
			"juggler daemon unreachable (socket %s): %w — start it: juggler daemon", socket, err,
		)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := cli.ResolveModel(ctx, juggler.ResolveModelParams{Name: modelName})
	if err != nil {
		return juggler.ResolveModelResult{}, fmt.Errorf("resolve model %q: %w", modelName, err)
	}
	return res, nil
}

// jugglerModelKind is a side-effect-free lookup (ListModels — never starts
// an instance) used by the subagent-model compatibility check in
// applyNamedProfile, so a plain literal model string never accidentally
// spawns a local juggler instance just because it happens to collide with
// a registered alias name.
func jugglerModelKind(modelName string) (juggler.ModelKind, bool) {
	socket, err := juggler.SocketPath()
	if err != nil {
		return "", false
	}
	cli, err := juggler.NewClient(socket)
	if err != nil {
		return "", false
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cli.ListModels(ctx)
	if err != nil {
		return "", false
	}
	for _, m := range res.Models {
		if m.Name == modelName {
			return m.Kind, true
		}
	}
	return "", false
}
