// Package jugglereval is the juggler agent evaluator (FDR 0019 §4): it
// collapses a run ledger into one boolean by running the brief's jq program
// through an embedded gojq, with no ambient authority.
//
// Restrictions, each enforced and tested:
//   - input is the ledger JSON document;
//   - the program MUST yield exactly one boolean; zero outputs, a second
//     output, a non-boolean or a runtime error all return (false, err);
//   - no environment ($ENV / env see nothing);
//   - no input/inputs (no input iterator is supplied);
//   - no module loader (import/include fail at compile time);
//   - a wall-clock budget enforced by context cancellation.
//
// gojq has no step counter, so a pathological program is bounded only by the
// wall clock (FDR 0019 Limitations). Callers treat any error as failure.
package jugglereval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/itchyny/gojq"
)

// KindJQ is the only evaluator kind supported in this slice.
const KindJQ = "jq"

// DefaultTimeout is the wall-clock budget applied when the caller's context
// carries no deadline.
const DefaultTimeout = 2 * time.Second

// Evaluate runs program of the given kind over the ledger and returns its
// single boolean verdict. Any error means the verdict is false.
func Evaluate(ctx context.Context, kind, program string, ledger json.RawMessage) (bool, error) {
	if kind != KindJQ {
		return false, fmt.Errorf("jugglereval: evaluator kind %q is not supported (only %q)", kind, KindJQ)
	}
	return evaluateJQ(ctx, program, ledger)
}

func evaluateJQ(ctx context.Context, program string, ledger json.RawMessage) (bool, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	var input any
	dec := json.NewDecoder(bytes.NewReader(ledger))
	if err := dec.Decode(&input); err != nil {
		return false, fmt.Errorf("jugglereval: decoding ledger: %w", err)
	}
	if dec.More() {
		return false, errors.New("jugglereval: ledger holds more than one JSON document")
	}

	query, err := gojq.Parse(program)
	if err != nil {
		return false, fmt.Errorf("jugglereval: parsing program: %w", err)
	}
	code, err := gojq.Compile(query,
		gojq.WithEnvironLoader(func() []string { return nil }),
	)
	if err != nil {
		return false, fmt.Errorf("jugglereval: compiling program: %w", err)
	}

	iter := code.RunWithContext(ctx, input)

	first, ok := iter.Next()
	if !ok {
		return false, errors.New("jugglereval: program produced no output (want exactly one boolean)")
	}
	if err, isErr := first.(error); isErr {
		return false, fmt.Errorf("jugglereval: running program: %w", err)
	}
	verdict, isBool := first.(bool)
	if !isBool {
		return false, fmt.Errorf("jugglereval: program output is %T, want exactly one boolean", first)
	}
	if second, more := iter.Next(); more {
		if err, isErr := second.(error); isErr {
			return false, fmt.Errorf("jugglereval: running program: %w", err)
		}
		return false, errors.New("jugglereval: program produced more than one output (want exactly one boolean)")
	}
	return verdict, nil
}
