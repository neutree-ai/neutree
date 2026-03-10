package util

import (
	"net/url"
	"strconv"

	"github.com/pkg/errors"
)

// URLComponents holds the parsed components of a URL
type URLComponents struct {
	Scheme string
	Host   string
	Port   int
	Path   string
}

// ParseURLComponents parses a URL string into its scheme, host, port, and path.
// Defaults port to 443 for HTTPS and 80 for HTTP when not specified.
func ParseURLComponents(rawURL string) (*URLComponents, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse URL: %s", rawURL)
	}

	scheme := u.Scheme
	if scheme != SchemeHTTP && scheme != SchemeHTTPS {
		return nil, errors.Errorf("unsupported or missing scheme in URL: %s", rawURL)
	}

	host := u.Hostname()
	if host == "" {
		return nil, errors.Errorf("missing host in URL: %s", rawURL)
	}

	port := 443

	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil {
			return nil, errors.Wrapf(err, "invalid port in URL: %s", rawURL)
		}

		port = p
	} else if scheme == SchemeHTTP {
		port = 80
	}

	path := u.Path
	if path == "" {
		path = "/"
	}

	return &URLComponents{
		Scheme: scheme,
		Host:   host,
		Port:   port,
		Path:   path,
	}, nil
}
