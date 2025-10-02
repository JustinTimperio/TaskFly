package cloud

// ProviderConfigHelper provides common config helper methods for providers
type ProviderConfigHelper struct {
	config map[string]interface{}
}

// NewProviderConfigHelper creates a new config helper
func NewProviderConfigHelper(config map[string]interface{}) *ProviderConfigHelper {
	return &ProviderConfigHelper{config: config}
}

// GetString gets a string configuration value with a default
func (h *ProviderConfigHelper) GetString(key, defaultValue string) string {
	if value, ok := h.config[key].(string); ok {
		return value
	}
	return defaultValue
}

// GetStringSlice gets a string slice configuration value with a default
func (h *ProviderConfigHelper) GetStringSlice(key string, defaultValue []string) []string {
	if value, ok := h.config[key].([]interface{}); ok {
		result := make([]string, len(value))
		for i, v := range value {
			if str, ok := v.(string); ok {
				result[i] = str
			}
		}
		return result
	}
	return defaultValue
}

// GetBool gets a boolean configuration value with a default
func (h *ProviderConfigHelper) GetBool(key string, defaultValue bool) bool {
	if value, ok := h.config[key].(bool); ok {
		return value
	}
	return defaultValue
}

// GetInt gets an integer configuration value with a default
func (h *ProviderConfigHelper) GetInt(key string, defaultValue int) int {
	if value, ok := h.config[key].(int); ok {
		return value
	}
	// Handle float64 from JSON unmarshaling
	if value, ok := h.config[key].(float64); ok {
		return int(value)
	}
	return defaultValue
}
