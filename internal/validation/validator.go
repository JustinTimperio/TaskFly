package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"
)

// ValidationError represents a validation error
type ValidationError struct {
	Field    string
	Message  string
	Severity string // "error", "warning", "info"
}

func (e ValidationError) String() string {
	return fmt.Sprintf("[%s] %s: %s", e.Severity, e.Field, e.Message)
}

// ValidationResult contains the results of validation
type ValidationResult struct {
	Errors   []ValidationError
	Warnings []ValidationError
	Info     []ValidationError
	Valid    bool
}

// AddError adds an error to the validation result
func (r *ValidationResult) AddError(field, message string) {
	r.Errors = append(r.Errors, ValidationError{
		Field:    field,
		Message:  message,
		Severity: "error",
	})
	r.Valid = false
}

// AddWarning adds a warning to the validation result
func (r *ValidationResult) AddWarning(field, message string) {
	r.Warnings = append(r.Warnings, ValidationError{
		Field:    field,
		Message:  message,
		Severity: "warning",
	})
}

// AddInfo adds an info message to the validation result
func (r *ValidationResult) AddInfo(field, message string) {
	r.Info = append(r.Info, ValidationError{
		Field:    field,
		Message:  message,
		Severity: "info",
	})
}

// TaskFlyConfig represents the taskfly.yml configuration
type TaskFlyConfig struct {
	CloudProvider     string                            `yaml:"cloud_provider"`
	InstanceConfig    map[string]map[string]interface{} `yaml:"instance_config"`
	ApplicationFiles  []string                          `yaml:"application_files"`
	RemoteDestDir     string                            `yaml:"remote_dest_dir"`
	RemoteScriptToRun string                            `yaml:"remote_script_to_run"`
	BundleName        string                            `yaml:"bundle_name"`
	Nodes             NodesConfig                       `yaml:"nodes"`
}

// NodesConfig represents the nodes configuration
type NodesConfig struct {
	Count            int                      `yaml:"count"`
	GlobalMetadata   map[string]interface{}   `yaml:"global_metadata"`
	DistributedLists map[string][]interface{} `yaml:"distributed_lists"`
	ConfigTemplate   map[string]interface{}   `yaml:"config_template"`
}

// Validator validates TaskFly configuration
type Validator struct {
	config     *TaskFlyConfig
	configPath string
	result     *ValidationResult
}

// NewValidator creates a new validator
func NewValidator(configPath string) (*Validator, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config TaskFlyConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &Validator{
		config:     &config,
		configPath: configPath,
		result:     &ValidationResult{Valid: true},
	}, nil
}

// Validate runs all validation checks
func (v *Validator) Validate() *ValidationResult {
	v.validateCloudProvider()
	v.validateInstanceConfig()
	v.validateApplicationFiles()
	v.validateNodesConfig()
	v.validateRemoteConfig()
	v.checkCommonIssues()

	return v.result
}

// validateCloudProvider validates the cloud_provider field
func (v *Validator) validateCloudProvider() {
	if v.config.CloudProvider == "" {
		v.result.AddError("cloud_provider", "cloud_provider is required")
		return
	}

	supportedProviders := []string{"aws", "local"}
	found := false
	for _, p := range supportedProviders {
		if v.config.CloudProvider == p {
			found = true
			break
		}
	}

	if !found {
		v.result.AddError("cloud_provider",
			fmt.Sprintf("unsupported cloud provider '%s'. Supported: %s",
				v.config.CloudProvider, strings.Join(supportedProviders, ", ")))
	}
}

// validateInstanceConfig validates the instance_config section
func (v *Validator) validateInstanceConfig() {
	if v.config.InstanceConfig == nil || len(v.config.InstanceConfig) == 0 {
		v.result.AddError("instance_config", "instance_config is required")
		return
	}

	providerConfig, ok := v.config.InstanceConfig[v.config.CloudProvider]
	if !ok {
		v.result.AddError("instance_config",
			fmt.Sprintf("no configuration found for provider '%s'", v.config.CloudProvider))
		return
	}

	// Provider-specific validation
	switch v.config.CloudProvider {
	case "aws":
		v.validateAWSConfig(providerConfig)
	case "local":
		v.validateLocalConfig(providerConfig)
	}
}

// validateAWSConfig validates AWS-specific configuration
func (v *Validator) validateAWSConfig(config map[string]interface{}) {
	// Required fields
	requiredFields := []string{"image_id", "instance_type", "key_name"}
	for _, field := range requiredFields {
		if val, ok := config[field]; !ok || val == "" {
			v.result.AddError(fmt.Sprintf("instance_config.aws.%s", field),
				fmt.Sprintf("%s is required for AWS provider", field))
		}
	}

	// Check AMI format
	if imageID, ok := config["image_id"].(string); ok && imageID != "" {
		if !strings.HasPrefix(imageID, "ami-") {
			v.result.AddWarning("instance_config.aws.image_id",
				"image_id should start with 'ami-' for AWS AMIs")
		}
	}

	// Validate region if present
	if region, ok := config["region"].(string); ok && region != "" {
		validRegions := []string{"us-east-1", "us-east-2", "us-west-1", "us-west-2",
			"eu-west-1", "eu-central-1", "ap-southeast-1", "ap-northeast-1"}
		found := false
		for _, r := range validRegions {
			if region == r {
				found = true
				break
			}
		}
		if !found {
			v.result.AddWarning("instance_config.aws.region",
				fmt.Sprintf("uncommon AWS region '%s', verify this is correct", region))
		}
	}

	// Check security groups
	if sg, ok := config["security_groups"]; ok {
		if sgSlice, ok := sg.([]interface{}); ok {
			if len(sgSlice) == 0 {
				v.result.AddWarning("instance_config.aws.security_groups",
					"no security groups specified, instances may not be accessible")
			}
		}
	}

	// Check SSH key path
	if keyName, ok := config["key_name"].(string); ok && keyName != "" {
		v.result.AddInfo("instance_config.aws.key_name",
			fmt.Sprintf("ensure AWS key pair '%s' exists in your AWS account", keyName))
	}

	// Check SSH key path if provided
	if sshKeyPath, ok := config["ssh_key_path"].(string); ok && sshKeyPath != "" {
		v.validateSSHKeyPath(sshKeyPath)
	}

	// Check SSH user
	if _, ok := config["ssh_user"]; !ok {
		v.result.AddWarning("instance_config.aws.ssh_user",
			"ssh_user not specified, defaulting to 'ubuntu' (may vary by AMI)")
	}
}

// validateLocalConfig validates local provider configuration
func (v *Validator) validateLocalConfig(config map[string]interface{}) {
	// Check for host or hosts
	hasHost := false
	hasSingleHost, ok1 := config["host"].(string)
	hasHostsArray, ok2 := config["hosts"].([]interface{})

	if ok1 && hasSingleHost != "" {
		hasHost = true
	}
	if ok2 && len(hasHostsArray) > 0 {
		hasHost = true
	}

	if !hasHost {
		v.result.AddError("instance_config.local.host",
			"either 'host' or 'hosts' array is required for local provider")
	}

	// Validate hosts array matches node count
	if ok2 && len(hasHostsArray) > 0 {
		if v.config.Nodes.Count > len(hasHostsArray) {
			v.result.AddError("instance_config.local.hosts",
				fmt.Sprintf("hosts array has %d entries but nodes.count is %d (need at least %d hosts)",
					len(hasHostsArray), v.config.Nodes.Count, v.config.Nodes.Count))
		} else if v.config.Nodes.Count < len(hasHostsArray) {
			v.result.AddWarning("instance_config.local.hosts",
				fmt.Sprintf("hosts array has %d entries but only %d will be used (nodes.count=%d)",
					len(hasHostsArray), v.config.Nodes.Count, v.config.Nodes.Count))
		}

		// Check for duplicate hosts
		hostMap := make(map[string]bool)
		for i, h := range hasHostsArray {
			if hostStr, ok := h.(string); ok {
				if hostMap[hostStr] {
					v.result.AddWarning("instance_config.local.hosts",
						fmt.Sprintf("duplicate host '%s' at index %d", hostStr, i))
				}
				hostMap[hostStr] = true
			}
		}
	}

	// Required fields for local provider
	if _, ok := config["ssh_user"]; !ok {
		v.result.AddError("instance_config.local.ssh_user",
			"ssh_user is required for local provider")
	}

	if sshKeyPath, ok := config["ssh_key_path"].(string); !ok || sshKeyPath == "" {
		v.result.AddError("instance_config.local.ssh_key_path",
			"ssh_key_path is required for local provider")
	} else {
		v.validateSSHKeyPath(sshKeyPath)
	}

	// Check target OS/arch if specified
	if targetOS, ok := config["target_os"].(string); ok && targetOS != "" {
		validOS := []string{"linux", "darwin", "windows"}
		found := false
		for _, os := range validOS {
			if targetOS == os {
				found = true
				break
			}
		}
		if !found {
			v.result.AddWarning("instance_config.local.target_os",
				fmt.Sprintf("uncommon target_os '%s', supported: %s", targetOS, strings.Join(validOS, ", ")))
		}
	}

	if targetArch, ok := config["target_arch"].(string); ok && targetArch != "" {
		validArch := []string{"amd64", "arm64"}
		found := false
		for _, arch := range validArch {
			if targetArch == arch {
				found = true
				break
			}
		}
		if !found {
			v.result.AddWarning("instance_config.local.target_arch",
				fmt.Sprintf("uncommon target_arch '%s', supported: %s", targetArch, strings.Join(validArch, ", ")))
		}
	}
}

// validateSSHKeyPath validates that SSH key path exists
func (v *Validator) validateSSHKeyPath(keyPath string) {
	// Expand home directory
	if strings.HasPrefix(keyPath, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			v.result.AddWarning("ssh_key_path",
				"could not verify SSH key path (unable to determine home directory)")
			return
		}
		keyPath = filepath.Join(homeDir, keyPath[2:])
	}

	// Check if file exists
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		v.result.AddError("ssh_key_path",
			fmt.Sprintf("SSH key file does not exist: %s", keyPath))
	} else if err != nil {
		v.result.AddWarning("ssh_key_path",
			fmt.Sprintf("could not verify SSH key file: %v", err))
	} else {
		// Check permissions (should be 600 or 400)
		info, _ := os.Stat(keyPath)
		mode := info.Mode().Perm()
		if mode != 0600 && mode != 0400 {
			v.result.AddWarning("ssh_key_path",
				fmt.Sprintf("SSH key has permissions %o, should be 0600 or 0400 for security", mode))
		}
	}
}

// validateApplicationFiles validates that application files exist
func (v *Validator) validateApplicationFiles() {
	if len(v.config.ApplicationFiles) == 0 {
		v.result.AddWarning("application_files",
			"no application files specified, bundle will only contain taskfly.yml")
		return
	}

	configDir := filepath.Dir(v.configPath)

	for _, file := range v.config.ApplicationFiles {
		fullPath := filepath.Join(configDir, file)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			v.result.AddError("application_files",
				fmt.Sprintf("file does not exist: %s", file))
		} else if err != nil {
			v.result.AddWarning("application_files",
				fmt.Sprintf("could not verify file: %s (%v)", file, err))
		}
	}

	// Check if remote_script_to_run is in application_files
	if v.config.RemoteScriptToRun != "" {
		found := false
		for _, file := range v.config.ApplicationFiles {
			if file == v.config.RemoteScriptToRun {
				found = true
				break
			}
		}
		if !found {
			v.result.AddError("remote_script_to_run",
				fmt.Sprintf("script '%s' not found in application_files", v.config.RemoteScriptToRun))
		}
	}
}

// validateNodesConfig validates the nodes configuration
func (v *Validator) validateNodesConfig() {
	if v.config.Nodes.Count <= 0 {
		v.result.AddError("nodes.count", "nodes.count must be greater than 0")
		return
	}

	if v.config.Nodes.Count > 1000 {
		v.result.AddWarning("nodes.count",
			fmt.Sprintf("deploying %d nodes may be expensive and slow", v.config.Nodes.Count))
	}

	// Validate distributed lists
	for listName, listValues := range v.config.Nodes.DistributedLists {
		if len(listValues) == 0 {
			v.result.AddWarning(fmt.Sprintf("nodes.distributed_lists.%s", listName),
				"distributed list is empty")
			continue
		}

		if len(listValues) < v.config.Nodes.Count {
			v.result.AddWarning(fmt.Sprintf("nodes.distributed_lists.%s", listName),
				fmt.Sprintf("list has %d items but %d nodes (items will be reused/cycled)",
					len(listValues), v.config.Nodes.Count))
		}

		// Check if list is referenced in config_template
		if v.config.Nodes.ConfigTemplate != nil {
			referenced := v.isListReferenced(listName, v.config.Nodes.ConfigTemplate)
			if !referenced {
				v.result.AddWarning(fmt.Sprintf("nodes.distributed_lists.%s", listName),
					fmt.Sprintf("list '%s' is not referenced in config_template", listName))
			}
		}
	}

	// Check for undefined template variables
	if v.config.Nodes.ConfigTemplate != nil {
		v.validateTemplateVariables(v.config.Nodes.ConfigTemplate)
	}
}

// isListReferenced checks if a distributed list is referenced in the config template
func (v *Validator) isListReferenced(listName string, template map[string]interface{}) bool {
	searchStr := fmt.Sprintf("{%s}", listName)
	return v.containsString(template, searchStr)
}

// containsString recursively searches for a string in a map
func (v *Validator) containsString(data map[string]interface{}, search string) bool {
	for _, value := range data {
		switch val := value.(type) {
		case string:
			if strings.Contains(val, search) {
				return true
			}
		case map[string]interface{}:
			if v.containsString(val, search) {
				return true
			}
		}
	}
	return false
}

// validateTemplateVariables checks for undefined template variables
func (v *Validator) validateTemplateVariables(template map[string]interface{}) {
	knownVars := map[string]bool{
		"node_id":       true,
		"node_index":    true,
		"total_nodes":   true,
		"deployment_id": true,
	}

	// Add global metadata keys
	for key := range v.config.Nodes.GlobalMetadata {
		knownVars[key] = true
	}

	// Add distributed list keys
	for key := range v.config.Nodes.DistributedLists {
		knownVars[key] = true
	}

	// Check template for unknown variables
	v.checkTemplateVars(template, "", knownVars)
}

// checkTemplateVars recursively checks template variables
func (v *Validator) checkTemplateVars(data map[string]interface{}, prefix string, knownVars map[string]bool) {
	for key, value := range data {
		fieldPath := key
		if prefix != "" {
			fieldPath = prefix + "." + key
		}

		if strVal, ok := value.(string); ok {
			// Find all template variables in the string
			vars := extractTemplateVars(strVal)
			for _, varName := range vars {
				if !knownVars[varName] {
					v.result.AddWarning(fmt.Sprintf("nodes.config_template.%s", fieldPath),
						fmt.Sprintf("unknown template variable '{%s}'", varName))
				}
			}
		} else if mapVal, ok := value.(map[string]interface{}); ok {
			v.checkTemplateVars(mapVal, fieldPath, knownVars)
		}
	}
}

// extractTemplateVars extracts template variables from a string
func extractTemplateVars(s string) []string {
	var vars []string
	start := -1
	for i, c := range s {
		if c == '{' {
			start = i + 1
		} else if c == '}' && start != -1 {
			varName := s[start:i]
			vars = append(vars, varName)
			start = -1
		}
	}
	return vars
}

// validateRemoteConfig validates remote deployment settings
func (v *Validator) validateRemoteConfig() {
	if v.config.RemoteDestDir == "" {
		v.result.AddWarning("remote_dest_dir",
			"remote_dest_dir not specified, will use default")
	}

	if v.config.RemoteScriptToRun == "" {
		v.result.AddInfo("remote_script_to_run",
			"no remote script specified, nodes will only register with daemon")
	}

	if v.config.BundleName == "" {
		v.result.AddInfo("bundle_name",
			"bundle_name not specified, will use default 'taskfly_bundle.tar.gz'")
	}
}

// checkCommonIssues checks for common configuration issues
func (v *Validator) checkCommonIssues() {
	// Check if using default values that might need customization
	if v.config.RemoteDestDir == "/tmp/taskfly_deployment" {
		v.result.AddInfo("remote_dest_dir",
			"using default /tmp directory, files may be cleaned up by system")
	}

	// Check for common security issues
	if v.config.CloudProvider == "aws" {
		if awsConfig, ok := v.config.InstanceConfig["aws"]; ok {
			if sg, ok := awsConfig["security_groups"].([]interface{}); ok {
				for _, group := range sg {
					if groupStr, ok := group.(string); ok && groupStr == "default" {
						v.result.AddWarning("instance_config.aws.security_groups",
							"using 'default' security group may have overly permissive rules")
					}
				}
			}
		}
	}
}
