package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/amarbel-llc/clown/internal/jugglermodels"
	rm "github.com/amarbel-llc/clown/internal/juggler"
)

// listAvailableModels returns the GGUF files under dir as rm.AvailableModel.
// Empty dir defaults to jugglermodels.Dir(). A missing directory yields
// an empty slice with a nil error, matching jugglermodels.List's behavior.
// If the resolved dir is still empty (jugglermodels.Dir() returns "" when
// $HOME is unresolvable), return an error rather than calling os.ReadDir("")
// which would silently read $PWD.
func listAvailableModels(dir string) ([]rm.AvailableModel, error) {
	if dir == "" {
		dir = jugglermodels.Dir()
	}
	if dir == "" {
		return nil, fmt.Errorf("juggler models dir unavailable: cannot resolve $HOME")
	}
	names, err := jugglermodels.List(dir)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	out := make([]rm.AvailableModel, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name+".gguf")
		st, err := os.Stat(path)
		if err != nil {
			// The file disappeared between the List and Stat — skip it.
			continue
		}
		out = append(out, rm.AvailableModel{
			Name: name,
			Path: path,
			Size: st.Size(),
		})
	}
	return out, nil
}
