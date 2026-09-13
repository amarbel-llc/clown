package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"code.linenisgreat.com/tommy/pkg/marshal"
)

// localGatewayConfigFile is the on-disk shape of the opencode/crush local
// gateway configs (~/.config/clown/{opencode,crush}.toml).
type localGatewayConfigFile struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

// writeLocalGatewayConfigFile writes url and token to the TOML file at path
// through tommy (clown#238), so an existing file keeps its comments and
// layout. Creates the parent directory at 0o700; the file is 0o600 because it
// carries a token.
func writeLocalGatewayConfigFile(path, url, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var cfg localGatewayConfigFile
	handle, err := marshal.UnmarshalDocument(existing, &cfg)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.URL, cfg.Token = url, token
	body, err := marshal.MarshalDocument(handle, &cfg)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
