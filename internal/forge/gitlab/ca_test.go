package gitlab

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePEMCert(t *testing.T, dir, name string, cert *x509.Certificate) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}), 0o644))
	return path
}

func tlsClientConfig(t *testing.T, client *http.Client) *tls.Config {
	t.Helper()
	rt := client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	tr, ok := rt.(*http.Transport)
	require.True(t, ok, "expected *http.Transport, got %T", rt)
	require.NotNil(t, tr.TLSClientConfig)
	return tr.TLSClientConfig
}

func TestApplyCIServerTLSCA_UnsetIsNoop(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, "")
	client := &http.Client{}
	require.NoError(t, applyCIServerTLSCA(client))
	assert.Nil(t, client.Transport, "unset CA must not install a custom transport")
}

func TestApplyCIServerTLSCA_MissingFile(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, filepath.Join(t.TempDir(), "missing.pem"))
	err := applyCIServerTLSCA(&http.Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ciServerTLSCAFileEnv)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), "InsecureSkipVerify")
}

func TestApplyCIServerTLSCA_InvalidPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-cert.pem")
	require.NoError(t, os.WriteFile(path, []byte("this is not a certificate\n"), 0o644))
	t.Setenv(ciServerTLSCAFileEnv, path)
	err := applyCIServerTLSCA(&http.Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PEM certificate bundle")
	assert.Contains(t, err.Error(), "TLS verification is not disabled")
}

func TestApplyCIServerTLSCA_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.pem")
	require.NoError(t, os.WriteFile(path, []byte{}, 0o644))
	t.Setenv(ciServerTLSCAFileEnv, path)
	err := applyCIServerTLSCA(&http.Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PEM certificate bundle")
}

func TestApplyCIServerTLSCA_NilClient(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, filepath.Join(t.TempDir(), "x.pem"))
	err := applyCIServerTLSCA(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http client is nil")
}

func startUniqueTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  key,
			Leaf:        cert,
		}},
		MinVersion: tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestApplyCIServerTLSCA_TrustsPrivateCAAndRejectsUntrusted(t *testing.T) {
	trusted := startUniqueTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 1}`)
	}))

	untrusted := startUniqueTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 2}`)
	}))

	caPath := writePEMCert(t, t.TempDir(), "ci-server-ca.pem", trusted.Certificate())
	t.Setenv(ciServerTLSCAFileEnv, caPath)

	client, err := New("test-token", WithBaseURL(trusted.URL), WithAfterFunc(noWaitAfter))
	require.NoError(t, err)

	tlsCfg := tlsClientConfig(t, client.http)
	assert.False(t, tlsCfg.InsecureSkipVerify, "must not disable TLS verification")
	assert.NotNil(t, tlsCfg.RootCAs)
	assert.GreaterOrEqual(t, tlsCfg.MinVersion, uint16(tls.VersionTLS12))

	resp, err := client.http.Get(trusted.URL + "/user")
	require.NoError(t, err, "private-CA GitLab endpoint must succeed")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"id": 1`)

	_, err = client.http.Get(untrusted.URL + "/user")
	require.Error(t, err, "certificate not signed by the supplied CA must be rejected")
	assert.Contains(t, err.Error(), "certificate")
}

func TestApplyCIServerTLSCA_UnsetRejectsUnknownAuthority(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, "")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := New("test-token", WithBaseURL(srv.URL), WithAfterFunc(noWaitAfter))
	require.NoError(t, err)
	_, err = client.http.Get(srv.URL)
	require.Error(t, err, "without a custom CA, httptest cert must be untrusted")
	assert.Contains(t, err.Error(), "certificate")
}

func TestNew_PropagatesCIServerTLSCAError(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, filepath.Join(t.TempDir(), "missing.pem"))
	_, err := New("test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitlab:")
	assert.Contains(t, err.Error(), ciServerTLSCAFileEnv)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCloneHTTPTransport_NilAndNonTransport(t *testing.T) {
	cloned := cloneHTTPTransport(nil)
	require.NotNil(t, cloned)

	cloned = cloneHTTPTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, nil
	}))
	require.NotNil(t, cloned)

	cloned = cloneHTTPTransport(http.DefaultTransport)
	require.NotNil(t, cloned)
	assert.NotSame(t, http.DefaultTransport, cloned)
}

func TestApplyCIServerTLSCA_ClearsInsecureSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	caPath := writePEMCert(t, t.TempDir(), "ca.pem", srv.Certificate())
	t.Setenv(ciServerTLSCAFileEnv, caPath)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // fixture to prove we clear it
		},
	}
	require.NoError(t, applyCIServerTLSCA(client))
	cfg := tlsClientConfig(t, client)
	assert.False(t, cfg.InsecureSkipVerify, "must not preserve InsecureSkipVerify from a prior transport")
}

func TestApplyCIServerTLSCA_UnparseablePEMBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	require.NoError(t, os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\nnot-valid-base64\n-----END CERTIFICATE-----\n"), 0o644))
	t.Setenv(ciServerTLSCAFileEnv, path)
	err := applyCIServerTLSCA(&http.Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no parseable certificates")
}
