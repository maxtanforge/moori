package release

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type resource struct {
	Name     string `yaml:"name,omitempty" json:"name,omitempty"`
	External bool   `yaml:"external,omitempty" json:"external,omitempty"`
}

var dockerResourceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func validateResources(items map[string]resource, kind string) error {
	if len(items) > 64 {
		return fmt.Errorf("expected at most 64 named %ss", kind)
	}
	names := make(map[string]bool)
	for key, item := range items {
		if !serviceName.MatchString(key) {
			return fmt.Errorf("invalid %s key", kind)
		}
		if item.Name == "" && !item.External {
			continue
		}
		if !item.External || !dockerResourceName.MatchString(item.Name) {
			return fmt.Errorf("external %s requires an explicit Docker name", kind)
		}
		if kind == "network" && (strings.EqualFold(item.Name, "host") || strings.EqualFold(item.Name, "none") || strings.EqualFold(item.Name, "bridge")) {
			return fmt.Errorf("external network must be a user-defined bridge network")
		}
		if names[item.Name] {
			return fmt.Errorf("duplicate Docker %s name", kind)
		}
		names[item.Name] = true
	}
	return nil
}

func validateNetworks(doc compose) error {
	if err := validateResources(doc.Networks, "network"); err != nil {
		return err
	}
	for _, svc := range doc.Services {
		if len(svc.Networks) > 64 {
			return fmt.Errorf("expected at most 64 networks per service")
		}
		seen := make(map[string]bool)
		for _, key := range svc.Networks {
			if _, ok := doc.Networks[key]; !ok || seen[key] {
				return fmt.Errorf("service networks must reference distinct declared networks")
			}
			seen[key] = true
		}
	}
	return nil
}

// ExternalResources returns the concrete Docker names in a compiled release.
func ExternalResources(document string) (volumes, networks []string, err error) {
	var doc compose
	if err := json.Unmarshal([]byte(document), &doc); err != nil {
		return nil, nil, err
	}
	return externalNames(doc.Volumes), externalNames(doc.Networks), nil
}

func hasExternalResource(items map[string]resource) bool {
	for _, item := range items {
		if item.External {
			return true
		}
	}
	return false
}

func externalNames(items map[string]resource) []string {
	var names []string
	for _, item := range items {
		if item.External {
			names = append(names, item.Name)
		}
	}
	slices.Sort(names)
	return names
}
