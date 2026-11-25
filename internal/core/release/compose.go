// Package release compiles self-contained, immutable Compose inputs.
package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/distribution/reference"
	"go.yaml.in/yaml/v3"
)

const maxComposeBytes = 256 << 10

var (
	serviceName     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	environmentName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	imageDigest     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type compose struct {
	Services map[string]service  `yaml:"services" json:"services"`
	Volumes  map[string]resource `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Networks map[string]resource `yaml:"networks,omitempty" json:"networks,omitempty"`
}

type service struct {
	Restart     string           `yaml:"restart,omitempty" json:"restart,omitempty"`
	Labels      composeKeyValues `yaml:"labels,omitempty" json:"labels,omitempty"`
	Image       string           `yaml:"image" json:"image"`
	Command     *[]string        `yaml:"command,omitempty" json:"command,omitempty"`
	Environment composeKeyValues `yaml:"environment,omitempty" json:"environment,omitempty"`
	Ports       []string         `yaml:"ports,omitempty" json:"ports,omitempty"`
	Volumes     []string         `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Networks    []string         `yaml:"networks,omitempty" json:"networks,omitempty"`
	Healthcheck *healthcheck     `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
}

type composeKeyValues map[string]string

func (values *composeKeyValues) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		var mapped map[string]string
		if err := node.Decode(&mapped); err != nil {
			return err
		}
		*values = mapped
		return nil
	}
	mapped := make(map[string]string, len(node.Content))
	for _, item := range node.Content {
		key, value, assigned := strings.Cut(item.Value, "=")
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || !assigned || key == "" {
			return errors.New("environment and label lists require explicit key=value entries")
		}
		if _, duplicate := mapped[key]; duplicate {
			return errors.New("duplicate environment or label key")
		}
		mapped[key] = value
	}
	*values = mapped
	return nil
}

// Resolver maps a normalized, tagged reference to its SHA-256 manifest digest.
// Its caller must impose network deadlines and apply registry access policy.
type Resolver func(context.Context, string) (string, error)

type Release struct {
	document string
	digest   string
	external bool
}

func (r Release) Compose() string             { return r.document }
func (r Release) Digest() string              { return r.digest }
func (r Release) UsesExternalResources() bool { return r.external }

// Compile accepts the same document for a full upload and for a named
// template. Only the caller's explicit template allowlist permits overrides.
// The no-override, no-resolver path is memoized per document; see memo.go.
func Compile(ctx context.Context, data []byte, overrides map[string]string, allowed []string, resolve Resolver) (Release, error) {
	if overrides == nil && allowed == nil && resolve == nil && ctx.Err() == nil {
		if r, err, ok := compileCache.lookup(data); ok {
			return r, err
		}
		r, err := compileParsed(ctx, data, overrides, allowed, resolve)
		compileCache.remember(data, r, err)
		return r, err
	}
	return compileParsed(ctx, data, overrides, allowed, resolve)
}

func compileParsed(ctx context.Context, data []byte, overrides map[string]string, allowed []string, resolve Resolver) (Release, error) {
	doc, err := decodeCompose(data)
	if err != nil {
		return Release{}, err
	}
	return compileCompose(ctx, doc, overrides, allowed, resolve)
}

// CompileExisting normalizes an operator-supplied definition for an existing
// Compose project. Docker resolves tags on the destination node.
func CompileExisting(ctx context.Context, data []byte) (Release, error) {
	if ctx.Err() == nil {
		if r, err, ok := existingCache.lookup(data); ok {
			return r, err
		}
		r, err := compileExistingParsed(ctx, data)
		existingCache.remember(data, r, err)
		return r, err
	}
	doc, err := decodeCompose(data)
	if err != nil {
		return Release{}, err
	}
	return normalizeCompose(ctx, doc, nil, nil, nil, true)
}

func compileExistingParsed(ctx context.Context, data []byte) (Release, error) {
	doc, err := decodeCompose(data)
	if err != nil {
		return Release{}, err
	}
	return normalizeCompose(ctx, doc, nil, nil, nil, true)
}

func decodeCompose(data []byte) (compose, error) {
	if len(data) == 0 || len(data) > maxComposeBytes {
		return compose{}, errors.New("Compose input is empty or exceeds 256 KiB")
	}
	var doc compose
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return compose{}, fmt.Errorf("decode Compose input: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return compose{}, errors.New("expected a single Compose document")
	}
	return doc, nil
}

func compileCompose(ctx context.Context, doc compose, overrides map[string]string, allowed []string, resolve Resolver) (Release, error) {
	return normalizeCompose(ctx, doc, overrides, allowed, resolve, false)
}

func normalizeCompose(ctx context.Context, doc compose, overrides map[string]string, allowed []string, resolve Resolver, existing bool) (Release, error) {
	if len(doc.Services) == 0 || len(doc.Services) > 64 {
		return Release{}, errors.New("expected 1 to 64 services")
	}
	if err := validateVolumes(doc); err != nil {
		return Release{}, err
	}
	if err := validateNetworks(doc); err != nil {
		return Release{}, err
	}
	if err := applyOverrides(doc, overrides, allowed); err != nil {
		return Release{}, err
	}
	names := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := doc.Services[name]
		if err := validateHealthcheck(svc.Healthcheck); err != nil {
			return Release{}, fmt.Errorf("service %q: %w", name, err)
		}
		if !serviceName.MatchString(name) {
			return Release{}, fmt.Errorf("invalid service name %q", name)
		}
		if err := validateServiceSettings(svc); err != nil {
			return Release{}, fmt.Errorf("service %q: %w", name, err)
		}
		for key := range svc.Environment {
			if !environmentName.MatchString(key) {
				return Release{}, fmt.Errorf("invalid environment name for service %q", name)
			}
		}
		// Compose interpolates values even when the file is JSON. Reject all
		// source interpolation so Agent environment cannot change the snapshot.
		encoded, err := json.Marshal(svc)
		if err != nil {
			return Release{}, err
		}
		if bytes.ContainsRune(encoded, '$') {
			return Release{}, fmt.Errorf("interpolation is unsupported in service %q", name)
		}
		if err := ctx.Err(); err != nil {
			return Release{}, err
		}
		if existing {
			ref, err := reference.ParseNormalizedNamed(svc.Image)
			if err != nil {
				return Release{}, fmt.Errorf("invalid image reference in service %q", name)
			}
			svc.Image = reference.TagNameOnly(ref).String()
		} else {
			pinned, err := pinImage(ctx, svc.Image, resolve)
			if err != nil {
				return Release{}, fmt.Errorf("service %q: %w", name, err)
			}
			svc.Image = pinned
		}
		doc.Services[name] = svc
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return Release{}, err
	}
	if len(encoded) > maxComposeBytes {
		return Release{}, errors.New("compiled Compose document exceeds 256 KiB")
	}
	return Release{
		document: string(encoded), digest: hash(encoded),
		external: hasExternalResource(doc.Volumes) || hasExternalResource(doc.Networks),
	}, nil
}

func applyOverrides(doc compose, overrides map[string]string, allowed []string) error {
	permitted := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		permitted[name] = true
	}
	for name, image := range overrides {
		svc, ok := doc.Services[name]
		if !ok || !permitted[name] {
			return fmt.Errorf("image override for service %q is not allowed", name)
		}
		svc.Image = image
		doc.Services[name] = svc
	}
	return nil
}

func pinImage(ctx context.Context, image string, resolve Resolver) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", errors.New("invalid image reference")
	}
	var digest string
	if pinned, ok := named.(reference.Digested); ok {
		digest = pinned.Digest().String()
	} else {
		if resolve == nil {
			return "", errors.New("image requires a digest or a registry resolver")
		}
		digest, err = resolve(ctx, reference.TagNameOnly(named).String())
		if err != nil {
			return "", fmt.Errorf("resolve image digest: %w", err)
		}
	}
	if !imageDigest.MatchString(digest) {
		return "", errors.New("expected a SHA-256 manifest digest")
	}
	return strings.Join([]string{reference.TrimNamed(named).Name(), digest}, "@"), nil
}

func validateServiceSettings(svc service) error {
	switch svc.Restart {
	case "", "no", "always", "unless-stopped", "on-failure":
	default:
		tail, ok := strings.CutPrefix(svc.Restart, "on-failure:")
		retries, err := strconv.ParseUint(tail, 10, 32)
		if !ok || err != nil || strconv.FormatUint(retries, 10) != tail {
			return errors.New("restart must be no, always, unless-stopped, on-failure or on-failure:<retries>")
		}
	}
	if len(svc.Labels) > 128 {
		return errors.New("expected at most 128 labels per service")
	}
	for key, value := range svc.Labels {
		if len(key) == 0 || len(key) > 255 || strings.ContainsAny(key, " =\t\r\n\x00") ||
			strings.HasPrefix(key, "com.docker.compose.") || len(value) > 4096 || strings.ContainsRune(value, '\x00') {
			return errors.New("invalid or reserved service label")
		}
	}
	return nil
}

// ComposeProjectName is the stable Docker Compose project name for a target.
func ComposeProjectName(target string) string {
	return "moori-" + hash([]byte(target))[len("sha256:"):len("sha256:")+32]
}
