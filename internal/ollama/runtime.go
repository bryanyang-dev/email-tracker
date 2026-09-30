package ollama

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const readinessPollInterval = 100 * time.Millisecond

type Runtime struct {
	client       *Client
	baseURL      string
	startCommand func() (*exec.Cmd, error)

	mu      sync.Mutex
	command *exec.Cmd
	done    chan error
}

func NewRuntime(client *Client, baseURL string) *Runtime {
	runtime := &Runtime{client: client, baseURL: baseURL}
	runtime.startCommand = runtime.ollamaCommand
	return runtime
}

// EnsureRunning starts the installed Ollama CLI only when the configured API
// cannot already be reached. The returned boolean reports process ownership.
func (r *Runtime) EnsureRunning(ctx context.Context) (bool, error) {
	if _, err := r.client.InstalledModels(ctx); err == nil {
		return false, nil
	}

	r.mu.Lock()
	if r.command != nil {
		r.mu.Unlock()
		return false, nil
	}
	command, err := r.startCommand()
	if err != nil {
		r.mu.Unlock()
		return false, err
	}
	if err := command.Start(); err != nil {
		r.mu.Unlock()
		return false, fmt.Errorf("start Ollama: %w", err)
	}
	r.command = command
	r.done = make(chan error, 1)
	done := r.done
	r.mu.Unlock()

	go func() {
		done <- command.Wait()
	}()

	ticker := time.NewTicker(readinessPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			stopContext, cancelStop := context.WithTimeout(context.Background(), 2*time.Second)
			_ = r.Stop(stopContext)
			cancelStop()
			return false, fmt.Errorf("wait for Ollama readiness: %w", ctx.Err())
		case waitErr := <-done:
			if _, healthErr := r.client.InstalledModels(ctx); healthErr == nil {
				r.clearCommand(command)
				return false, nil
			}
			r.clearCommand(command)
			if waitErr == nil {
				return false, fmt.Errorf("Ollama exited before becoming ready")
			}
			return false, fmt.Errorf("Ollama exited before becoming ready: %w", waitErr)
		case <-ticker.C:
			if _, err := r.client.InstalledModels(ctx); err == nil {
				return true, nil
			}
		}
	}
}

func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	command := r.command
	done := r.done
	r.mu.Unlock()
	if command == nil || command.Process == nil {
		return nil
	}

	if err := command.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop Ollama: %w", err)
	}

	select {
	case <-done:
		r.clearCommand(command)
		return nil
	case <-ctx.Done():
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill Ollama after shutdown timeout: %w", err)
		}
		<-done
		r.clearCommand(command)
		return ctx.Err()
	}
}

func (r *Runtime) ollamaCommand() (*exec.Cmd, error) {
	binary, err := exec.LookPath("ollama")
	if err != nil {
		return nil, fmt.Errorf("find Ollama executable: %w", err)
	}
	parsed, err := url.Parse(r.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse Ollama endpoint: %w", err)
	}

	command := exec.Command(binary, "serve")
	command.Env = runtimeEnvironment(os.Environ(), parsed.Host)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return command, nil
}

func (r *Runtime) clearCommand(command *exec.Cmd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.command == command {
		r.command = nil
		r.done = nil
	}
}

func runtimeEnvironment(environment []string, host string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "OLLAMA_HOST" {
			continue
		}
		if runtimeEnvironmentVariable(name) {
			result = append(result, entry)
		}
	}
	return append(result, "OLLAMA_HOST="+host)
}

func runtimeEnvironmentVariable(name string) bool {
	switch name {
	case "HOME", "PATH", "TMPDIR", "USER", "LOGNAME", "LANG":
		return true
	default:
		return strings.HasPrefix(name, "LC_") || strings.HasPrefix(name, "OLLAMA_")
	}
}
