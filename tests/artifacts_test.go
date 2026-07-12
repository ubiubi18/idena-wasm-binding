package tests

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var requiredArtifacts = map[string]struct{}{
	"libidena_wasm_darwin_amd64.a":  {},
	"libidena_wasm_darwin_arm64.a":  {},
	"libidena_wasm_linux_aarch64.a": {},
	"libidena_wasm_linux_amd64.a":   {},
	"libidena_wasm_windows_amd64.a": {},
}

type compatibilityLock struct {
	Components []struct {
		Name   string `json:"name"`
		Commit string `json:"commit"`
	} `json:"components"`
	Artifacts []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
}

func TestStaticLibraryArtifactChecksums(t *testing.T) {
	libDir := filepath.Join(repositoryRoot(t), "lib")
	manifest, err := os.Open(filepath.Join(libDir, "SHA256SUMS"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manifest.Close()) })

	found := make(map[string]struct{}, len(requiredArtifacts))
	scanner := bufio.NewScanner(manifest)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		require.Len(t, fields, 2, "invalid SHA256SUMS line")
		checksum, artifactName := fields[0], fields[1]
		_, expected := requiredArtifacts[artifactName]
		require.True(t, expected, "unexpected artifact %q", artifactName)
		_, duplicate := found[artifactName]
		require.False(t, duplicate, "duplicate artifact %q", artifactName)

		decoded, err := hex.DecodeString(checksum)
		require.NoError(t, err, "invalid checksum for %s", artifactName)
		require.Len(t, decoded, sha256.Size, "invalid checksum for %s", artifactName)
		require.Equal(t, checksum, fileSHA256(t, filepath.Join(libDir, artifactName)), artifactName)
		found[artifactName] = struct{}{}
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, requiredArtifacts, found)
}

func TestStaticLibraryArtifactSource(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "lib", "ARTIFACTS_SOURCE"))
	require.NoError(t, err)

	values := parseArtifactSource(t, data)
	require.Equal(t, "https://github.com/ubiubi18/idena-wasm", values["repository"])
	requireCommitHash(t, values["idena_wasm_revision"])
	requireCommitHash(t, values["wasmer_revision"])
	_, err = strconv.ParseUint(values["workflow_run"], 10, 64)
	require.NoError(t, err)
	require.Equal(t, "1.97.0", values["rust_toolchain"])
	require.Len(t, values, 5)
}

func TestCompatibilityStackMatchesArtifactProvenance(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "compatibility", "stack-lock.json"))
	require.NoError(t, err)
	var lock compatibilityLock
	require.NoError(t, json.Unmarshal(raw, &lock))

	components := make(map[string]string, len(lock.Components))
	for _, component := range lock.Components {
		require.NotEmpty(t, component.Name)
		requireCommitHash(t, component.Commit)
		_, duplicate := components[component.Name]
		require.False(t, duplicate, "duplicate component %q", component.Name)
		components[component.Name] = component.Commit
	}
	require.Equal(t, "67ba065fdb02aa07cced2a43a261e481ca5b39d9", components["idena-wasm-binding"])

	sourceRaw, err := os.ReadFile(filepath.Join(root, "lib", "ARTIFACTS_SOURCE"))
	require.NoError(t, err)
	source := parseArtifactSource(t, sourceRaw)
	require.Equal(t, components["idena-wasm"], source["idena_wasm_revision"])
	require.Equal(t, components["wasmer"], source["wasmer_revision"])

	lockedArtifacts := make(map[string]string, len(lock.Artifacts))
	for _, artifact := range lock.Artifacts {
		decoded, err := hex.DecodeString(artifact.SHA256)
		require.NoError(t, err)
		require.Len(t, decoded, sha256.Size)
		_, duplicate := lockedArtifacts[artifact.Name]
		require.False(t, duplicate, "duplicate locked artifact %q", artifact.Name)
		lockedArtifacts[artifact.Name] = artifact.SHA256
	}

	manifest, err := os.Open(filepath.Join(root, "lib", "SHA256SUMS"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manifest.Close()) })
	manifestArtifacts := make(map[string]string)
	scanner := bufio.NewScanner(manifest)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		require.Len(t, fields, 2)
		manifestArtifacts[fields[1]] = fields[0]
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, lockedArtifacts, manifestArtifacts)
}

func TestArtifactSourceParserAcceptsCRLF(t *testing.T) {
	values := parseArtifactSource(t, []byte("repository=https://example.com/repo\r\nrevision=abc123\r\n"))
	require.Equal(t, "https://example.com/repo", values["repository"])
	require.Equal(t, "abc123", values["revision"])
}

func parseArtifactSource(t *testing.T, data []byte) map[string]string {
	t.Helper()
	values := make(map[string]string)
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "invalid ARTIFACTS_SOURCE line %d", lineNumber+1)
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		require.NotEmpty(t, key)
		require.NotEmpty(t, value)
		_, duplicate := values[key]
		require.False(t, duplicate, "duplicate source key %q", key)
		values[key] = value
	}
	return values
}

func TestWindowsStaticLibraryNativeDependencies(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "lib", "link_std_windows.go"))
	require.NoError(t, err)

	linkFlags := string(data)
	for _, library := range []string{"-lws2_32", "-lbcrypt", "-luserenv", "-lntdll"} {
		require.Contains(t, linkFlags, library)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Dir(filepath.Dir(sourceFile))
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()

	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, err)
	return hex.EncodeToString(hash.Sum(nil))
}

func requireCommitHash(t *testing.T, value string) {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err, fmt.Sprintf("invalid commit hash %q", value))
	require.Len(t, decoded, 20, "invalid commit hash %q", value)
}
