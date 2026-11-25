// Adopt converts an external Compose project into the managed subset so it
// can become a moori template and release. Conversion is conservative: a
// service is converted only when the managed model can run it equivalently,
// and every simplification is reported as an explicit issue instead of a
// silent drop.
package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/distribution/reference"
	"go.yaml.in/yaml/v3"
)

// AdoptInput describes one external project: the runtime Compose project
// name, the primary Compose file, and per-service candidate image digest
// references (repo@sha256:…) observed on the node. Manifest digests come
// from the node inventory, so adoption never needs registry access.
type AdoptInput struct {
	Project string
	Compose []byte
	Pins    map[string][]string
}

// AdoptIssue explains one simplification or omission. Service is empty for
// project-level findings.
type AdoptIssue struct {
	Service string `json:"service,omitempty"`
	Message string `json:"message"`
}

// Adopted is the conversion result. Compose is empty when no service could
// be converted; Issues then explain why.
type Adopted struct {
	Compose         string            `json:"compose"`
	Issues          []AdoptIssue      `json:"issues"`
	Services        []string          `json:"services"`
	Volumes         []string          `json:"volumes"`
	Networks        []string          `json:"networks"`
	Ports           []TemplatePort    `json:"ports,omitempty"`
	VolumeBindings  map[string]string `json:"volume_bindings,omitempty"`
	NetworkBindings map[string]string `json:"network_bindings,omitempty"`
}

const maxAdoptBytes = 256 << 10

var pinReference = regexp.MustCompile(`^[^@\s]+@sha256:[a-f0-9]{64}$`)

// serviceFields are the settings the managed subset can carry over.
var serviceFields = map[string]bool{
	"image": true, "command": true, "environment": true, "labels": true,
	"ports": true, "volumes": true, "networks": true, "restart": true, "healthcheck": true,
}

// noteFields are droppable without changing what the service runs; every
// other unsupported key blocks the service.
var noteFields = map[string]bool{
	"container_name": true, // must drop: old and managed projects run side by side
	"expose":         true, // documentation only
	"depends_on":     true, // startup ordering is not part of the managed subset
}

// Adopt converts the project or reports exactly what blocks adoption.
func Adopt(ctx context.Context, in AdoptInput) (Adopted, error) {
	if !dockerResourceName.MatchString(in.Project) {
		return Adopted{}, errors.New("invalid Compose project name")
	}
	if len(in.Compose) == 0 || len(in.Compose) > maxAdoptBytes {
		return Adopted{}, errors.New("Compose input is empty or exceeds 256 KiB")
	}
	var top map[string]yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(in.Compose))
	if err := dec.Decode(&top); err != nil {
		return Adopted{}, fmt.Errorf("decode Compose file: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return Adopted{}, errors.New("expected a single Compose document")
	}
	if _, ok := top["include"]; ok {
		return Adopted{}, errors.New("include is unsupported: merge multi-file projects into one file first")
	}
	if top["services"].Kind == 0 {
		return Adopted{}, errors.New("Compose file declares no services")
	}
	var rawServices map[string]map[string]yaml.Node
	servicesNode := top["services"]
	if err := servicesNode.Decode(&rawServices); err != nil {
		return Adopted{}, fmt.Errorf("decode services: %w", err)
	}
	var projectIssues []AdoptIssue
	volumes, err := parseResources(top["volumes"])
	if err != nil {
		return Adopted{}, fmt.Errorf("decode volumes: %w", err)
	}
	networks, err := parseResources(top["networks"])
	if err != nil {
		return Adopted{}, fmt.Errorf("decode networks: %w", err)
	}
	dropBroken(volumes, "卷", &projectIssues)
	dropBroken(networks, "网络", &projectIssues)

	a := &adopter{input: in, volumes: volumes, networks: networks, doc: compose{Services: map[string]service{}}, noted: map[string]bool{}}
	names := make([]string, 0, len(rawServices))
	for name := range rawServices {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return Adopted{}, err
		}
		a.convertService(name, rawServices[name])
	}

	result := Adopted{Issues: append(projectIssues, a.issues...)}
	if len(a.doc.Services) == 0 {
		return result, nil
	}
	if err := a.declareResources(); err != nil {
		result.Issues = append(result.Issues, AdoptIssue{Message: err.Error()})
		return result, nil
	}
	if err := validateVolumes(a.doc); err != nil {
		return Adopted{}, fmt.Errorf("converted volumes: %w", err)
	}
	if err := validateNetworks(a.doc); err != nil {
		return Adopted{}, fmt.Errorf("converted networks: %w", err)
	}
	encoded, err := yaml.Marshal(a.doc)
	if err != nil {
		return Adopted{}, err
	}
	// Self-check through the production compiler: adoption must never hand
	// the operator a document the template flow would reject.
	if _, err := CompileExisting(ctx, encoded); err != nil {
		return Adopted{}, fmt.Errorf("converted document failed compilation: %w", err)
	}
	result.Compose = string(encoded)
	result.Services = slices.Sorted(maps.Keys(a.doc.Services))
	result.Volumes = externalNames(a.doc.Volumes)
	result.Networks = externalNames(a.doc.Networks)
	result.VolumeBindings = adoptedBindings(a.doc.Volumes)
	result.NetworkBindings = adoptedBindings(a.doc.Networks)
	for _, name := range result.Services {
		for index, port := range a.doc.Services[name].Ports {
			if _, err := parseTemplatePort(port); err == nil {
				result.Ports = append(result.Ports, TemplatePort{Service: name, Port: index, Value: port})
			}
		}
	}
	return result, nil
}

func adoptedBindings(resources map[string]resource) map[string]string {
	bindings := make(map[string]string, len(resources))
	for key, entry := range resources {
		if entry.External {
			bindings[key] = entry.Name
		}
	}
	return bindings
}

type externalResource struct {
	name     string
	external bool
	broken   bool
}

func parseResources(node yaml.Node) (map[string]externalResource, error) {
	out := map[string]externalResource{}
	if node.Kind == 0 || node.Tag == "!!null" {
		return out, nil
	}
	var raw map[string]yaml.Node
	if err := node.Decode(&raw); err != nil {
		return out, err
	}
	for key, item := range raw {
		var resource externalResource
		switch {
		case item.Tag == "!!null":
		case item.Kind == yaml.MappingNode:
			fields := map[string]yaml.Node{}
			if err := item.Decode(&fields); err != nil {
				resource.broken = true
				break
			}
			external := fields["external"]
			if external.Kind != 0 && external.Tag != "!!null" {
				if external.Kind == yaml.MappingNode {
					resource.external = true
					var nested struct {
						Name string `yaml:"name"`
					}
					if err := external.Decode(&nested); err != nil || nested.Name == "" {
						resource.broken = true
						break
					}
					resource.name = nested.Name
				} else if external.Tag != "!!bool" || external.Decode(&resource.external) != nil {
					resource.broken = true
				}
			}
			explicitNode := fields["name"]
			if explicitNode.Kind != 0 && explicitNode.Tag != "!!null" {
				var explicit string
				if err := explicitNode.Decode(&explicit); err != nil || explicit == "" {
					resource.broken = true
					break
				}
				resource.name = explicit
			}
		default:
			resource.broken = true
		}
		out[key] = resource
	}
	return out, nil
}

type adopter struct {
	input    AdoptInput
	volumes  map[string]externalResource
	networks map[string]externalResource
	doc      compose
	issues   []AdoptIssue
	noted    map[string]bool
}

func (a *adopter) issue(service, format string, args ...any) {
	a.issues = append(a.issues, AdoptIssue{Service: service, Message: fmt.Sprintf(format, args...)})
}

func (a *adopter) convertService(name string, raw map[string]yaml.Node) {
	if unknown := slices.Sorted(maps.Keys(unknownKeys(raw))); len(unknown) > 0 {
		a.issue(name, "未纳管：%s 等配置项在受管模型之外，服务已跳过。", strings.Join(quoted(unknown), "、"))
		return
	}
	if key := text(raw["container_name"]); key != "" {
		a.issue(name, "已忽略 container_name（纳管期间新旧项目并行运行，需要避免容器重名）。")
	}
	if depends := raw["depends_on"]; depends.Kind != 0 && depends.Tag != "!!null" {
		a.issue(name, "已忽略 depends_on：纳管后各服务并发启动，不保证启动顺序。")
	}
	if !serviceName.MatchString(name) {
		a.issue(name, "未纳管：服务名不符合受管命名规则。")
		return
	}
	image := text(raw["image"])
	if image == "" {
		a.issue(name, "未纳管：缺少 image（build 等来源的镜像无法直接纳管）。")
		return
	}
	pin, err := pickPin(image, a.input.Pins[name])
	if err != nil {
		a.issue(name, "未纳管：%v。", err)
		return
	}
	var svc service
	svc.Image = pin
	if restart := raw["restart"]; restart.Kind != 0 {
		svc.Restart = text(restart)
	}
	switch command, err := parseCommand(raw["command"]); {
	case err != nil:
		a.issue(name, "未纳管：%v。", err)
		return
	case command != nil:
		svc.Command = &command
	}
	if environment := raw["environment"]; environment.Kind != 0 && environment.Tag != "!!null" {
		if svc.Environment, err = a.parseKeyValues(name, "环境变量", environment, true); err != nil {
			return
		}
	}
	if labels := raw["labels"]; labels.Kind != 0 && labels.Tag != "!!null" {
		if svc.Labels, err = a.parseKeyValues(name, "标签", labels, false); err != nil {
			return
		}
	}
	if svc.Ports, err = a.parsePorts(name, raw["ports"]); err != nil {
		return
	}
	if svc.Volumes, err = a.parseMounts(name, raw["volumes"]); err != nil {
		return
	}
	svc.Networks, err = a.parseServiceNetworks(name, raw["networks"])
	if err != nil {
		return
	}
	if check := raw["healthcheck"]; check.Kind != 0 && check.Tag != "!!null" {
		svc.Healthcheck = a.parseHealthcheck(name, check)
	}
	if err := validateServiceSettings(svc); err != nil {
		a.issue(name, "未纳管：%v。", err)
		return
	}
	a.doc.Services[name] = svc
}

// unknownKeys lists the settings outside the managed model that blocked a
// service, so the operator knows what to move or restructure.
func unknownKeys(raw map[string]yaml.Node) map[string]bool {
	out := map[string]bool{}
	for key := range raw {
		if !serviceFields[key] && !noteFields[key] {
			out[key] = true
		}
	}
	return out
}

func quoted(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strconv.Quote(value)
	}
	return out
}

// dropBroken removes declarations the converter could not interpret so any
// referencing service reports it as undeclared, and notes them project-wide.
func dropBroken(resources map[string]externalResource, kind string, issues *[]AdoptIssue) {
	for key, resource := range resources {
		if resource.broken {
			delete(resources, key)
			*issues = append(*issues, AdoptIssue{Message: fmt.Sprintf("%s声明 %q 格式无法识别，引用它的服务将无法纳管。", kind, key)})
		}
	}
}

// declareResources turns every resource a converted service references into
// an external declaration bound to the runtime Docker name, so adoption
// reuses the project's existing volumes and networks instead of recreating
// them. Services without explicit networks share the project default.
func (a *adopter) declareResources() error {
	usedVolumes, usedNetworks := map[string]bool{}, map[string]bool{}
	defaultNeeded := false
	for _, svc := range a.doc.Services {
		if len(svc.Networks) == 0 {
			defaultNeeded = true
		}
		for _, mount := range svc.Volumes {
			usedVolumes[strings.SplitN(mount, ":", 2)[0]] = true
		}
		for _, key := range svc.Networks {
			usedNetworks[key] = true
		}
	}
	if defaultNeeded {
		usedNetworks["default"] = true
	}
	concreteName := func(key string, source externalResource) string {
		if source.name != "" {
			return source.name
		}
		if source.external {
			return key
		}
		return a.input.Project + "_" + key
	}
	for kind, used := range map[string]map[string]bool{"volume": usedVolumes, "network": usedNetworks} {
		declarations := map[string]resource{}
		seen := map[string]bool{}
		for key := range used {
			source := a.volumes[key]
			if kind == "network" {
				source = a.networks[key]
			}
			concrete := concreteName(key, source)
			if !dockerResourceName.MatchString(concrete) {
				return fmt.Errorf("运行时项目名 %q 生成的%s名称 %q 无效；请先调整项目名或声明。", a.input.Project, kind, concrete)
			}
			if seen[concrete] {
				return fmt.Errorf("多个%s声明指向同一个 Docker 名称 %q；请显式改名后再纳管。", kind, concrete)
			}
			seen[concrete] = true
			declarations[key] = resource{Name: concrete, External: true}
		}
		if len(declarations) > 0 {
			if kind == "volume" {
				a.doc.Volumes = declarations
			} else {
				a.doc.Networks = declarations
			}
		}
	}
	return nil
}

func text(node yaml.Node) string {
	if node.Kind == 0 || node.Tag == "!!null" {
		return ""
	}
	var value string
	if node.Decode(&value) != nil {
		return ""
	}
	return value
}

// pickPin selects the observed digest reference whose repository matches the
// service image, so adoption pins exactly the image the node is running.
func pickPin(image string, pins []string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", errors.New("镜像引用无效")
	}
	repo := reference.TrimNamed(named).Name()
	for _, pin := range slices.Sorted(slices.Values(pins)) {
		if !pinReference.MatchString(pin) {
			continue
		}
		pinned, err := reference.ParseNormalizedNamed(pin)
		if err != nil || reference.TrimNamed(pinned).Name() != repo {
			continue
		}
		return pin, nil
	}
	if len(pins) == 0 {
		return "", fmt.Errorf("节点快照没有 %q 的镜像摘要，无法按受管模型固定镜像", repo)
	}
	return "", fmt.Errorf("节点快照的镜像摘要均不匹配 %q", repo)
}

// parseCommand carries over list commands verbatim and shell-splits scalar
// commands the way Compose does before exec.
func parseCommand(node yaml.Node) ([]string, error) {
	if node.Kind == 0 || node.Tag == "!!null" {
		return nil, nil
	}
	var list []string
	if err := node.Decode(&list); err == nil && node.Kind == yaml.SequenceNode {
		for _, item := range list {
			if strings.ContainsRune(item, '$') {
				return nil, errors.New("command 含有变量插值，无法按原文纳管")
			}
		}
		return list, nil
	}
	var scalar string
	if node.Decode(&scalar) != nil || scalar == "" {
		return nil, errors.New("command 格式无法识别")
	}
	words, err := splitShellWords(scalar)
	if err != nil {
		return nil, fmt.Errorf("command 无法解析: %w", err)
	}
	for _, word := range words {
		if strings.ContainsRune(word, '$') {
			return nil, errors.New("command 含有变量插值，无法按原文纳管")
		}
	}
	return words, nil
}

// splitShellWords approximates the word splitting Compose applies to scalar
// commands: POSIX quotes plus backslash escapes outside and inside double
// quotes.
func splitShellWords(value string) ([]string, error) {
	var (
		words   []string
		current strings.Builder
		space   = func() {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		}
		quote      rune
		escaped    bool
		sawContent bool
	)
	for _, r := range value {
		switch {
		case escaped:
			if quote == '"' && !strings.ContainsRune(`\"$`+"`", r) {
				current.WriteRune('\\')
			}
			current.WriteRune(r)
			escaped, sawContent = false, true
		case r == '\\' && quote != '\'':
			escaped = true
			sawContent = true
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote, sawContent = r, true
		case quote != 0 || !strings.ContainsRune(" \t", r):
			current.WriteRune(r)
			sawContent = true
		default:
			space()
			sawContent = false
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("存在未闭合的引号或反斜杠")
	}
	if current.Len() > 0 || sawContent {
		space()
	}
	return words, nil
}

// parseKeyValues reads map or key=value list forms. Environment values may
// be inherited (null or a bare key); labels never are. Values containing
// interpolation are dropped with a note because the managed model forbids
// all source interpolation.
func (a *adopter) parseKeyValues(name, kind string, node yaml.Node, environment bool) (composeKeyValues, error) {
	values := composeKeyValues{}
	add := func(key, value string) {
		if strings.ContainsRune(value, '$') {
			a.issue(name, "%s %q 含有变量插值，已忽略该值。", kind, key)
			return
		}
		if environment && !environmentName.MatchString(key) {
			a.issue(name, "%s %q 的名称不符合受管命名规则，已忽略。", kind, key)
			return
		}
		if !environment && (len(key) == 0 || len(key) > 255 || strings.ContainsAny(key, " =\t\r\n\x00")) {
			a.issue(name, "%s %q 的名称无效，已忽略。", kind, key)
			return
		}
		if _, duplicate := values[key]; !duplicate {
			values[key] = value
		}
	}
	dropInherited := func(key string) {
		a.issue(name, "%s %q 继承外部环境，值未知，已忽略；请在纳管前补上具体值。", kind, key)
	}
	switch {
	case node.Kind == yaml.MappingNode:
		var pairs map[string]yaml.Node
		if node.Decode(&pairs) != nil {
			a.issue(name, "%s 格式无法识别，已整体忽略。", kind)
			return composeKeyValues{}, nil
		}
		for _, key := range slices.Sorted(maps.Keys(pairs)) {
			valueNode := pairs[key]
			if valueNode.Tag == "!!null" {
				dropInherited(key)
				continue
			}
			var value string
			if valueNode.Decode(&value) != nil {
				a.issue(name, "%s %q 的值无法识别，已忽略。", kind, key)
				continue
			}
			add(key, value)
		}
	case node.Kind == yaml.SequenceNode:
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				a.issue(name, "%s 列表中存在无法识别的条目，已忽略。", kind)
				continue
			}
			key, value, assigned := strings.Cut(item.Value, "=")
			if !assigned {
				dropInherited(item.Value)
				continue
			}
			add(strings.TrimSpace(key), value)
		}
	default:
		a.issue(name, "%s 格式无法识别，已整体忽略。", kind)
	}
	if !environment {
		var reserved []string
		for key := range values {
			if strings.HasPrefix(key, "com.docker.compose.") {
				reserved = append(reserved, key)
				delete(values, key)
			}
		}
		if len(reserved) > 0 {
			a.issue(name, "已忽略 %d 个 Compose 内部标签（%s）。", len(reserved), strings.Join(reserved, ", "))
		}
	}
	return values, nil
}

// parsePorts carries short mappings over verbatim and rewrites long mappings
// into short form; host-mode publishing and port ranges cannot be carried
// over and are dropped with a note.
func (a *adopter) parsePorts(name string, node yaml.Node) ([]string, error) {
	if node.Kind == 0 || node.Tag == "!!null" {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		a.issue(name, "端口格式无法识别，已整体忽略。")
		return nil, nil
	}
	var ports []string
	for _, item := range node.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			var value string
			if item.Decode(&value) != nil || value == "" {
				a.issue(name, "存在无法识别的端口映射，已忽略。")
				continue
			}
			if strings.ContainsRune(value, '$') {
				a.issue(name, "端口映射 %q 含有变量插值，已忽略。", value)
				continue
			}
			if host, _, ok := strings.Cut(value, "-"); ok && strings.ContainsAny(host, "0123456789") && strings.Contains(value, ":") {
				a.issue(name, "端口范围映射 %q 无法按受管模型固定，已忽略。", value)
				continue
			}
			ports = append(ports, value)
		case yaml.MappingNode:
			var fields map[string]yaml.Node
			if item.Decode(&fields) != nil {
				a.issue(name, "存在无法识别的端口映射，已忽略。")
				continue
			}
			target, published, hostIP := text(fields["target"]), text(fields["published"]), text(fields["host_ip"])
			protocol, mode := text(fields["protocol"]), text(fields["mode"])
			if target == "" {
				a.issue(name, "存在缺少容器端口的映射，已忽略。")
				continue
			}
			if mode != "" && mode != "ingress" {
				a.issue(name, "容器端口 %s 以 host 模式发布，受管模型不支持，已忽略该映射。", target)
				continue
			}
			if !slices.Contains([]string{"", "tcp", "udp", "sctp"}, protocol) {
				a.issue(name, "容器端口 %s 的协议 %q 不受支持，已忽略该映射。", target, protocol)
				continue
			}
			value := ""
			if hostIP != "" {
				value = hostIP + ":"
			}
			if published != "" {
				if strings.ContainsAny(published, "-") {
					a.issue(name, "端口 %s 的发布范围为区间，已忽略该映射。", target)
					continue
				}
				value += published + ":" + target
			} else if value != "" {
				value += ":" + target
			} else {
				value = target
			}
			if protocol != "" {
				value += "/" + protocol
			}
			ports = append(ports, value)
		default:
			a.issue(name, "存在无法识别的端口映射，已忽略。")
		}
	}
	return ports, nil
}

// parseMounts carries named-volume mounts over; bind mounts and anonymous
// volumes cannot be represented by the managed model and block the service,
// because silently dropping either would change what the service stores.
func (a *adopter) parseMounts(name string, node yaml.Node) ([]string, error) {
	if node.Kind == 0 || node.Tag == "!!null" {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		a.issue(name, "卷挂载格式无法识别，已整体忽略。")
		return nil, nil
	}
	var mounts []string
	targets := map[string]bool{}
	bind := func(source, destination string) error {
		a.issue(name, "未纳管：挂载 %s:%s 是绑定挂载或匿名卷，受管模型只支持具名卷；请把该内容迁入具名卷后再纳管。", source, destination)
		return errStopService
	}
	for _, item := range node.Content {
		var source, destination, mode string
		switch item.Kind {
		case yaml.ScalarNode:
			var value string
			if item.Decode(&value) != nil {
				a.issue(name, "存在无法识别的卷挂载，已忽略。")
				continue
			}
			parts := strings.Split(value, ":")
			if len(parts) == 1 {
				if err := bind("匿名卷", value); err != nil {
					return nil, err
				}
			}
			if len(parts) < 2 || len(parts) > 3 {
				a.issue(name, "卷挂载 %q 格式无法识别，已忽略。", value)
				continue
			}
			source, destination = parts[0], parts[1]
			if len(parts) == 3 {
				mode = parts[2]
			}
		case yaml.MappingNode:
			var fields map[string]yaml.Node
			if item.Decode(&fields) != nil {
				a.issue(name, "存在无法识别的卷挂载，已忽略。")
				continue
			}
			kind := text(fields["type"])
			source, destination = text(fields["source"]), text(fields["target"])
			readOnly := fields["read_only"]
			if readOnly.Tag == "!!bool" {
				var flag bool
				_ = readOnly.Decode(&flag)
				if flag {
					mode = "ro"
				}
			}
			if kind == "bind" {
				if err := bind(source, destination); err != nil {
					return nil, err
				}
			}
			if kind != "" && kind != "volume" {
				a.issue(name, "卷挂载类型 %q 不受支持，已忽略。", kind)
				continue
			}
		default:
			a.issue(name, "存在无法识别的卷挂载，已忽略。")
			continue
		}
		if source == "" {
			if err := bind("匿名卷", destination); err != nil {
				return nil, err
			}
		}
		if strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~") {
			if err := bind(source, destination); err != nil {
				return nil, err
			}
		}
		if _, declared := a.volumes[source]; !declared {
			a.issue(name, "未纳管：挂载引用了未声明的卷 %q。", source)
			return nil, errStopService
		}
		if mode != "" && mode != "ro" && mode != "rw" {
			a.issue(name, "卷挂载 %s:%s 的模式 %q 不受支持，已忽略该挂载。", source, destination, mode)
			continue
		}
		mount := source + ":" + destination
		if mode == "ro" {
			mount += ":ro"
		}
		if !a.validMountTarget(name, destination, targets) {
			return nil, errStopService
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

var errStopService = errors.New("stop service")

func (a *adopter) validMountTarget(service, target string, seen map[string]bool) bool {
	if !path.IsAbs(target) || target == "/" || path.Clean(target) != target ||
		strings.ContainsAny(target, "\\\x00\r\n$") || seen[target] {
		a.issue(service, "未纳管：卷挂载目标 %q 不是唯一且规范的绝对路径。", target)
		return false
	}
	seen[target] = true
	return true
}

// parseServiceNetworks carries the declared networks a service attaches to;
// per-service settings such as aliases or static addresses are dropped with
// a note.
func (a *adopter) parseServiceNetworks(name string, node yaml.Node) ([]string, error) {
	if node.Kind == 0 || node.Tag == "!!null" {
		return nil, nil
	}
	note := func() {
		if !a.noted[name] {
			a.noted[name] = true
			a.issue(name, "已忽略网络的自定义设置（别名、静态地址等），服务仍加入同一网络。")
		}
	}
	var keys []string
	switch node.Kind {
	case yaml.SequenceNode:
		for _, item := range node.Content {
			var key string
			if item.Decode(&key) != nil || key == "" {
				a.issue(name, "存在无法识别的网络引用，已忽略。")
				continue
			}
			keys = append(keys, key)
		}
	case yaml.MappingNode:
		var fields map[string]yaml.Node
		if node.Decode(&fields) != nil {
			a.issue(name, "网络引用格式无法识别，已整体忽略。")
			return nil, nil
		}
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			if fields[key].Kind == yaml.MappingNode {
				note()
			}
			keys = append(keys, key)
		}
	default:
		a.issue(name, "网络引用格式无法识别，已整体忽略。")
		return nil, nil
	}
	for _, key := range keys {
		if _, declared := a.networks[key]; !declared {
			a.issue(name, "未纳管：服务引用了未声明的网络 %q。", key)
			return nil, errStopService
		}
	}
	return keys, nil
}

// parseHealthcheck carries the healthcheck over when it maps onto the
// managed subset; an unsupported form drops the healthcheck (the image
// default applies) rather than the service.
func (a *adopter) parseHealthcheck(name string, node yaml.Node) *healthcheck {
	var fields map[string]yaml.Node
	if node.Decode(&fields) != nil {
		a.issue(name, "healthcheck 格式无法识别，已忽略。")
		return nil
	}
	disable := fields["disable"]
	if disable.Tag == "!!bool" {
		var disabled bool
		_ = disable.Decode(&disabled)
		if disabled {
			a.issue(name, "已忽略 healthcheck disable：纳管后沿用镜像自带健康检查。")
			return nil
		}
	}
	test := fields["test"]
	if test.Kind == 0 || test.Tag == "!!null" {
		a.issue(name, "healthcheck 缺少 test，已忽略。")
		return nil
	}
	var commands []string
	if test.Kind == yaml.ScalarNode {
		var scalar string
		if test.Decode(&scalar) != nil || scalar == "" {
			a.issue(name, "healthcheck test 格式无法识别，已忽略。")
			return nil
		}
		commands = []string{"CMD-SHELL", scalar}
	} else {
		if test.Decode(&commands) != nil || len(commands) == 0 {
			a.issue(name, "healthcheck test 格式无法识别，已忽略。")
			return nil
		}
		switch commands[0] {
		case "CMD", "CMD-SHELL":
		case "NONE":
			a.issue(name, "healthcheck NONE 无需保留，已忽略。")
			return nil
		default:
			commands = append([]string{"CMD-SHELL"}, strings.Join(commands, " "))
		}
	}
	result := &healthcheck{Test: commands}
	for key, target := range map[string]**string{
		"interval": &result.Interval, "timeout": &result.Timeout,
		"start_period": &result.StartPeriod, "start_interval": &result.StartInterval,
	} {
		duration := fields[key]
		if duration.Kind == 0 || duration.Tag == "!!null" {
			continue
		}
		var value string
		if duration.Decode(&value) != nil || value == "" {
			a.issue(name, "healthcheck %s 格式无法识别，已忽略整个健康检查。", key)
			return nil
		}
		*target = &value
	}
	retriesNode := fields["retries"]
	if retriesNode.Kind != 0 && retriesNode.Tag != "!!null" {
		var retries int
		if retriesNode.Decode(&retries) != nil {
			a.issue(name, "healthcheck retries 格式无法识别，已忽略整个健康检查。")
			return nil
		}
		result.Retries = &retries
	}
	if err := validateHealthcheck(result); err != nil {
		a.issue(name, "healthcheck 无法按受管模型保留（%v），已忽略。", err)
		return nil
	}
	return result
}
