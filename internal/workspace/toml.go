package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

type tomlConfig struct {
	Name  string `toml:"name"`
	Roots []Root `toml:"roots,omitempty"`
}

func encodeConfigTOML(name string, roots []Root) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(tomlConfig{Name: name, Roots: roots}); err != nil {
		return nil, WrapError(ErrConfigInvalid, "encode config", err)
	}
	return buf.Bytes(), nil
}

func decodeConfigTOML(data []byte) (tomlConfig, error) {
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg tomlConfig
	if err := dec.Decode(&cfg); err != nil {
		var decErr *toml.DecodeError
		if errors.As(err, &decErr) {
			row, col := decErr.Position()
			return tomlConfig{}, &Error{
				Code: ErrConfigInvalid, Line: row, Column: col, Cause: err,
				Message: fmt.Sprintf("decode config at line %d, col %d: %s", row, col, decErr.Error()),
			}
		}
		return tomlConfig{}, WrapError(ErrConfigInvalid, "decode config", err)
	}
	return cfg, nil
}

func computeConfigHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
