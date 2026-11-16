package pipmix

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/pelletier/go-toml/v2"
	"github.com/peterbourgon/ff/v3"
)

// parseFlags parses command-line flags, PIPMIX_* environment variables, and
// the -config file.
func parseFlags(fs *flag.FlagSet, args []string) error {
	return ff.Parse(fs, args,
		ff.WithEnvVarPrefix("PIPMIX"),
		ff.WithConfigFileFlag("config"),
		ff.WithConfigFileParser(parseTOML),
		ff.WithAllowMissingConfigFile(true),
	)
}

// parseTOML is an ff.ConfigFileParser that maps [table] key to flag table.key.
func parseTOML(r io.Reader, set func(name, value string) error) error {
	var m map[string]map[string]any
	if err := toml.NewDecoder(r).Decode(&m); err != nil {
		if de, ok := errors.AsType[*toml.DecodeError](err); ok {
			row, col := de.Position()
			return fmt.Errorf("config %d:%d: %s\n%s", row, col, de.Error(), de.String())
		}
		return fmt.Errorf("config: %w", err)
	}
	for _, table := range slices.Sorted(maps.Keys(m)) {
		for _, key := range slices.Sorted(maps.Keys(m[table])) {
			if err := set(table+"."+key, fmt.Sprint(m[table][key])); err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "pipmix", "config.toml")
}
