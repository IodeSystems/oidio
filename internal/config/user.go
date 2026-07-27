package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// User is the per-user defaults file for oidio's TOOLING — not for the server.
//
// Kept separate from the server config on purpose. oidio.yaml describes the
// models a HOST serves and is deployment state; this describes where an operator
// happens to point their own CLI, and is personal. Merging them would put a
// developer's endpoint into a config that ships with a machine.
//
// Read from $OIDIO_USER_CONFIG, else ~/.oidio.yml, else ~/.oidio.yaml. Absent is
// not an error: every field here has a flag.
type User struct {
	// Config is the default server config to read model definitions from, so the
	// CLI does not need --config in a directory that has no oidio.yaml.
	Config string `yaml:"config"`

	// LLM is the content reviewer used by `speakers review --llm`. oidio hosts no
	// language model; this points at an OpenAI-compatible endpoint.
	LLM struct {
		Endpoint string `yaml:"endpoint"`
		Model    string `yaml:"model"`
		Key      string `yaml:"key"`
	} `yaml:"llm"`
}

// LoadUser reads the user defaults. A missing file yields a zero User and no
// error; a malformed one is an error, because silently ignoring a file the user
// wrote is worse than refusing to start.
func LoadUser() (User, error) {
	var u User
	path := os.Getenv("OIDIO_USER_CONFIG")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return u, nil
		}
		for _, p := range []string{".oidio.yml", ".oidio.yaml"} {
			c := filepath.Join(home, p)
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}
	if path == "" {
		return u, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return u, nil
	}
	if err := yaml.Unmarshal(b, &u); err != nil {
		return u, err
	}
	// A key may be held in the environment rather than written to disk.
	u.LLM.Key = os.ExpandEnv(u.LLM.Key)
	return u, nil
}
