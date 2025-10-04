package metadata

import (
	"fmt"
	"strings"
)

// NodeConfig represents the configuration for a single node
type NodeConfig struct {
	NodeID       string                 `json:"node_id"`
	NodeIndex    int                    `json:"node_index"`
	TotalNodes   int                    `json:"total_nodes"`
	DeploymentID string                 `json:"deployment_id"`
	Config       map[string]interface{} `json:"config"`
}

// NodesConfig represents the enhanced nodes configuration
type NodesConfig struct {
	Count            int                      `yaml:"count"`
	GlobalMetadata   map[string]interface{}   `yaml:"global_metadata"`
	DistributedLists map[string][]interface{} `yaml:"distributed_lists"`
	ConfigTemplate   map[string]interface{}   `yaml:"config_template"`
}

// GenerateNodeConfigs creates individual configurations for each node
func GenerateNodeConfigs(nodesConfig NodesConfig, deploymentID string) ([]NodeConfig, error) {
	if err := ValidateNodesConfig(nodesConfig); err != nil {
		return nil, err
	}

	nodeConfigs := make([]NodeConfig, nodesConfig.Count)

	for i := 0; i < nodesConfig.Count; i++ {
		// Create base node config with deployment-scoped node ID
		nodeConfig := NodeConfig{
			NodeID:       fmt.Sprintf("%s_node_%d", deploymentID, i),
			NodeIndex:    i,
			TotalNodes:   nodesConfig.Count,
			DeploymentID: deploymentID,
			Config:       make(map[string]interface{}),
		}

		// Copy global metadata
		for key, value := range nodesConfig.GlobalMetadata {
			nodeConfig.Config[key] = value
		}

		// Distribute list items to this node in round-robin fashion
		for listName, listItems := range nodesConfig.DistributedLists {
			if len(listItems) == 0 {
				continue
			}

			// Collect all items that should go to this node (round-robin)
			var nodeItems []interface{}
			for itemIndex := i; itemIndex < len(listItems); itemIndex += nodesConfig.Count {
				item := listItems[itemIndex]

				// Only allow simple types (strings, numbers, booleans)
				switch item.(type) {
				case string, int, int64, float64, bool:
					nodeItems = append(nodeItems, item)
				default:
					return nil, fmt.Errorf("distributed list '%s' contains complex type %T - only simple types (string, int, float, bool) are supported", listName, item)
				}
			}

			// Always store as array for consistency
			if len(nodeItems) > 0 {
				nodeConfig.Config[listName] = nodeItems
			}
		}

		// Apply template configuration with simple replacements
		// Process this after global metadata and distributed lists are set
		for key, value := range nodesConfig.ConfigTemplate {
			processedValue := processSimpleTemplate(value, nodeConfig)
			nodeConfig.Config[key] = processedValue
		}

		nodeConfigs[i] = nodeConfig
	}

	return nodeConfigs, nil
}

// processSimpleTemplate handles simple string replacement for template values
func processSimpleTemplate(value interface{}, nodeConfig NodeConfig) interface{} {
	switch v := value.(type) {
	case string:
		// Simple string replacements
		result := v
		result = strings.ReplaceAll(result, "{node_id}", nodeConfig.NodeID)
		result = strings.ReplaceAll(result, "{node_index}", fmt.Sprintf("%d", nodeConfig.NodeIndex))
		result = strings.ReplaceAll(result, "{total_nodes}", fmt.Sprintf("%d", nodeConfig.TotalNodes))
		result = strings.ReplaceAll(result, "{deployment_id}", nodeConfig.DeploymentID)

		// Replace references to global metadata and distributed lists
		// If the entire string is just a placeholder, return the actual value (not stringified)
		for key, val := range nodeConfig.Config {
			placeholder := fmt.Sprintf("{%s}", key)

			// If the entire string is just this placeholder, return the raw value
			if result == placeholder {
				return val
			}

			// Otherwise do string replacement for simple types embedded in strings
			switch v := val.(type) {
			case string:
				result = strings.ReplaceAll(result, placeholder, v)
			case int, int64:
				result = strings.ReplaceAll(result, placeholder, fmt.Sprintf("%d", v))
			case float64:
				result = strings.ReplaceAll(result, placeholder, fmt.Sprintf("%g", v))
			case bool:
				result = strings.ReplaceAll(result, placeholder, fmt.Sprintf("%t", v))
			}
		}

		return result
	case map[string]interface{}:
		// Recursively process maps
		result := make(map[string]interface{})
		for k, val := range v {
			result[k] = processSimpleTemplate(val, nodeConfig)
		}
		return result
	case []interface{}:
		// Recursively process slices
		result := make([]interface{}, len(v))
		for i, val := range v {
			result[i] = processSimpleTemplate(val, nodeConfig)
		}
		return result
	default:
		// Return value as-is for non-templatable types
		return value
	}
}

// ValidateNodesConfig validates the nodes configuration
func ValidateNodesConfig(config NodesConfig) error {
	if config.Count <= 0 {
		return fmt.Errorf("nodes count must be greater than 0")
	}

	// Validate that distributed lists have appropriate lengths
	for listName, listItems := range config.DistributedLists {
		if len(listItems) == 0 {
			return fmt.Errorf("distributed list '%s' cannot be empty", listName)
		}
	}

	return nil
}
