package preset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetch_LocalFile(t *testing.T) {
	dir := t.TempDir()
	content := "version: \"1\"\nruntime: claude\n"
	path := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	data, err := Fetch(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestFetch_LocalFileMissing(t *testing.T) {
	_, err := Fetch(context.Background(), "/nonexistent/path/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading preset file")
}

func TestFetch_LocalFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	require.NoError(t, os.WriteFile(path, []byte{}, 0o644))

	_, err := Fetch(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is empty")
}

func TestFetch_HTTPS(t *testing.T) {
	content := "version: \"1\"\nruntime: claude\n"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	data, err := Fetch(context.Background(), srv.URL+"/preset.yaml")
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestFetch_HTTPSNotFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	_, err := Fetch(context.Background(), srv.URL+"/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestFetch_HTTPSEmpty(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	_, err := Fetch(context.Background(), srv.URL+"/empty.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is empty")
}

func TestFetch_HTTPSUnavailable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version: \"1\"\n"))
	}))
	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()
	url := srv.URL + "/preset.yaml"
	srv.Close()

	_, err := Fetch(context.Background(), url)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching preset")
}

func TestFetch_UnsupportedScheme(t *testing.T) {
	_, err := Fetch(context.Background(), "ftp://example.com/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestFetch_HTTPSchemeRejected(t *testing.T) {
	_, err := Fetch(context.Background(), "http://example.com/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestFetch_LocalFileExceedsMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.yaml")
	data := make([]byte, maxSize+1)
	for i := range data {
		data[i] = 'a'
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))

	_, err := Fetch(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestFetch_HTTPSExceedsMaxSize(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, maxSize+1))
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	_, err := Fetch(context.Background(), srv.URL+"/large.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestFetch_HTTPSRedirectToHTTP_Rejected(t *testing.T) {
	httpTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not reach here"))
	}))
	defer httpTarget.Close()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpTarget.URL+"/preset.yaml", http.StatusFound)
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	_, err := Fetch(context.Background(), srv.URL+"/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-HTTPS")
}

func TestValidateHash_Match(t *testing.T) {
	data := []byte("hello world")
	hash := sha256.Sum256(data)
	hexHash := hex.EncodeToString(hash[:])

	err := ValidateHash(data, hexHash)
	require.NoError(t, err)
}

func TestValidateHash_MatchUppercase(t *testing.T) {
	data := []byte("hello world")
	hash := sha256.Sum256(data)
	hexHash := strings.ToUpper(hex.EncodeToString(hash[:]))

	err := ValidateHash(data, hexHash)
	require.NoError(t, err)
}

func TestValidateHash_Mismatch(t *testing.T) {
	data := []byte("hello world")
	wrongHash := strings.Repeat("ab", 32)

	err := ValidateHash(data, wrongHash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset hash mismatch")
}

func TestValidateHash_InvalidLength(t *testing.T) {
	err := ValidateHash([]byte("data"), "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "64-character")
}

func TestValidateHash_InvalidHex(t *testing.T) {
	err := ValidateHash([]byte("data"), strings.Repeat("zz", 32))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid hex")
}

func TestValidateYAML_Valid(t *testing.T) {
	err := ValidateYAML([]byte("version: \"1\"\nruntime: claude\n"))
	require.NoError(t, err)
}

func TestValidateYAML_Invalid(t *testing.T) {
	err := ValidateYAML([]byte(":\n  - :\n  bad: [unclosed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid YAML")
}

func TestIsRemote(t *testing.T) {
	assert.True(t, IsRemote("https://example.com/preset.yaml"))
	assert.True(t, IsRemote("http://example.com/preset.yaml"))
	assert.False(t, IsRemote("/local/path/preset.yaml"))
	assert.False(t, IsRemote("relative/path.yaml"))
	assert.False(t, IsRemote("://not-a-url"))
}

func TestLoad_LocalWithHash(t *testing.T) {
	content := "version: \"1\"\nruntime: claude\n"
	hash := sha256.Sum256([]byte(content))
	path := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	data, err := Load(context.Background(), path, hex.EncodeToString(hash[:]))
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestLoad_InvalidHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: \"1\"\n"), 0o644))

	_, err := Load(context.Background(), path, strings.Repeat("ab", 32))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset hash mismatch")
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(":\n  - :\n"), 0o644))

	_, err := Load(context.Background(), path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid YAML")
}

func TestLoad_FetchError(t *testing.T) {
	_, err := Load(context.Background(), "/nonexistent/path/preset.yaml", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading preset file")
}

func TestFetch_InvalidHTTPSURL(t *testing.T) {
	_, err := Fetch(context.Background(), "https://example.com/preset.yaml\x00")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching preset")
}

func TestFetch_CanceledContext(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version: \"1\"\n"))
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Fetch(ctx, srv.URL+"/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching preset")
}

func TestFetch_HTTPSTooManyRedirects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.String(), http.StatusFound)
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	_, err := Fetch(context.Background(), srv.URL+"/loop.yaml")
	require.Error(t, err)
}

func TestApply_ByteForByteInstall(t *testing.T) {
	t.Parallel()
	// Comments and ordering must survive; Apply must not re-encode YAML.
	preset := []byte("# vendor comment\nversion: \"1\"\nruntime: claude\nroles:\n  - triage\n")
	overlay := []byte("version: \"1\"\nruntime: pi\n# repo overlay\n")

	plan := Apply(preset, nil, overlay)
	assert.Equal(t, preset, plan.Base, "base must be the preset bytes, not a merge or remarshal")
	assert.Equal(t, overlay, plan.Overlay, "overlay must be preserved unchanged")
	assert.True(t, plan.BaseChanged)
	assert.Equal(t, BasePath, ".fullsend/config.base.yaml")
	assert.Equal(t, OverlayPath, ".fullsend/config.yaml")
}

func TestApply_ReplacesChangedBase(t *testing.T) {
	t.Parallel()
	old := []byte("version: \"1\"\nruntime: claude\nroles:\n  - triage\n")
	overlay := []byte("version: \"1\"\n# keep me\nkill_switch: true\n")
	next := []byte("version: \"1\"\nruntime: pi\n")

	plan := Apply(next, old, overlay)
	assert.Equal(t, next, plan.Base, "changed source replaces the complete base; leftover keys must not remain")
	assert.Equal(t, overlay, plan.Overlay)
	assert.True(t, plan.BaseChanged)
}

func TestApply_UnchangedBase(t *testing.T) {
	t.Parallel()
	content := []byte("version: \"1\"\nruntime: claude\n")
	overlay := []byte("version: \"1\"\n")

	plan := Apply(content, content, overlay)
	assert.Equal(t, content, plan.Base)
	assert.Equal(t, overlay, plan.Overlay)
	assert.False(t, plan.BaseChanged)
}

func TestApply_DoesNotMutateInputs(t *testing.T) {
	t.Parallel()
	preset := []byte("version: \"1\"\n")
	existing := []byte("old\n")
	overlay := []byte("overlay\n")

	plan := Apply(preset, existing, overlay)
	plan.Base[0] = 'X'
	plan.Overlay[0] = 'Y'
	assert.Equal(t, "version: \"1\"\n", string(preset))
	assert.Equal(t, "overlay\n", string(overlay))
}
