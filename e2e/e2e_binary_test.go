package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

func buildMailbox(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mailbox-e2e-*")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, "mailbox")
		cmd := exec.Command("go", "build", "-o", buildPath, "github.com/sjawhar/mailbox/cmd/mailbox")
		cmd.Dir = ".."
		if output, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v: %s", err, output)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildPath
}

// runBinary executes the built mailbox on the batch surface with only env.
func runBinary(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	return runBinaryInDir(t, "", env, args...)
}

// runBinaryInDir executes the built mailbox with dir as its working directory.
func runBinaryInDir(t *testing.T, dir string, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(buildMailbox(t), args...)
	command.Dir = dir
	command.Env = environment(env)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatalf("run mailbox %q: %v", args, err)
	return -1, "", ""
}
