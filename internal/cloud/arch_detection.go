package cloud

import "strings"

// DetectArchFromInstanceType determines the CPU architecture based on AWS instance type
// AWS Graviton instances (ARM64) vs x86_64 instances
func DetectArchFromInstanceType(instanceType string) string {
	// Graviton-based instances use ARM64
	gravitonPrefixes := []string{
		// Graviton 1
		"a1.",

		// Graviton 2
		"t4g.", "t4gd.",
		"m6g.", "m6gd.",
		"c6g.", "c6gd.", "c6gn.",
		"r6g.", "r6gd.",
		"x2gd.",
		"g5g.",                    // Graphics-intensive
		"im4gn.", "is4gen.", "i4g.", // Storage-optimized

		// Graviton 3
		"m7g.", "m7gd.",
		"c7g.", "c7gd.", "c7gn.",
		"r7g.", "r7gd.", "r7gn.",
		"hpc7g.", // HPC workloads

		// Graviton 4 (Latest - released July 2024)
		"r8g.",
		"x8g.",
		"c8g.",
		"m8g.",
		"i8g.",
	}

	for _, prefix := range gravitonPrefixes {
		if strings.HasPrefix(instanceType, prefix) {
			return "arm64"
		}
	}

	// Default to x86_64/amd64 for all other instance types
	return "amd64"
}
