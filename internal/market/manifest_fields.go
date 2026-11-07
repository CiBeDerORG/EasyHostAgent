package market

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// 配置定义必须来自已逐文件校验的候选包，不能在运行时重新读取 Git。
func resolveManifestFields(m *manifest, files map[string][]byte) error {
	if m.Version == 0 || m.Version == 1 {
		return nil
	}
	if m.Version != 2 || m.Configuration.Config != "config.yaml" || m.Configuration.Secrets != "secrets.yaml" || len(m.Config) != 0 {
		return errors.New("不支持的应用配置契约")
	}
	seen := map[string]bool{}
	for _, definition := range []struct{ name, target string }{{"config.yaml", "env"}, {"secrets.yaml", "secrets"}} {
		raw := files[m.ID+"/"+definition.name]
		if len(raw) == 0 || len(raw) > 128*1024 {
			return errors.New("配置定义缺失或超过大小限制")
		}
		var root yaml.Node
		if err := yaml.Unmarshal(raw, &root); err != nil {
			return errors.New("配置定义格式无效")
		}
		var check func(*yaml.Node) bool
		check = func(n *yaml.Node) bool {
			if n.Kind == yaml.AliasNode {
				return false
			}
			if n.Kind == yaml.MappingNode {
				keys := map[string]bool{}
				for i := 0; i < len(n.Content); i += 2 {
					k := n.Content[i]
					if k.Tag != "!!str" || k.Value == "<<" || keys[k.Value] {
						return false
					}
					keys[k.Value] = true
				}
			}
			for _, child := range n.Content {
				if !check(child) {
					return false
				}
			}
			return true
		}
		if !check(&root) {
			return errors.New("配置定义包含重复键或 YAML 引用")
		}
		var shape struct{ Fields []map[string]any }
		if err := root.Decode(&shape); err != nil || shape.Fields == nil {
			return errors.New("配置定义必须包含 fields 数组")
		}
		var data struct{ Fields []field }
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&data); err != nil {
			return errors.New("配置定义字段无效")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("配置定义必须只有一个文档")
		}
		for index, f := range data.Fields {
			attrs := shape.Fields[index]
			if _, ok := attrs["required"].(bool); !ok {
				return errors.New("字段必须声明是否必填")
			}
			if _, ok := attrs["description"].(string); !ok {
				return errors.New("字段必须声明说明")
			}
			if _, ok := attrs["label"].(string); !ok {
				return errors.New("字段必须声明名称")
			}
			if _, exists := attrs["target"]; exists {
				return errors.New("存储目标由定义文件确定")
			}
			validDefault := false
			switch f.Type {
			case "port", "number":
				_, validDefault = attrs["default"].(int)
			case "boolean":
				_, validDefault = attrs["default"].(bool)
			case "string", "ipv4", "secret":
				_, validDefault = attrs["default"].(string)
			}
			if !validDefault {
				return errors.New("字段类型或默认值类型无效")
			}
			if f.Target != "" || f.CustomInput == nil || f.Default == nil || seen[f.Key] || !fieldKey.MatchString(f.Key) || (f.Type == "secret") != (definition.target == "secrets") {
				return errors.New("配置定义重复、属性缺失或存储类型不匹配")
			}
			if f.Type == "secret" && *f.Default != "" {
				return errors.New("密钥定义不能包含真实默认值")
			}
			if f.GeneratedByScript && f.Type != "secret" {
				return errors.New("自动生成仅适用于密钥")
			}
			if f.Required && (!*f.CustomInput || (f.Editable != nil && !*f.Editable)) && *f.Default == "" && !f.GeneratedByScript {
				return errors.New("必填只读字段缺少默认值或生成机制")
			}
			f.Target = definition.target
			// 手工必填字段允许模板留空，由安装输入或已有配置满足。
			probe := f
			probe.Required = false
			if err := probe.validate(*f.Default); err != nil {
				return err
			}
			seen[f.Key] = true
			m.Config = append(m.Config, f)
		}
	}
	if len(m.Config) > 120 {
		return errors.New("配置字段数量超过上限")
	}
	return nil
}
