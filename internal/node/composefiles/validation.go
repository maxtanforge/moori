package composefiles

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

func validateDocuments(snapshot Snapshot) error {
	for _, file := range snapshot.Files {
		decoder := yaml.NewDecoder(strings.NewReader(file.Content))
		var document yaml.Node
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			continue
		} else if err != nil {
			return errors.New("Compose 源文件不是有效的 YAML")
		}
		if err := decoder.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
			return errors.New("Compose 源文件只允许一个 YAML 文档")
		}
		if err := checkReferences(&document); err != nil {
			return err
		}
	}
	return nil
}

func checkReferences(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		switch node.ShortTag() {
		case "!!str", "!!int", "!!bool", "!!float", "!!null", "!!timestamp", "!!merge":
		default:
			return errors.New("Compose 源文件包含不支持的 YAML 类型标签")
		}
		if hasInterpolation(node.Value) {
			return errors.New("Compose 源文件依赖环境变量插值，请填写明确值或用 $$ 保留字面量")
		}
	case yaml.MappingNode:
		if node.ShortTag() != "!!map" && node.ShortTag() != "!override" && node.ShortTag() != "!reset" {
			return errors.New("Compose 源文件包含不支持的 YAML 类型标签")
		}
	case yaml.SequenceNode:
		if node.ShortTag() != "!!seq" && node.ShortTag() != "!override" && node.ShortTag() != "!reset" {
			return errors.New("Compose 源文件包含不支持的 YAML 类型标签")
		}
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			for key.Kind == yaml.AliasNode && key.Alias != nil {
				key = key.Alias
			}
			if key.Kind != yaml.ScalarNode {
				return ErrInvalidSource
			}
			switch key.Value {
			case "include", "extends", "env_file", "label_file", "credential_spec", "provider", "develop", "file", "build":
				return fmt.Errorf("Compose 源文件使用了不支持的 %s 引用，请改用直接登记的源文件", key.Value)
			case "environment":
				if usesAmbientEnvironment(node.Content[i+1]) {
					return errors.New("Compose 环境变量必须填写明确值")
				}
			case "configs", "secrets":
				if hasConfigEnvironment(node.Content[i+1]) {
					return errors.New("Compose 配置或密钥不能从环境变量读取")
				}
			}
		}
	}
	for _, child := range node.Content {
		if err := checkReferences(child); err != nil {
			return err
		}
	}
	return nil
}

func hasInterpolation(value string) bool {
	for i := 0; i+1 < len(value); i++ {
		if value[i] != '$' {
			continue
		}
		next := value[i+1]
		if next == '$' {
			i++
		} else if next == '{' || next == '_' || next >= 'A' && next <= 'Z' || next >= 'a' && next <= 'z' {
			return true
		}
	}
	return false
}

func concrete(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return node.Alias
	}
	return node
}

func usesAmbientEnvironment(node *yaml.Node) bool {
	node = concrete(node)
	switch node.Kind {
	case yaml.MappingNode:
		for i := 1; i < len(node.Content); i += 2 {
			if concrete(node.Content[i-1]).Value == "<<" || concrete(node.Content[i]).ShortTag() == "!!null" {
				return true
			}
		}
	case yaml.SequenceNode:
		for _, value := range node.Content {
			if value = concrete(value); value.Kind == yaml.ScalarNode && !strings.Contains(value.Value, "=") {
				return true
			}
		}
	}
	return false
}

func hasConfigEnvironment(node *yaml.Node) bool {
	node = concrete(node)
	if node.Kind != yaml.MappingNode {
		return false
	}
	for i := 1; i < len(node.Content); i += 2 {
		if concrete(node.Content[i-1]).Value == "<<" {
			return true
		}
		contents := concrete(node.Content[i])
		if contents.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j < len(contents.Content); j += 2 {
			if key := concrete(contents.Content[j]); key.Value == "environment" || key.Value == "<<" {
				return true
			}
		}
	}
	return false
}
