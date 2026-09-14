package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

const (
	defaultListen  = "127.0.0.1:8080"
	defaultDataDir = "pocket_gateway_data"
)

type Config struct {
	Listen    string
	DataDir   string
	PublicURL string
}

func ParseServe(args []string, getenv func(string) string, output io.Writer) (Config, error) {
	cfg := Config{
		Listen:    valueOr(getenv("POCKET_AI_GATEWAY_LISTEN"), defaultListen),
		DataDir:   valueOr(getenv("POCKET_AI_GATEWAY_DATA_DIR"), defaultDataDir),
		PublicURL: strings.TrimSpace(getenv("POCKET_AI_GATEWAY_PUBLIC_URL")),
	}

	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.Listen, "listen", cfg.Listen, "address to listen on")
	flags.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for local gateway data")
	flags.StringVar(&cfg.PublicURL, "public-url", cfg.PublicURL, "public origin used for host, origin, and secure-cookie checks")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}

	cfg.Listen = strings.TrimSpace(cfg.Listen)
	if cfg.Listen == "" {
		return Config{}, errors.New("listen address cannot be empty")
	}
	listenHost, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return Config{}, fmt.Errorf("invalid listen address: %w", err)
	}
	if cfg.PublicURL != "" {
		parsed, err := url.Parse(cfg.PublicURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Config{}, errors.New("public URL must be an HTTP or HTTPS origin without a path")
		}
		cfg.PublicURL = parsed.Scheme + "://" + parsed.Host
	}
	address := net.ParseIP(listenHost)
	networkFacing := listenHost != "localhost" && (address == nil || !address.IsLoopback())
	if networkFacing {
		if cfg.PublicURL == "" {
			return Config{}, errors.New("public URL is required when listening beyond loopback")
		}
		if !strings.HasPrefix(cfg.PublicURL, "https://") {
			return Config{}, errors.New("public URL must use HTTPS when listening beyond loopback")
		}
	}

	cfg.DataDir = strings.TrimSpace(cfg.DataDir)
	if cfg.DataDir == "" {
		return Config{}, errors.New("data directory cannot be empty")
	}
	absolute, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data directory: %w", err)
	}
	cfg.DataDir = absolute

	return cfg, nil
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
