//go:build windows

package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

var version = "dev"

var errRelaunched = errors.New("installer relaunched with administrator privileges")

type installerOptions struct {
	controlURL       string
	installDirectory string
	dataDirectory    string
	serviceName      string
	startService     bool
	keepTemp         bool
	showVersion      bool
	verifyPayload    bool
}

func main() {
	options := parseOptions()
	if options.showVersion {
		fmt.Println(version)
		return
	}
	if options.verifyPayload {
		if err := verifyPayload(); err != nil {
			fmt.Fprintln(os.Stderr, "IPv6Mesh payload verification failed:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(options); err != nil {
		if errors.Is(err, errRelaunched) {
			return
		}
		fmt.Fprintln(os.Stderr, "IPv6Mesh installer failed:", err)
		os.Exit(1)
	}
}

func parseOptions() installerOptions {
	options := installerOptions{}
	flag.StringVar(&options.controlURL, "control-url", "", "control-plane URL, for example http://[2001:db8::1]:8080")
	flag.StringVar(&options.installDirectory, "install-directory", "", "installation directory (default: C:\\Program Files\\IPv6Mesh)")
	flag.StringVar(&options.dataDirectory, "data-directory", "", "data directory (default: C:\\ProgramData\\IPv6Mesh)")
	flag.StringVar(&options.serviceName, "service-name", "", "Windows service name (default: IPv6Mesh)")
	flag.BoolVar(&options.startService, "start-service", true, "start the service after installation")
	flag.BoolVar(&options.keepTemp, "keep-temp", false, "keep the extracted temporary payload for debugging")
	flag.BoolVar(&options.showVersion, "version", false, "print installer version")
	flag.BoolVar(&options.verifyPayload, "verify-payload", false, "verify the embedded payload without installing")
	flag.Parse()
	return options
}

func run(options installerOptions) error {
	if len(embeddedPayload) == 0 {
		return errors.New("installer payload is empty; build with packaging/windows/build-installer.ps1")
	}

	admin, err := isAdministrator()
	if err != nil {
		return fmt.Errorf("check administrator privileges: %w", err)
	}
	if !admin {
		if err := relaunchElevated(); err != nil {
			return err
		}
		return errRelaunched
	}

	controlURL, err := resolveControlURL(options.controlURL)
	if err != nil {
		return err
	}

	tempDirectory, err := os.MkdirTemp("", "ipv6mesh-installer-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	preserveTemp := options.keepTemp
	defer func() {
		if !preserveTemp {
			_ = os.RemoveAll(tempDirectory)
		}
	}()

	if err := extractPayload(embeddedPayload, tempDirectory); err != nil {
		preserveTemp = true
		fmt.Fprintln(os.Stderr, "Temporary payload kept at:", tempDirectory)
		return err
	}

	powershell := findPowerShell()
	arguments := []string{
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-File",
		filepath.Join(tempDirectory, "install.ps1"),
		"-ControlUrl",
		controlURL,
	}
	if options.installDirectory != "" {
		arguments = append(arguments, "-InstallDirectory", options.installDirectory)
	}
	if options.dataDirectory != "" {
		arguments = append(arguments, "-DataDirectory", options.dataDirectory)
	}
	if options.serviceName != "" {
		arguments = append(arguments, "-ServiceName", options.serviceName)
	}
	if options.startService {
		arguments = append(arguments, "-StartService")
	}

	fmt.Printf("IPv6Mesh installer %s\n", version)
	fmt.Println("Control URL:", controlURL)
	fmt.Println("Extracted payload:", tempDirectory)

	command := exec.Command(powershell, arguments...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		preserveTemp = true
		fmt.Fprintln(os.Stderr, "Temporary payload kept at:", tempDirectory)
		return fmt.Errorf("run install.ps1: %w", err)
	}

	fmt.Println("IPv6Mesh installation completed.")
	return nil
}

func resolveControlURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		fmt.Print("Control-plane URL: ")
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read control-plane URL: %w", err)
		}
		value = strings.TrimSpace(line)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid control-plane URL %q", value)
	}
	return value, nil
}

func isAdministrator() (bool, error) {
	return windows.GetCurrentProcessToken().IsElevated(), nil
}

func relaunchElevated() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate installer executable: %w", err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("locate working directory: %w", err)
	}
	verb := windows.StringToUTF16Ptr("runas")
	file := windows.StringToUTF16Ptr(executable)
	arguments := windows.StringToUTF16Ptr(windows.ComposeCommandLine(os.Args[1:]))
	directory := windows.StringToUTF16Ptr(workingDirectory)
	if err := windows.ShellExecute(0, verb, file, arguments, directory, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("request administrator elevation: %w", err)
	}
	fmt.Println("UAC elevation requested; continue in the administrator window.")
	return nil
}

func findPowerShell() string {
	if windowsDirectory := os.Getenv("WINDIR"); windowsDirectory != "" {
		candidate := filepath.Join(windowsDirectory, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if candidate, err := exec.LookPath("powershell.exe"); err == nil {
		return candidate
	}
	return "powershell.exe"
}

func extractPayload(payload []byte, destination string) error {
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return fmt.Errorf("read embedded payload: %w", err)
	}
	for _, entry := range archive.File {
		name, err := safeZipPath(entry.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, name)
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("payload contains unsupported symbolic link %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return fmt.Errorf("create payload directory %q: %w", entry.Name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return fmt.Errorf("create payload parent for %q: %w", entry.Name, err)
		}
		input, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open payload file %q: %w", entry.Name, err)
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			_ = input.Close()
			return fmt.Errorf("create payload file %q: %w", entry.Name, err)
		}
		_, copyErr := io.Copy(output, input)
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if copyErr != nil {
			return fmt.Errorf("extract payload file %q: %w", entry.Name, copyErr)
		}
		if closeOutputErr != nil {
			return fmt.Errorf("close payload file %q: %w", entry.Name, closeOutputErr)
		}
		if closeInputErr != nil {
			return fmt.Errorf("close payload entry %q: %w", entry.Name, closeInputErr)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "install.ps1")); err != nil {
		return fmt.Errorf("embedded payload is missing install.ps1: %w", err)
	}
	return nil
}

func verifyPayload() error {
	if len(embeddedPayload) == 0 {
		return errors.New("installer payload is empty; build with packaging/windows/build-installer.ps1")
	}
	tempDirectory, err := os.MkdirTemp("", "ipv6mesh-installer-verify-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDirectory)
	if err := extractPayload(embeddedPayload, tempDirectory); err != nil {
		return err
	}
	for _, required := range []string{"install.ps1", "vpn-service.exe", "vpnctl.exe", "wireguard.dll", "wireguardnt-LICENSE.txt"} {
		if _, err := os.Stat(filepath.Join(tempDirectory, required)); err != nil {
			return fmt.Errorf("embedded payload is missing %s: %w", required, err)
		}
	}
	fmt.Printf("Embedded payload verified (%d bytes, %s)\n", len(embeddedPayload), version)
	return nil
}

func safeZipPath(name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe payload path %q", name)
	}
	return clean, nil
}
