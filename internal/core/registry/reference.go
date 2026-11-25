package registry

import (
	"context"
	_ "crypto/sha256" // enables sha256 parsing in distribution/reference
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/distribution/reference"
)

var (
	ErrAmbiguous            = errors.New("multiple enabled registries match this image at the same repository prefix")
	ErrOperationCredentials = errors.New("registry operation credentials are unavailable")
	ErrDenied               = errors.New("registry access denied or configuration unavailable")
	ErrSecureTransport      = errors.New("private registry credentials require verified HTTPS")
	ErrRetry                = errors.New("registry provider temporarily unavailable")
)

type Credential struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	ServerAddress string `json:"serveraddress,omitempty"`
}

type Source func(context.Context, string, string) (map[string]Credential, error)

type Image struct {
	Reference  string
	Host       string
	Repository string
	Digest     string
}

func NormalizeHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return "docker.io", nil
	}
	named, err := reference.ParseNormalizedNamed(host + "/moori-probe")
	if err != nil || reference.Domain(named) != host || len(host) > 253 {
		return "", errors.New("invalid registry hostname")
	}
	return host, nil
}

func NormalizeRepositoryPrefix(prefix string) (string, error) {
	if prefix == "" {
		return "", nil
	}
	named, err := reference.ParseNormalizedNamed("registry.invalid/" + prefix)
	if err != nil || reference.Path(named) != prefix || named.String() != "registry.invalid/"+prefix {
		return "", errors.New("invalid registry repository prefix")
	}
	return prefix, nil
}

func InRepository(repository, prefix string) bool {
	return prefix == "" || repository == prefix || strings.HasPrefix(repository, prefix+"/")
}

func ParseImage(value string) (Image, error) {
	var image Image
	if len(value) > 384 {
		return image, errors.New("invalid registry image reference")
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return image, errors.New("invalid registry image reference")
	}
	host, err := NormalizeHost(reference.Domain(named))
	if err != nil {
		return image, err
	}
	image.Host = host
	image.Repository = reference.Path(named)
	if pinned, ok := named.(reference.Digested); ok {
		image.Digest = pinned.Digest().String()
		if !strings.HasPrefix(image.Digest, "sha256:") {
			return Image{}, errors.New("registry image digest must use SHA-256")
		}
	}
	image.Reference = reference.TagNameOnly(named).String()
	return image, nil
}

// ComposeImages returns the distinct canonical image references in a verified snapshot.
func ComposeImages(document string) ([]Image, error) {
	var compose struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(document), &compose); err != nil || len(compose.Services) == 0 || len(compose.Services) > 64 {
		return nil, errors.New("invalid snapshot image set")
	}
	images := make([]Image, 0, len(compose.Services))
	seen := make(map[string]bool, len(compose.Services))
	for _, service := range compose.Services {
		image, err := ParseImage(service.Image)
		if err != nil || image.Digest == "" || image.Reference != service.Image {
			return nil, errors.New("invalid snapshot image reference")
		}
		if !seen[image.Reference] {
			seen[image.Reference] = true
			images = append(images, image)
		}
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Reference < images[j].Reference })
	return images, nil
}
