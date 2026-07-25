package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	return Decode(file)
}

func Decode(reader io.Reader) (Config, error) {
	decoder := yaml.NewDecoder(io.LimitReader(reader, 1<<20))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("config contains multiple YAML documents")
		}
		return Config{}, fmt.Errorf("decode trailing config: %w", err)
	}
	config.ApplyDefaults()
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func Encode(config Config) ([]byte, error) {
	config.ApplyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var output strings.Builder
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(config); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close config encoder: %w", err)
	}
	return []byte(output.String()), nil
}
