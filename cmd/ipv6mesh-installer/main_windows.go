//go:build windows

package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
	"golang.org/x/sys/windows"
)

var version = "dev"

var errRelaunched = errors.New("installer relaunched with administrator privileges")

type installerOptions struct {
	controlURL       string
	invite           string
	deviceName       string
	networkID        string
	installDirectory string
	dataDirectory    string
	serviceName      string
	startService     bool
	connect          bool
	keepTemp         bool
	nonInteractive   bool
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
		waitForExit(options.nonInteractive)
		os.Exit(1)
	}
}

func parseOptions() installerOptions {
	options := installerOptions{}
	flag.StringVar(&options.controlURL, "control-url", "", "control-plane URL, for example http://[2001:db8::1]:8080")
	flag.StringVar(&options.invite, "invite", "", "one-time enrollment invite; prompted when omitted")
	flag.StringVar(&options.deviceName, "device-name", "", "device display name; computer name is used when omitted")
	flag.StringVar(&options.networkID, "network", "", "network ID; the invite network is used when omitted")
	flag.StringVar(&options.installDirectory, "install-directory", "", "installation directory (default: C:\\Program Files\\IPv6Mesh)")
	flag.StringVar(&options.dataDirectory, "data-directory", "", "data directory (default: C:\\ProgramData\\IPv6Mesh)")
	flag.StringVar(&options.serviceName, "service-name", "", "Windows service name (default: IPv6Mesh)")
	flag.BoolVar(&options.startService, "start-service", true, "start the service after installation")
	flag.BoolVar(&options.connect, "connect", true, "join and connect the node after installation")
	flag.BoolVar(&options.keepTemp, "keep-temp", false, "keep the extracted temporary payload for debugging")
	flag.BoolVar(&options.nonInteractive, "non-interactive", false, "do not prompt or wait for input; required values must be flags")
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
	if options.nonInteractive && strings.TrimSpace(options.controlURL) == "" {
		return errors.New("-control-url is required with -non-interactive")
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
	arguments := buildInstallArguments(options, tempDirectory, controlURL)

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
	if !options.startService {
		fmt.Println("The service was not started because -start-service=false was supplied.")
		waitForExit(options.nonInteractive)
		return nil
	}
	if err := runConnectionWizard(options); err != nil {
		return err
	}
	return nil
}

func buildInstallArguments(options installerOptions, tempDirectory, controlURL string) []string {
	arguments := []string{
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-File",
		filepath.Join(tempDirectory, "install.ps1"),
		"-PackageDirectory",
		tempDirectory,
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
	return arguments
}

func runConnectionWizard(options installerOptions) error {
	reader := bufio.NewReader(os.Stdin)
	client := ipc.NewClient(ipc.DefaultPipeName)
	status, err := callService(client, ipc.Request{Type: ipc.CommandStatus})
	if err != nil {
		return fmt.Errorf("wait for IPv6Mesh service: %w", err)
	}
	if !status.OK {
		return serviceResponseError("status", status)
	}

	networkID := strings.TrimSpace(options.networkID)
	if status.NetworkID != "" {
		fmt.Println("This device is already joined to network:", status.NetworkID)
		if networkID == "" {
			networkID = status.NetworkID
		}
		if status.VirtualIPv4 != "" {
			fmt.Println("Current virtual IPv4:", status.VirtualIPv4)
		}
	} else {
		invite, err := promptValue(reader, "One-time invite token", options.invite, "", options.nonInteractive)
		if err != nil {
			return err
		}
		deviceName, err := promptValue(reader, "Device name", options.deviceName, defaultDeviceName(), options.nonInteractive)
		if err != nil {
			return err
		}
		fmt.Println("Joining the IPv6Mesh network...")
		joined, err := callService(client, ipc.Request{Type: ipc.CommandJoin, Invite: invite, DisplayName: deviceName})
		if err != nil {
			return fmt.Errorf("join network: %w", err)
		}
		if !joined.OK {
			return serviceResponseError("join", joined)
		}
		networkID = joined.NetworkID
		fmt.Println("Joined network:", joined.NetworkID)
		fmt.Println("Virtual IPv4:", joined.VirtualIPv4)
	}

	if !options.connect {
		fmt.Println("Connection step skipped because -connect=false was supplied.")
		waitForExit(options.nonInteractive)
		return nil
	}
	if networkID == "" {
		return errors.New("network ID is unavailable; provide -network")
	}
	fmt.Println("Connecting the virtual adapter...")
	connected, err := callService(client, ipc.Request{Type: ipc.CommandConnect, NetworkID: networkID})
	if err != nil {
		return fmt.Errorf("connect network: %w", err)
	}
	if !connected.OK {
		return serviceResponseError("connect", connected)
	}
	finalStatus, err := callService(client, ipc.Request{Type: ipc.CommandStatus})
	if err != nil {
		return fmt.Errorf("read final status: %w", err)
	}
	if !finalStatus.OK {
		return serviceResponseError("final status", finalStatus)
	}
	fmt.Println("IPv6Mesh is connected.")
	fmt.Println("Network:", finalStatus.NetworkID)
	fmt.Println("Virtual IPv4:", finalStatus.VirtualIPv4)
	fmt.Println("Path:", finalStatus.PathState)
	waitForExit(options.nonInteractive)
	return nil
}

func callService(client *ipc.Client, request ipc.Request) (ipc.Response, error) {
	if client == nil {
		return ipc.Response{}, errors.New("IPC client is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var lastErr error
	for {
		response, err := client.Call(ctx, request)
		if err == nil {
			return response, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ipc.Response{}, fmt.Errorf("%w: %v", ctx.Err(), lastErr)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func serviceResponseError(operation string, response ipc.Response) error {
	if response.Error == nil {
		return fmt.Errorf("%s failed", operation)
	}
	if response.Error.Message != "" {
		return fmt.Errorf("%s failed: %s (%s)", operation, response.Error.Message, response.Error.Code)
	}
	return fmt.Errorf("%s failed: %s", operation, response.Error.Code)
}

func promptValue(reader *bufio.Reader, label, value, fallback string, nonInteractive bool) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return value, nil
	}
	if nonInteractive {
		return "", fmt.Errorf("%s is required in non-interactive mode", label)
	}
	if fallback != "" {
		fmt.Printf("%s [%s]: ", label, fallback)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read %s: %w", label, err)
	}
	value = strings.TrimSpace(line)
	if value == "" {
		value = strings.TrimSpace(fallback)
	}
	if value == "" {
		return "", fmt.Errorf("%s cannot be empty", label)
	}
	return value, nil
}

func defaultDeviceName() string {
	if name, err := os.Hostname(); err == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if name := strings.TrimSpace(os.Getenv("COMPUTERNAME")); name != "" {
		return name
	}
	return "ipv6mesh-device"
}

func waitForExit(nonInteractive bool) {
	if nonInteractive {
		return
	}
	fmt.Println("Press Enter to close this window.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
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
