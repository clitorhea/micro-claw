package tools

import (
	"context"
	"strings"
	"testing"
)

func TestExecuteShellCommand(t *testing.T) {
	// Create registry with nil parameters, as registration doesn't evaluate them.
	r := NewRegistry(nil, nil, 0)

	// Verify tool is registered
	if !r.HasTool("execute_shell_command") {
		t.Fatal("execute_shell_command tool is not registered")
	}

	toolDef := r.tools["execute_shell_command"]
	if !toolDef.IsStateful {
		t.Error("execute_shell_command should be stateful")
	}

	ctx := context.Background()

	// Test successful command execution
	args := map[string]interface{}{
		"command": "echo 'Hello World'",
	}
	output, err := toolDef.Execute(ctx, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(output) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", output)
	}

	// Test command execution with error
	argsFailed := map[string]interface{}{
		"command": "false",
	}
	outputFailed, errFailed := toolDef.Execute(ctx, argsFailed)
	if errFailed != nil {
		t.Fatalf("unexpected execution framework error: %v", errFailed)
	}
	if !strings.Contains(outputFailed, "Command failed with error") {
		t.Errorf("expected output to contain failure details, got %q", outputFailed)
	}

	// Test invalid arguments
	argsInvalid := map[string]interface{}{}
	_, errInvalid := toolDef.Execute(ctx, argsInvalid)
	if errInvalid == nil {
		t.Error("expected error for missing 'command' argument")
	}
}
