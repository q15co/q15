package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/q15co/q15/libs/proxy-contract/proxypb"
	"github.com/q15co/q15/systems/exec-service/internal/proxyclient"
)

const proxyAdminConnectTimeout = 5 * time.Second

var systemCABundleCandidates = []string{
	"SSL_CERT_FILE",
	"NIX_SSL_CERT_FILE",
}

var systemCABundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/ssl/certs/ca-bundle.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/cert.pem",
}

type proxySessionProfile struct {
	Enabled        bool
	PolicyRevision string
	Env            []string
}

func bootstrapProxyRuntime(
	ctx context.Context,
	adminAddress string,
) (proxySessionProfile, func(), error) {
	adminAddress = strings.TrimSpace(adminAddress)
	if adminAddress == "" {
		return proxySessionProfile{}, func() {}, nil
	}

	connectCtx, cancel := context.WithTimeout(ctx, proxyAdminConnectTimeout)
	defer cancel()

	client, err := proxyclient.New(connectCtx, adminAddress)
	if err != nil {
		return proxySessionProfile{}, func() {}, err
	}
	defer client.Close()

	info, err := client.GetRuntimeInfo(connectCtx)
	if err != nil {
		return proxySessionProfile{}, func() {}, fmt.Errorf("get proxy runtime info: %w", err)
	}
	if strings.TrimSpace(info.GetAdvertiseProxyUrl()) == "" {
		return proxySessionProfile{}, func() {}, fmt.Errorf("proxy advertise URL is required")
	}

	caPath, cleanup, err := writeProxyCABundle(info.GetCaCertPem())
	if err != nil {
		return proxySessionProfile{}, func() {}, err
	}

	return proxySessionProfile{
		Enabled:        true,
		PolicyRevision: strings.TrimSpace(info.GetPolicyRevision()),
		Env:            buildProxyEnv(info, caPath),
	}, cleanup, nil
}

func writeProxyCABundle(caPEM []byte) (string, func(), error) {
	if len(caPEM) == 0 {
		return "", func() {}, nil
	}
	dir, err := os.MkdirTemp("", "q15-exec-service-proxy-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create proxy CA temp dir: %w", err)
	}
	path := filepath.Join(dir, "ca.crt")
	bundle, err := buildProxyCABundle(caPEM)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, err
	}
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("write proxy CA bundle: %w", err)
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

func buildProxyCABundle(caPEM []byte) ([]byte, error) {
	caPEM = bytes.TrimSpace(caPEM)
	if len(caPEM) == 0 {
		return nil, nil
	}

	systemBundle, err := readSystemCABundle()
	if err != nil {
		return nil, err
	}

	bundle := make([]byte, 0, len(systemBundle)+len(caPEM)+2)
	if len(systemBundle) > 0 {
		bundle = append(bundle, bytes.TrimSpace(systemBundle)...)
		bundle = append(bundle, '\n')
	}
	bundle = append(bundle, caPEM...)
	bundle = append(bundle, '\n')
	return bundle, nil
}

func readSystemCABundle() ([]byte, error) {
	for _, envName := range systemCABundleCandidates {
		path := strings.TrimSpace(os.Getenv(envName))
		if bundle, ok, err := tryReadSystemCABundle(path); ok || err != nil {
			return bundle, err
		}
	}
	for _, path := range systemCABundlePaths {
		if bundle, ok, err := tryReadSystemCABundle(path); ok || err != nil {
			return bundle, err
		}
	}
	return nil, nil
}

func tryReadSystemCABundle(path string) ([]byte, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read system CA bundle %q: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, false, nil
	}
	return data, true, nil
}

func buildProxyEnv(info *proxypb.GetRuntimeInfoResponse, caPath string) []string {
	if info == nil {
		return nil
	}

	proxyURL := strings.TrimSpace(info.GetAdvertiseProxyUrl())
	noProxy := strings.TrimSpace(info.GetNoProxy())
	env := make([]string, 0, len(info.GetEnvValues())+16)
	appendKV := func(key string, value string) {
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return
		}
		env = append(env, key+"="+value)
	}

	keys := make([]string, 0, len(info.GetEnvValues()))
	for key := range info.GetEnvValues() {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		appendKV(key, info.GetEnvValues()[key])
	}

	appendKV("HTTP_PROXY", proxyURL)
	appendKV("HTTPS_PROXY", proxyURL)
	appendKV("ALL_PROXY", proxyURL)
	appendKV("NO_PROXY", noProxy)
	if info.GetSetLowercaseProxyEnv() {
		appendKV("http_proxy", proxyURL)
		appendKV("https_proxy", proxyURL)
		appendKV("all_proxy", proxyURL)
		appendKV("no_proxy", noProxy)
	}
	if strings.TrimSpace(caPath) != "" {
		for _, key := range []string{
			"SSL_CERT_FILE",
			"NIX_SSL_CERT_FILE",
			"NODE_EXTRA_CA_CERTS",
			"REQUESTS_CA_BUNDLE",
			"CURL_CA_BUNDLE",
			"GIT_SSL_CAINFO",
		} {
			appendKV(key, caPath)
		}
	}
	return env
}
