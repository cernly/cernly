// Package yamlnode holds the small helpers the plugins share for reading
// parsed YAML nodes.
package yamlnode

import "gopkg.in/yaml.v3"

// Get follows keys through nested mappings and returns the value node, or nil
// when a step is not a mapping or the key is absent.
func Get(n *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == k {
				next = n.Content[i+1]
				break
			}
		}
		n = next
	}
	return n
}

// LastLine returns the highest line number in the node's subtree.
func LastLine(n *yaml.Node) int {
	last := n.Line
	for _, c := range n.Content {
		last = max(last, LastLine(c))
	}
	return last
}
