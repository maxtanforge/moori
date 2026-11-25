package release

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

type healthcheck struct {
	Test          []string `yaml:"test" json:"test"`
	Interval      *string  `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout       *string  `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries       *int     `yaml:"retries,omitempty" json:"retries,omitempty"`
	StartPeriod   *string  `yaml:"start_period,omitempty" json:"start_period,omitempty"`
	StartInterval *string  `yaml:"start_interval,omitempty" json:"start_interval,omitempty"`
}

func validateVolumes(doc compose) error {
	if err := validateResources(doc.Volumes, "volume"); err != nil {
		return err
	}

	for name, svc := range doc.Services {
		if len(svc.Volumes) > 64 {
			return fmt.Errorf("service %q exceeds 64 volume mounts", name)
		}
		targets := make(map[string]bool)
		for _, mount := range svc.Volumes {
			parts := strings.Split(mount, ":")
			if len(parts) < 2 || len(parts) > 3 {
				return errors.New("expected named-volume:container-path[:ro|rw]")
			}
			if _, ok := doc.Volumes[parts[0]]; !ok {
				return errors.New("mount must reference a declared volume")
			}
			target := parts[1]
			if !path.IsAbs(target) || target == "/" || path.Clean(target) != target || strings.ContainsAny(target, "\\\x00\r\n$") || targets[target] {
				return errors.New("volume target must be a unique clean absolute container path")
			}
			if len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw" {
				return errors.New("volume mode must be ro or rw")
			}
			targets[target] = true
		}
	}
	return nil
}

func validateHealthcheck(h *healthcheck) error {
	if h == nil {
		return nil
	}
	if len(h.Test) < 2 || len(h.Test) > 64 || h.Test[0] != "CMD" && h.Test[0] != "CMD-SHELL" || h.Test[0] == "CMD-SHELL" && len(h.Test) != 2 {
		return errors.New("healthcheck test requires CMD arguments or one CMD-SHELL command")
	}
	if strings.TrimSpace(h.Test[1]) == "" {
		return errors.New("healthcheck command is empty")
	}
	for _, arg := range h.Test {
		if len(arg) > 4096 || strings.ContainsRune(arg, '\x00') {
			return errors.New("invalid healthcheck argument")
		}
	}
	for _, field := range []struct {
		name  string
		value *string
		zero  bool
	}{
		{"interval", h.Interval, false}, {"timeout", h.Timeout, false}, {"start_period", h.StartPeriod, true}, {"start_interval", h.StartInterval, false},
	} {
		if field.value == nil {
			continue
		}
		duration, err := time.ParseDuration(*field.value)
		if err != nil || duration > 30*time.Minute || duration < time.Millisecond && !(field.zero && duration == 0) {
			return fmt.Errorf("healthcheck %s must be 1ms to 30m (start_period also permits zero)", field.name)
		}
	}
	if h.Retries != nil && (*h.Retries < 1 || *h.Retries > 100) {
		return errors.New("healthcheck retries must be 1 to 100")
	}
	return nil
}
