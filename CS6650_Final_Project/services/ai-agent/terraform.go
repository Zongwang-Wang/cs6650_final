package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ReadCurrentNodeCount reads node_count from terraform.tfvars.
// Falls back to 4 (our deployed count) if file or variable not found.
func ReadCurrentNodeCount(terraformDir string) (int, error) {
	tfvarsPath := filepath.Join(terraformDir, "terraform.tfvars")
	data, err := os.ReadFile(tfvarsPath)
	if err != nil {
		return 4, fmt.Errorf("read tfvars: %w", err)
	}

	re := regexp.MustCompile(`node_count\s*=\s*(\d+)`)
	matches := re.FindStringSubmatch(string(data))
	if len(matches) < 2 {
		return 4, fmt.Errorf("node_count not found in tfvars")
	}

	count, err := strconv.Atoi(matches[1])
	if err != nil {
		return 4, fmt.Errorf("parse node_count: %w", err)
	}
	return count, nil
}

// ApplyTerraform updates node_count in terraform.tfvars and runs terraform apply.
// Dry-run mode is enforced by the caller — this always applies for real.
func ApplyTerraform(terraformDir string, newCount int) error {
	tfvarsPath := filepath.Join(terraformDir, "terraform.tfvars")

	// Read current tfvars
	data, err := os.ReadFile(tfvarsPath)
	if err != nil {
		return fmt.Errorf("read tfvars: %w", err)
	}

	content := string(data)

	// Replace or append node_count
	re := regexp.MustCompile(`(?m)^node_count\s*=\s*\d+`)
	newLine := fmt.Sprintf("node_count = %d", newCount)
	if re.MatchString(content) {
		content = re.ReplaceAllString(content, newLine)
	} else {
		content = strings.TrimRight(content, "\n") + "\n" + newLine + "\n"
	}

	if err := os.WriteFile(tfvarsPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write tfvars: %w", err)
	}

	log.Printf("terraform: updated node_count=%d in %s", newCount, tfvarsPath)

	// Run terraform apply
	cmd := exec.Command("terraform", "apply", "-auto-approve",
		fmt.Sprintf("-var=node_count=%d", newCount))
	cmd.Dir = terraformDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("terraform apply: %w\noutput: %s", err, string(out))
	}

	log.Printf("terraform apply output: %s", string(out))
	return nil
}
