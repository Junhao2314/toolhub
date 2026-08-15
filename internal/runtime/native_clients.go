package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"golang.org/x/sys/unix"
)

const (
	nativeClientInspectionTimeout = 5 * time.Second
	nativeClientMaxOutputBytes    = 4096
)

var nativeSemanticVersionPattern = regexp.MustCompile(`(?:^|[^0-9A-Za-z.-])[vV]?([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:$|[^0-9A-Za-z.+-])`)

var errNativeClientPathUnsafe = errors.New("native client path is unsafe")

type nativeSemanticVersion struct {
	components [3]uint64
	prerelease string
}

type nativeClientInspector struct {
	timeout        time.Duration
	maxOutputBytes int
	systemPrefixes []string
}

type nativeClientLocation struct {
	directory       string
	containmentRoot string
}

type nativeClientExecutable struct {
	file            *os.File
	binaryDirectory string
	device          uint64
	inode           uint64
}

func (e *nativeClientExecutable) Close() error { return e.file.Close() }

func InspectNativeClient(ctx context.Context, user ManagedUser, clientKind string) bridgeprotocol.NativeClientInspectionResponse {
	return (nativeClientInspector{
		timeout:        nativeClientInspectionTimeout,
		maxOutputBytes: nativeClientMaxOutputBytes,
		systemPrefixes: []string{"/usr/bin", "/usr/local/bin"},
	}).Inspect(ctx, user, clientKind)
}

func (i nativeClientInspector) Inspect(ctx context.Context, user ManagedUser, clientKind string) bridgeprotocol.NativeClientInspectionResponse {
	result := bridgeprotocol.NativeClientInspectionResponse{ClientKind: clientKind}
	if clientKind != bridgeprotocol.RuntimeClaude && clientKind != bridgeprotocol.RuntimeCodex {
		result.ErrorCode = "native_client_kind_invalid"
		return result
	}
	if err := validateInspectionUser(user); err != nil {
		result.ErrorCode = "native_client_path_unsafe"
		return result
	}
	executables := make([]*nativeClientExecutable, 0, 1)
	defer func() {
		for _, executable := range executables {
			_ = executable.Close()
		}
	}()
	seen := make(map[[2]uint64]struct{})
	for _, location := range i.approvedLocations(user) {
		executable, err := openNativeClient(location, clientKind)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if errors.Is(err, errNativeClientPathUnsafe) {
				result.ErrorCode = "native_client_path_unsafe"
			} else {
				result.ErrorCode = "native_client_inspection_failed"
			}
			return result
		}
		identity := [2]uint64{executable.device, executable.inode}
		if _, exists := seen[identity]; exists {
			_ = executable.Close()
			continue
		}
		seen[identity] = struct{}{}
		executables = append(executables, executable)
	}
	switch len(executables) {
	case 0:
		result.ErrorCode = "native_client_not_found"
		return result
	case 1:
		return i.inspectVersion(ctx, user, clientKind, executables[0])
	default:
		result.ErrorCode = "native_client_resolution_ambiguous"
		return result
	}
}

func openNativeClient(location nativeClientLocation, clientKind string) (*nativeClientExecutable, error) {
	root := filepath.Clean(location.containmentRoot)
	directory := filepath.Clean(location.directory)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) || !pathWithin(root, directory) {
		return nil, errNativeClientPathUnsafe
	}
	rootBase, err := unix.Open(string(filepath.Separator), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootBase)
	rootRelative := strings.TrimPrefix(root, string(filepath.Separator))
	rootFD, err := unix.Openat2(rootBase, rootRelative, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return nil, os.ErrNotExist
		}
		if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ELOOP) {
			return nil, errNativeClientPathUnsafe
		}
		return nil, err
	}
	defer unix.Close(rootFD)

	directoryRelative, err := filepath.Rel(root, directory)
	if err != nil || filepath.IsAbs(directoryRelative) || directoryRelative == ".." || strings.HasPrefix(directoryRelative, ".."+string(filepath.Separator)) {
		return nil, errNativeClientPathUnsafe
	}
	candidateRelative := filepath.Join(directoryRelative, clientKind)
	fd, err := unix.Openat2(rootFD, candidateRelative, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return nil, os.ErrNotExist
		}
		if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ELOOP) {
			return nil, errNativeClientPathUnsafe
		}
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0111 == 0 {
		_ = unix.Close(fd)
		return nil, errNativeClientPathUnsafe
	}
	file := os.NewFile(uintptr(fd), "native-client-"+clientKind)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open native client file descriptor")
	}
	return &nativeClientExecutable{file: file, binaryDirectory: directory, device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func (i nativeClientInspector) approvedLocations(user ManagedUser) []nativeClientLocation {
	locations := []nativeClientLocation{{directory: filepath.Join(user.Home, ".local", "bin"), containmentRoot: filepath.Join(user.Home, ".local")}}
	nvmBins, _ := filepath.Glob(filepath.Join(user.Home, ".nvm", "versions", "node", "*", "bin"))
	for _, directory := range nvmBins {
		locations = append(locations, nativeClientLocation{directory: directory, containmentRoot: filepath.Dir(directory)})
	}
	locations = append(locations, nativeClientLocation{directory: filepath.Join(user.Home, ".volta", "bin"), containmentRoot: filepath.Join(user.Home, ".volta")})
	for _, directory := range i.systemPrefixes {
		locations = append(locations, nativeClientLocation{directory: directory, containmentRoot: filepath.Dir(directory)})
	}
	return locations
}

func (i nativeClientInspector) inspectVersion(ctx context.Context, user ManagedUser, clientKind string, executable *nativeClientExecutable) bridgeprotocol.NativeClientInspectionResponse {
	result := bridgeprotocol.NativeClientInspectionResponse{ClientKind: clientKind}
	timeout := i.timeout
	if timeout <= 0 || timeout > nativeClientInspectionTimeout {
		timeout = nativeClientInspectionTimeout
	}
	maxOutput := i.maxOutputBytes
	if maxOutput <= 0 || maxOutput > nativeClientMaxOutputBytes {
		maxOutput = nativeClientMaxOutputBytes
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(callCtx, "/proc/self/fd/3", "--version")
	command.ExtraFiles = []*os.File{executable.file}
	command.WaitDelay = 100 * time.Millisecond
	command.Dir = user.Home
	command.Env = []string{
		"HOME=" + user.Home,
		"USER=" + user.Name,
		"LOGNAME=" + user.Name,
		"LANG=C",
		"LC_ALL=C",
		"PATH=" + fixedInspectionPath(executable.binaryDirectory),
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(user.UID), Gid: uint32(user.GID), Groups: []uint32{}}}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	output := &boundedNativeOutput{max: maxOutput}
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		result.ErrorCode = "native_client_timeout"
		return result
	}
	if output.overflow {
		result.ErrorCode = "native_client_output_invalid"
		return result
	}
	if err != nil {
		result.ErrorCode = "native_client_inspection_failed"
		return result
	}
	version, semanticVersion, ok := parseNativeSemanticVersion(output.String())
	if !ok {
		result.ErrorCode = "native_client_version_invalid"
		return result
	}
	result.Version = version
	minimum := map[string][3]uint64{
		bridgeprotocol.RuntimeClaude: {2, 1, 232},
		bridgeprotocol.RuntimeCodex:  {0, 147, 0},
	}[clientKind]
	if compareNativeVersionFloor(semanticVersion, minimum) < 0 {
		result.ErrorCode = "native_client_version_unsupported"
		return result
	}
	result.Supported = true
	return result
}

func validateInspectionUser(user ManagedUser) error {
	if bridgeprotocol.ValidateManagedUsername(user.Name) != nil || user.UID < 0 || user.GID < 0 || !filepath.IsAbs(user.Home) || filepath.Clean(user.Home) == string(filepath.Separator) || strings.ContainsAny(user.Home, ":\x00\r\n") {
		return errors.New("managed user is invalid")
	}
	resolved, err := filepath.EvalSymlinks(user.Home)
	if err != nil || resolved != filepath.Clean(user.Home) {
		return errors.New("managed user home is not canonical")
	}
	info, err := os.Lstat(user.Home)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed user home is unsafe")
	}
	return nil
}

func fixedInspectionPath(binaryDirectory string) string {
	paths := []string{filepath.Clean(binaryDirectory), "/usr/local/bin", "/usr/bin", "/bin"}
	unique := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if !seen[path] {
			seen[path] = true
			unique = append(unique, path)
		}
	}
	return strings.Join(unique, ":")
}

func pathWithin(prefix, candidate string) bool {
	prefix = filepath.Clean(prefix)
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(prefix, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func parseNativeSemanticVersion(output string) (string, nativeSemanticVersion, bool) {
	match := nativeSemanticVersionPattern.FindStringSubmatch(output)
	if len(match) != 6 {
		return "", nativeSemanticVersion{}, false
	}
	var values [3]uint64
	for index := range values {
		if len(match[index+1]) > 1 && match[index+1][0] == '0' {
			return "", nativeSemanticVersion{}, false
		}
		value, err := strconv.ParseUint(match[index+1], 10, 31)
		if err != nil {
			return "", nativeSemanticVersion{}, false
		}
		values[index] = value
	}
	for _, identifier := range strings.Split(match[4], ".") {
		if len(identifier) > 1 && identifier[0] == '0' {
			if _, err := strconv.ParseUint(identifier, 10, 64); err == nil {
				return "", nativeSemanticVersion{}, false
			}
		}
	}
	canonical := fmt.Sprintf("%d.%d.%d", values[0], values[1], values[2])
	if match[4] != "" {
		canonical += "-" + match[4]
	}
	if match[5] != "" {
		canonical += "+" + match[5]
	}
	return canonical, nativeSemanticVersion{components: values, prerelease: match[4]}, true
}

func compareNativeVersionFloor(left nativeSemanticVersion, right [3]uint64) int {
	for index := range left.components {
		if left.components[index] < right[index] {
			return -1
		}
		if left.components[index] > right[index] {
			return 1
		}
	}
	if left.prerelease != "" {
		return -1
	}
	return 0
}

type boundedNativeOutput struct {
	buffer   bytes.Buffer
	max      int
	overflow bool
}

func (b *boundedNativeOutput) Write(value []byte) (int, error) {
	written := len(value)
	remaining := b.max - b.buffer.Len()
	if remaining < len(value) {
		b.overflow = true
	}
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		_, _ = b.buffer.Write(value[:remaining])
	}
	return written, nil
}

func (b *boundedNativeOutput) String() string { return b.buffer.String() }
