package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// ResolveDatabaseURL accepts a connection secret or standard Postgres environment settings.
func ResolveDatabaseURL(env string) (string, error) {
	value, present, err := lookupSecretEnvValue(env)
	if err != nil || present {
		return value, err
	}
	host, user, database := os.Getenv("PGHOST"), os.Getenv("PGUSER"), os.Getenv("PGDATABASE")
	if host == "" || user == "" || database == "" {
		return "", fmt.Errorf(
			"configure %s (or %s_FILE), or PGHOST, PGUSER, PGDATABASE and PGPASSWORD_FILE",
			env,
			env,
		)
	}
	password, err := resolveSecretEnvValue("PGPASSWORD")
	if err != nil {
		return "", err
	}
	if port := os.Getenv("PGPORT"); port != "" {
		host = net.JoinHostPort(host, port)
	}
	sslMode := strings.TrimSpace(os.Getenv("PGSSLMODE"))
	if sslMode == "" {
		sslMode = "require"
	}
	connection := url.URL{
		Scheme:   "postgresql",
		Host:     host,
		User:     url.UserPassword(user, password),
		Path:     "/" + database,
		RawQuery: url.Values{"sslmode": []string{sslMode}}.Encode(),
	}
	return connection.String(), nil
}
