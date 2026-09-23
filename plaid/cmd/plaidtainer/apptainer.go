package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// PlaidOptions captures Plaid-specific networking options extracted from CLI flags.
type PlaidOptions struct {
	HostNetworking bool
	IP             string
	Gateway        string
	DNS            string
	Socket         string
	Bind           string
	Binds          []string
	Binary         string
	MTU            int
}

// EnsureDefaults sets default socket and binary paths if not explicitly configured.
func (opts *PlaidOptions) EnsureDefaults() {
	if opts.Socket == "" {
		opts.Socket = "/run/plaid/plaidd.sock"
	}
	if opts.Binary == "" {
		opts.Binary = "/usr/local/bin/plaid"
	}
	if opts.MTU <= 0 {
		if envMTU := os.Getenv("PLAID_MTU"); envMTU != "" {
			if parsed, err := strconv.Atoi(envMTU); err == nil && parsed > 0 {
				opts.MTU = parsed
			}
		}
	}
}

// mergePlaidOptions merges two PlaidOptions structs, giving precedence to override.
func mergePlaidOptions(base, override PlaidOptions) PlaidOptions {
	res := base
	if override.HostNetworking {
		res.HostNetworking = true
	}
	if override.IP != "" {
		res.IP = override.IP
	}
	if override.Gateway != "" {
		res.Gateway = override.Gateway
	}
	if override.DNS != "" {
		res.DNS = override.DNS
	}
	if override.Socket != "" {
		res.Socket = override.Socket
	}
	if override.Bind != "" {
		res.Bind = override.Bind
	}
	if len(override.Binds) > 0 {
		res.Binds = append(res.Binds, override.Binds...)
	}
	if override.Binary != "" {
		res.Binary = override.Binary
	}
	if override.MTU > 0 {
		res.MTU = override.MTU
	}
	return res
}

// parseWrapperArgs parses options strictly up to the container image/target argument.
// Any arguments following the image (or following '--') are preserved byte-for-byte as workload arguments.
func parseWrapperArgs(args []string) (opts PlaidOptions, apptainerFlags []string, image string, workloadArgs []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Explicit delimiter: everything after '--' belongs to the image / workload
		if arg == "--" {
			remaining := args[i+1:]
			if image == "" && len(remaining) > 0 {
				image = remaining[0]
				workloadArgs = remaining[1:]
			} else {
				workloadArgs = remaining
			}
			return opts, apptainerFlags, image, workloadArgs
		}

		// Plaid-specific options
		if arg == "--host-networking" {
			opts.HostNetworking = true
			continue
		}
		if strings.HasPrefix(arg, "--ip=") {
			opts.IP = strings.TrimPrefix(arg, "--ip=")
			continue
		}
		if arg == "--ip" && i+1 < len(args) {
			opts.IP = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, "--gateway=") {
			opts.Gateway = strings.TrimPrefix(arg, "--gateway=")
			continue
		}
		if arg == "--gateway" && i+1 < len(args) {
			opts.Gateway = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, "--dns=") {
			opts.DNS = strings.TrimPrefix(arg, "--dns=")
			continue
		}
		if arg == "--dns" && i+1 < len(args) {
			opts.DNS = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, "--socket=") {
			opts.Socket = strings.TrimPrefix(arg, "--socket=")
			continue
		}
		if arg == "--socket" && i+1 < len(args) {
			opts.Socket = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, "--bind=") {
			val := strings.TrimPrefix(arg, "--bind=")
			opts.Bind = val
			opts.Binds = append(opts.Binds, val)
			continue
		}
		if arg == "--bind" && i+1 < len(args) {
			val := args[i+1]
			opts.Bind = val
			opts.Binds = append(opts.Binds, val)
			i++
			continue
		}
		if strings.HasPrefix(arg, "--binary=") {
			opts.Binary = strings.TrimPrefix(arg, "--binary=")
			continue
		}
		if arg == "--binary" && i+1 < len(args) {
			opts.Binary = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, "--mtu=") {
			if val, err := strconv.Atoi(strings.TrimPrefix(arg, "--mtu=")); err == nil {
				opts.MTU = val
			}
			continue
		}
		if arg == "--mtu" && i+1 < len(args) {
			if val, err := strconv.Atoi(args[i+1]); err == nil {
				opts.MTU = val
			}
			i++
			continue
		}

		// Apptainer flags
		if strings.HasPrefix(arg, "-") {
			apptainerFlags = append(apptainerFlags, arg)
			if !strings.Contains(arg, "=") && takesArg(arg) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				apptainerFlags = append(apptainerFlags, args[i+1])
				i++
			}
			continue
		}

		// First non-flag argument is the image! Stop parsing wrapper options.
		image = arg
		workloadArgs = args[i+1:]
		return opts, apptainerFlags, image, workloadArgs
	}

	return opts, apptainerFlags, image, workloadArgs
}

// parsePlaidOptions scans args and extracts Plaid-specific flags, returning the parsed options
// and remaining arguments intended for Apptainer.
func parsePlaidOptions(args []string) (PlaidOptions, []string) {
	opts, flags, image, workload := parseWrapperArgs(args)
	var remaining []string
	remaining = append(remaining, flags...)
	if image != "" {
		remaining = append(remaining, image)
	}
	remaining = append(remaining, workload...)
	return opts, remaining
}

// findApptainerBinary locates the underlying apptainer binary.
func findApptainerBinary() (string, error) {
	if bin := os.Getenv("APPTAINER_BIN"); bin != "" {
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}
	bin, err := exec.LookPath("apptainer")
	if err != nil {
		return "", fmt.Errorf("apptainer binary not found in PATH: %w", err)
	}
	return bin, nil
}

// findHostPlaidBinary locates the host plaid binary to bind-mount into containers.
func findHostPlaidBinary() string {
	var candidates []string

	// 1. Explicit environment variable override
	if envBin := os.Getenv("PLAID_BIN"); envBin != "" {
		candidates = append(candidates, envBin)
	}

	// 2. Sibling binary in the same directory as this executable
	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		candidates = append(candidates,
			filepath.Join(execDir, "plaid"),
			filepath.Join(execDir, "plaid-linux"),
		)
	}

	// 3. Standard installation paths
	candidates = append(candidates,
		"/usr/local/bin/plaid",
		"/opt/cni/bin/plaid",
		"/usr/libexec/cni/plaid",
	)

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			return c
		}
	}

	if p, err := exec.LookPath("plaid"); err == nil {
		return p
	}

	return ""
}

// runCommand runs a command, forwards stdio and returns exit code.
func runCommand(bin string, args []string) int {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "Error executing %s: %v\n", bin, err)
		return 1
	}
	return 0
}

// runCommandWithCleanup runs a command with bounded signal trapping and cleanup callback.
func runCommandWithCleanup(bin string, args []string, cleanup func()) int {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if err := cmd.Start(); err != nil {
		cleanup()
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "Error starting %s: %v\n", bin, err)
		return 1
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var waitErr error
	select {
	case sig := <-sigChan:
		if cmd.Process != nil {
			_ = cmd.Process.Signal(sig)
		}
		// Bounded escalation: wait up to 5 seconds for graceful shutdown, then SIGKILL
		select {
		case waitErr = <-done:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			waitErr = <-done
		case <-sigChan:
			// Second signal immediately escalates to SIGKILL
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			waitErr = <-done
		}
	case waitErr = <-done:
	}

	cleanup()

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}
