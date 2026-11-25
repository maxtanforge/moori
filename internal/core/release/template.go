package release

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type TemplatePort struct {
	Service string `json:"service"`
	Port    int    `json:"port"`
	Value   string `json:"value,omitempty"`
}

type TemplateEnvironment struct {
	Service string `json:"service"`
	Key     string `json:"key"`
	Value   string `json:"value,omitempty"`
}

type TemplateResource struct {
	Key      string `json:"key"`
	Name     string `json:"name,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type TemplateParameters struct {
	Ports       []TemplatePort        `json:"ports"`
	Environment []TemplateEnvironment `json:"environment"`
	Volumes     []TemplateResource    `json:"volumes"`
	Networks    []TemplateResource    `json:"networks"`
}

type TemplateOverrides struct {
	Ports       []TemplatePort        `json:"ports,omitempty"`
	Environment []TemplateEnvironment `json:"environment,omitempty"`
	Volumes     []TemplateResource    `json:"volumes,omitempty"`
	Networks    []TemplateResource    `json:"networks,omitempty"`
}

// PrepareTemplate validates the supported Compose subset and detaches
// concrete external resource names so a template can move between nodes.
func PrepareTemplate(ctx context.Context, input []byte, fields TemplateParameters) (Release, TemplateParameters, error) {
	doc, err := decodeCompose(input)
	if err != nil {
		return Release{}, TemplateParameters{}, err
	}
	if err := validateResources(doc.Volumes, "volume"); err != nil {
		return Release{}, TemplateParameters{}, err
	}
	if err := validateNetworks(doc); err != nil {
		return Release{}, TemplateParameters{}, err
	}
	if err := validateTemplateEnvironment(doc); err != nil {
		return Release{}, TemplateParameters{}, err
	}
	if len(fields.Ports) > 64 || len(fields.Environment) > 64 {
		return Release{}, TemplateParameters{}, fmt.Errorf("template accepts at most 64 ports and 64 environment parameters")
	}
	result := TemplateParameters{Ports: []TemplatePort{}, Environment: []TemplateEnvironment{}}
	portSet := make(map[TemplatePort]bool)
	for _, field := range fields.Ports {
		svc, ok := doc.Services[field.Service]
		if !ok || field.Port < 0 || field.Port >= len(svc.Ports) || field.Value != "" || portSet[field] {
			return Release{}, TemplateParameters{}, fmt.Errorf("duplicate or invalid template port declaration")
		}
		if _, err := parseTemplatePort(svc.Ports[field.Port]); err != nil {
			return Release{}, TemplateParameters{}, err
		}
		portSet[field] = true
		field.Value = svc.Ports[field.Port]
		result.Ports = append(result.Ports, field)
	}
	envSet := make(map[TemplateEnvironment]bool)
	for _, field := range fields.Environment {
		_, ok := doc.Services[field.Service].Environment[field.Key]
		if !ok || field.Value != "" || envSet[field] || credentialEnvironment(field.Key) {
			return Release{}, TemplateParameters{}, fmt.Errorf("duplicate, credential-related or invalid template environment declaration")
		}
		envSet[field] = true
		result.Environment = append(result.Environment, field)
	}
	result.Volumes, err = detachResourceBindings(doc.Volumes, fields.Volumes)
	if err != nil {
		return Release{}, TemplateParameters{}, err
	}
	result.Networks, err = detachResourceBindings(doc.Networks, fields.Networks)
	if err != nil {
		return Release{}, TemplateParameters{}, err
	}
	artifact, err := compileCompose(ctx, doc, nil, nil, nil)
	return artifact, result, err
}

func detachResourceBindings(resources map[string]resource, declarations []TemplateResource) ([]TemplateResource, error) {
	parameters := make([]TemplateResource, 0, len(declarations))
	found := make(map[string]bool)
	for _, field := range declarations {
		value, ok := resources[field.Key]
		if !ok || field.Name != "" || found[field.Key] {
			return nil, fmt.Errorf("duplicate or undeclared template resource key")
		}
		field.Required = field.Required || value.External
		parameters = append(parameters, field)
		found[field.Key] = true
	}
	for _, key := range slices.Sorted(maps.Keys(resources)) {
		if resources[key].External {
			resources[key] = resource{}
			if !found[key] {
				parameters = append(parameters, TemplateResource{Key: key, Required: true})
			}
		}
	}
	return parameters, nil
}

// InstantiateTemplate adjusts explicitly declared settings and recompiles the
// same Compose subset used by normal releases and downstream verification.
func InstantiateTemplate(ctx context.Context, document string, fields TemplateParameters, deploy TemplateOverrides) (Release, error) {
	var doc compose
	if err := json.Unmarshal([]byte(document), &doc); err != nil {
		return Release{}, err
	}
	availablePorts := make(map[TemplatePort]string, len(fields.Ports))
	for _, field := range fields.Ports {
		availablePorts[TemplatePort{Service: field.Service, Port: field.Port}] = field.Value
	}
	for _, input := range deploy.Ports {
		key := TemplatePort{Service: input.Service, Port: input.Port}
		current, ok := availablePorts[key]
		if !ok {
			return Release{}, fmt.Errorf("duplicate or undeclared port override")
		}
		delete(availablePorts, key)
		value, err := replaceTemplatePort(current, input.Value)
		if err != nil {
			return Release{}, err
		}
		doc.Services[input.Service].Ports[input.Port] = value
	}
	availableEnv := make(map[TemplateEnvironment]bool, len(fields.Environment))
	for _, field := range fields.Environment {
		availableEnv[field] = true
	}
	for _, input := range deploy.Environment {
		key := TemplateEnvironment{Service: input.Service, Key: input.Key}
		if !availableEnv[key] {
			return Release{}, fmt.Errorf("duplicate or undeclared environment override")
		}
		delete(availableEnv, key)
		doc.Services[input.Service].Environment[input.Key] = input.Value
	}
	if err := validateTemplateEnvironment(doc); err != nil {
		return Release{}, err
	}
	if err := bindResources(doc.Volumes, fields.Volumes, deploy.Volumes); err != nil {
		return Release{}, err
	}
	if err := bindResources(doc.Networks, fields.Networks, deploy.Networks); err != nil {
		return Release{}, err
	}
	return compileCompose(ctx, doc, nil, nil, nil)
}

func bindResources(resources map[string]resource, fields, overrides []TemplateResource) error {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field.Key] = field.Required
	}
	for _, value := range overrides {
		if _, ok := allowed[value.Key]; !ok || !dockerResourceName.MatchString(value.Name) {
			return fmt.Errorf("undeclared, duplicate or invalid external resource binding")
		}
		delete(allowed, value.Key)
		resources[value.Key] = resource{Name: value.Name, External: true}
	}
	for key, required := range allowed {
		if required {
			return fmt.Errorf("external resource %q requires a Docker name", key)
		}
	}
	return nil
}

type publishedPort struct{ address, published, target, protocol string }

func parseTemplatePort(value string) (publishedPort, error) {
	bad := fmt.Errorf("template port parameter must describe one fixed published port")
	mapping, protocol, _ := strings.Cut(value, "/")
	if protocol != "" && protocol != "tcp" && protocol != "udp" && protocol != "sctp" {
		return publishedPort{}, bad
	}
	end := strings.LastIndexByte(mapping, ':')
	if end < 0 {
		return publishedPort{}, bad
	}
	p := publishedPort{target: mapping[end+1:], published: mapping[:end], protocol: protocol}
	if split := strings.LastIndexByte(p.published, ':'); split >= 0 {
		p.address, p.published = p.published[:split], p.published[split+1:]
		ip, err := netip.ParseAddr(strings.TrimPrefix(strings.TrimSuffix(p.address, "]"), "["))
		if err != nil || ip.Zone() != "" || ip.Is6() && !strings.HasPrefix(p.address, "[") ||
			strings.HasPrefix(p.address, "[") != strings.HasSuffix(p.address, "]") {
			return publishedPort{}, bad
		}
	}
	if !validTemplatePort(p.target) || !validTemplatePort(p.published) {
		return publishedPort{}, bad
	}
	return p, nil
}

func validTemplatePort(value string) bool {
	n, err := strconv.Atoi(value)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == value
}

func replaceTemplatePort(original, override string) (string, error) {
	base, err := parseTemplatePort(original)
	if err != nil {
		return "", err
	}
	published := override
	if !validTemplatePort(override) {
		next, err := parseTemplatePort(override)
		if err != nil || base.address != next.address || base.target != next.target ||
			defaultProtocol(base.protocol) != defaultProtocol(next.protocol) {
			return "", fmt.Errorf("a port override may change only the published port")
		}
		published = next.published
	}
	result := published + ":" + base.target
	if base.address != "" {
		result = base.address + ":" + result
	}
	if base.protocol != "" {
		result += "/" + base.protocol
	}
	return result, nil
}

func defaultProtocol(value string) string {
	if value == "" {
		return "tcp"
	}
	return value
}

var credentialName = regexp.MustCompile(`(?:^|_)(?:PASSWORD|PASSWD|PASS|PASSPHRASE|PWD|TOKEN|CREDENTIALS?|SECRET|API_?KEY|ACCESS_?KEY|PRIVATE_?KEY)(?:_|$)`)

func credentialEnvironment(name string) bool {
	return credentialName.MatchString(strings.ToUpper(name))
}

func validateTemplateEnvironment(doc compose) error {
	for _, svc := range doc.Services {
		for key, value := range svc.Environment {
			if credentialEnvironment(key) && !validSecretFileReference(key, value) {
				return fmt.Errorf("template credential environment must use a /run/secrets file reference")
			}
			if strings.Contains(value, "-----BEGIN ") || hasURLCredential(value) {
				return fmt.Errorf("inline credentials are unsupported in template environment")
			}
		}
	}
	return nil
}

func validSecretFileReference(key, value string) bool {
	return strings.HasSuffix(strings.ToUpper(key), "_FILE") && strings.HasPrefix(value, "/run/secrets/") &&
		ValidSecretName(strings.TrimPrefix(value, "/run/secrets/"))
}

func hasURLCredential(value string) bool {
	if !strings.Contains(value, "://") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.User != nil
}

// TemplateSecretFiles discovers portable file-based secret requirements;
// their destination versions are assigned by the execution target.
func (r Release) TemplateSecretFiles() (map[string][]string, error) {
	var doc compose
	if err := json.Unmarshal([]byte(r.document), &doc); err != nil {
		return nil, err
	}
	requirements := make(map[string][]string)
	for service, svc := range doc.Services {
		names := make(map[string]bool)
		for key, value := range svc.Environment {
			if validSecretFileReference(key, value) {
				names[strings.TrimPrefix(value, "/run/secrets/")] = true
			}
		}
		if len(names) != 0 {
			requirements[service] = slices.Sorted(maps.Keys(names))
		}
	}
	return requirements, nil
}
