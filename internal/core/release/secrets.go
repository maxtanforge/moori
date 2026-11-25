package release

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
)

const MaxSecretValues = 16
const MaxSecretValueBytes = 16 << 10

var secretVersion = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

type SecretBindings struct {
	Secrets  map[string]string   `json:"secrets,omitempty"`
	Services map[string][]string `json:"services,omitempty"`
}

func ValidSecretName(name string) bool       { return len(name) <= 64 && environmentName.MatchString(name) }
func ValidSecretVersion(version string) bool { return secretVersion.MatchString(version) }

func (b SecretBindings) Canonical() (SecretBindings, error) {
	if len(b.Secrets) > MaxSecretValues || len(b.Services) > 64 {
		return SecretBindings{}, errors.New("secret bindings exceed limit")
	}
	referenced := make(map[string]bool, len(b.Secrets))
	mounts := make(map[string][]string, len(b.Services))
	for service, names := range b.Services {
		if !serviceName.MatchString(service) || len(names) == 0 || len(names) > MaxSecretValues {
			return SecretBindings{}, errors.New("invalid secret service binding")
		}
		list := slices.Clone(names)
		slices.Sort(list)
		for i, name := range list {
			if _, ok := b.Secrets[name]; !ok || i > 0 && list[i-1] == name {
				return SecretBindings{}, errors.New("invalid or duplicate mounted secret")
			}
			referenced[name] = true
		}
		mounts[service] = list
	}
	for name, version := range b.Secrets {
		if !ValidSecretName(name) || !ValidSecretVersion(version) || !referenced[name] {
			return SecretBindings{}, errors.New("each secret requires a named service and a valid version")
		}
	}
	return SecretBindings{Secrets: b.Secrets, Services: mounts}, nil
}

func (r Release) validateSecretServices(mounts map[string][]string) error {
	if len(mounts) == 0 {
		return nil
	}
	var doc compose
	if err := json.Unmarshal([]byte(r.document), &doc); err != nil {
		return err
	}
	for name, secrets := range mounts {
		svc, ok := doc.Services[name]
		if !ok {
			return errors.New("secret mount references a service absent from the release")
		}
		for _, mount := range svc.Volumes {
			target := strings.Split(mount, ":")[1]
			for _, secret := range secrets {
				secretPath := "/run/secrets/" + secret
				if target == secretPath || strings.HasPrefix(secretPath, target+"/") || strings.HasPrefix(target, secretPath+"/") {
					return errors.New("volume mount overlaps a bound secret path")
				}
			}
		}
	}
	return nil
}
