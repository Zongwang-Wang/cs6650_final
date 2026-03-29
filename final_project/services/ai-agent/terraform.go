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

// ReadCurrentTaskCount reads the current ai_desired_count from terraform.tfvars.
func ReadCurrentTaskCount(terraformDir string) (int, error) {
	tfvarsPath := filepath.Join(terraformDir, "terraform.tfvars")
	data, err := os.ReadFile(tfvarsPath)
	if err != nil {
		return 0, fmt.Errorf("read tfvars: %w", err)
	}

	re := regexp.MustCompile(`ai_desired_count\s*=\s*(\d+)`)
	matches := re.FindStringSubmatch(string(data))
	if len(matches) < 2 {
		return 0, fmt.Errorf("ai_desired_count not found in %s", tfvarsPath)
	}

	count, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("parse ai_desired_count: %w", err)
	}

	return count, nil
}

// ApplyTerraform updates terraform.tfvars with the new task count and runs
// terraform plan + apply.
func ApplyTerraform(terraformDir string, newTaskCount int) error {
	tfvarsPath := filepath.Join(terraformDir, "terraform.tfvars")

	// Read current file
	data, err := os.ReadFile(tfvarsPath)
	if err != nil {
		return fmt.Errorf("read tfvars: %w", err)
	}

	content := string(data)

	// Update ai_desired_count
	reCount := regexp.MustCompile(`ai_desired_count\s*=\s*\d+`)
	newContent := reCount.ReplaceAllString(content, fmt.Sprintf("ai_desired_count = %d", newTaskCount))

	// Ensure scaling_mode is set to "ai"
	reMode := regexp.MustCompile(`scaling_mode\s*=\s*"[^"]*"`)
	if reMode.MatchString(newContent) {
		newContent = reMode.ReplaceAllString(newContent, `scaling_mode = "ai"`)
	}

	// Write updated file
	if err := os.WriteFile(tfvarsPath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("write tfvars: %w", err)
	}

	log.Printf("Updated %s: ai_desired_count=%d, scaling_mode=ai", tfvarsPath, newTaskCount)

	// Run terraform plan
	log.Println("Running terraform plan...")
	planCmd := exec.Command("terraform", "plan", "-input=false")
	planCmd.Dir = terraformDir
	planCmd.Stdout = os.Stdout
	planCmd.Stderr = os.Stderr
	if err := planCmd.Run(); err != nil {
		return fmt.Errorf("terraform plan failed: %w", err)
	}

	// Run terraform apply
	log.Println("Running terraform apply...")
	applyCmd := exec.Command("terraform", "apply", "-auto-approve", "-input=false")
	applyCmd.Dir = terraformDir
	applyCmd.Stdout = os.Stdout
	applyCmd.Stderr = os.Stderr
	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("terraform apply failed: %w", err)
	}

	log.Println("Terraform apply completed successfully.")
	return nil
}

// DryRunTerraform logs what would change without actually applying.
func DryRunTerraform(terraformDir string, currentCount, newCount int) {
	tfvarsPath := filepath.Join(terraformDir, "terraform.tfvars")
	log.Printf("DRY RUN: would update %s", tfvarsPath)
	log.Printf("DRY RUN: ai_desired_count: %d -> %d", currentCount, newCount)
	log.Printf("DRY RUN: scaling_mode -> \"ai\"")

	// Show the diff that would be made
	data, err := os.ReadFile(tfvarsPath)
	if err != nil {
		log.Printf("DRY RUN: (could not read tfvars for preview: %v)", err)
		return
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ai_desired_count") || strings.HasPrefix(trimmed, "scaling_mode") {
			log.Printf("DRY RUN:   current: %s", trimmed)
		}
	}
}
