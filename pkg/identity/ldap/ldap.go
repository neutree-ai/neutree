// Package ldap authenticates users against an LDAP directory (OpenLDAP, Active Directory)
// with the search-then-bind flow and maps the user entry to an Identity.
//
// The package is deliberately self-contained: it imports only the standard library and
// github.com/go-ldap/ldap/v3, so it can be reused outside neutree. It does no logging;
// callers log the returned errors, which never contain the password.
package ldap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

// UsernamePlaceholder is replaced in Config.UserFilter with the escaped login name.
const UsernamePlaceholder = "{username}"

// DefaultTimeout applies to dialing and to each LDAP operation when Config.Timeout is zero.
const DefaultTimeout = 10 * time.Second

const objectGUIDAttribute = "objectGUID"

var (
	// ErrInvalidConfig means the Config failed validation.
	ErrInvalidConfig = errors.New("ldap: invalid config")
	// ErrInvalidCredentials means the user bind was rejected (LDAP result code 49) or the
	// supplied credentials were empty.
	ErrInvalidCredentials = errors.New("ldap: invalid credentials")
	// ErrEmptyUsername and ErrEmptyPassword are rejected before any network call. Both also
	// match ErrInvalidCredentials. An empty password must never reach the server: RFC 4513
	// treats it as an unauthenticated bind, which many servers accept.
	ErrEmptyUsername = fmt.Errorf("%w: empty username", ErrInvalidCredentials)
	ErrEmptyPassword = fmt.Errorf("%w: empty password", ErrInvalidCredentials)
	// ErrUserNotFound means the user search returned no entry.
	ErrUserNotFound = errors.New("ldap: user not found")
	// ErrMultipleUsers means the user search matched more than one entry.
	ErrMultipleUsers = errors.New("ldap: multiple users match")
	// ErrServiceBindFailed means the server rejected the service account bind.
	ErrServiceBindFailed = errors.New("ldap: service account bind failed")
	// ErrMissingAttribute means the user entry lacks a required attribute (ID or username),
	// or the ID value is malformed.
	ErrMissingAttribute = errors.New("ldap: user entry is missing a required attribute")
	// ErrConnection covers dial failures, network errors, timeouts and context cancellation.
	ErrConnection = errors.New("ldap: connection failed")
	// ErrTLS covers TLS handshake and certificate verification failures.
	ErrTLS = errors.New("ldap: TLS failed")
)

// Config describes how to reach the directory and how to find users in it.
type Config struct {
	// URL is ldap://host:389 or ldaps://host:636.
	URL string
	// StartTLS upgrades an ldap:// connection to TLS. It cannot be combined with ldaps://.
	StartTLS bool
	// RootCAs is a PEM bundle used to verify the server certificate; empty uses the system pool.
	RootCAs            []byte
	InsecureSkipVerify bool
	// Timeout bounds dialing and each LDAP operation; zero means DefaultTimeout.
	Timeout time.Duration

	// BindDN and BindPassword are the service account used to search for users.
	BindDN       string
	BindPassword string

	UserBaseDN string
	// UserFilter must contain UsernamePlaceholder, e.g. "(&(objectClass=inetOrgPerson)(uid={username}))".
	UserFilter string
	Attributes AttributeMapping
}

// AttributeMapping names the directory attributes read into an Identity.
// ID and Username are required; the others are optional and skipped when empty.
type AttributeMapping struct {
	// ID is a stable identifier: "entryUUID" (OpenLDAP) or "objectGUID" (AD, decoded to a GUID string).
	ID string
	// Username is e.g. "uid" or "sAMAccountName".
	Username    string
	Email       string
	DisplayName string
	// MemberOf is e.g. "memberOf"; empty means groups are not read.
	MemberOf string
}

// Identity is the authenticated user as described by the directory.
type Identity struct {
	ExternalID  string
	DN          string
	Username    string
	Email       string
	DisplayName string
	// Groups holds the raw group DNs from the MemberOf attribute; nil when none.
	Groups []string
}

// Dialer opens a connection to the directory. NewDialer returns the real implementation;
// tests inject fakes.
type Dialer func(ctx context.Context) (goldap.Client, error)

// Authenticator authenticates users with the search-then-bind flow.
type Authenticator struct {
	cfg  Config
	dial Dialer
}

// Validate reports whether the config is usable.
func (c Config) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("%w: parse url: %w", ErrInvalidConfig, err)
	}

	switch u.Scheme {
	case "ldap":
	case "ldaps":
		if c.StartTLS {
			return fmt.Errorf("%w: StartTLS cannot be used with ldaps://", ErrInvalidConfig)
		}
	default:
		return fmt.Errorf("%w: url scheme must be ldap or ldaps, got %q", ErrInvalidConfig, u.Scheme)
	}

	if u.Hostname() == "" {
		return fmt.Errorf("%w: url has no host", ErrInvalidConfig)
	}

	if c.Timeout < 0 {
		return fmt.Errorf("%w: timeout must not be negative", ErrInvalidConfig)
	}

	required := []struct{ name, value string }{
		{"bind DN", c.BindDN},
		{"bind password", c.BindPassword},
		{"user base DN", c.UserBaseDN},
		{"user filter", c.UserFilter},
		{"ID attribute", c.Attributes.ID},
		{"username attribute", c.Attributes.Username},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidConfig, field.name)
		}
	}

	if !strings.Contains(c.UserFilter, UsernamePlaceholder) {
		return fmt.Errorf("%w: user filter must contain %s", ErrInvalidConfig, UsernamePlaceholder)
	}

	if _, err := c.tlsConfig(); err != nil {
		return err
	}

	return nil
}

func (c Config) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}

	return DefaultTimeout
}

func (c Config) tlsConfig() (*tls.Config, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: parse url: %w", ErrInvalidConfig, err)
	}

	cfg := &tls.Config{
		ServerName:         u.Hostname(),
		InsecureSkipVerify: c.InsecureSkipVerify, //nolint:gosec // explicit opt-in by the administrator
		MinVersion:         tls.VersionTLS12,
	}

	if len(c.RootCAs) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(c.RootCAs) {
			return nil, fmt.Errorf("%w: root CAs contain no valid PEM certificate", ErrInvalidConfig)
		}

		cfg.RootCAs = pool
	}

	return cfg, nil
}

// NewDialer returns a Dialer that connects to cfg.URL, verifying TLS with cfg.RootCAs and
// upgrading with StartTLS when configured. The context deadline, if any, bounds the dial.
func NewDialer(cfg Config) Dialer {
	return func(ctx context.Context) (goldap.Client, error) {
		tlsCfg, err := cfg.tlsConfig()
		if err != nil {
			return nil, err
		}

		dialer := &net.Dialer{Timeout: cfg.timeout()}
		if deadline, ok := ctx.Deadline(); ok {
			dialer.Deadline = deadline
		}

		conn, err := goldap.DialURL(cfg.URL, goldap.DialWithDialer(dialer), goldap.DialWithTLSConfig(tlsCfg))
		if err != nil {
			return nil, connectionError("dial", err)
		}

		conn.SetTimeout(cfg.timeout())

		if cfg.StartTLS {
			if err := conn.StartTLS(tlsCfg); err != nil {
				_ = conn.Close()
				// go-ldap flattens the handshake error into a string, so the cause type is lost;
				// any StartTLS failure is reported as a TLS failure.
				return nil, fmt.Errorf("%w: StartTLS: %w", ErrTLS, err)
			}
		}

		return conn, nil
	}
}

// New validates cfg and returns an Authenticator using dial to reach the directory.
func New(cfg Config, dial Dialer) (*Authenticator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if dial == nil {
		return nil, fmt.Errorf("%w: dialer is required", ErrInvalidConfig)
	}

	return &Authenticator{cfg: cfg, dial: dial}, nil
}

// Authenticate verifies username/password and returns the user's identity.
//
// It binds as the service account, searches for exactly one user entry, then rebinds on the
// same connection as that entry's DN with the password. The connection is closed before
// returning on every path, so it is never reused while bound as the user.
func (a *Authenticator) Authenticate(ctx context.Context, username, password string) (*Identity, error) {
	if strings.TrimSpace(username) == "" {
		return nil, ErrEmptyUsername
	}

	if password == "" {
		return nil, ErrEmptyPassword
	}

	conn, err := a.dial(ctx)
	if err != nil {
		if errors.Is(err, ErrConnection) || errors.Is(err, ErrTLS) || errors.Is(err, ErrInvalidConfig) {
			return nil, err
		}

		return nil, connectionError("dial", err)
	}

	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { _ = conn.Close() }) }

	defer closeConn()

	// Closing the connection aborts any in-flight operation when ctx is done.
	stop := context.AfterFunc(ctx, closeConn)
	defer stop()

	if err := conn.Bind(a.cfg.BindDN, a.cfg.BindPassword); err != nil {
		return nil, a.operationError(ctx, ErrServiceBindFailed, "service bind", err)
	}

	entry, err := a.searchUser(ctx, conn, username)
	if err != nil {
		return nil, err
	}

	if err := conn.Bind(entry.DN, password); err != nil {
		if goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials) {
			return nil, fmt.Errorf("%w: %w", ErrInvalidCredentials, err)
		}

		return nil, a.operationError(ctx, nil, "user bind", err)
	}

	return a.mapEntry(entry)
}

func (a *Authenticator) searchUser(ctx context.Context, conn goldap.Client, username string) (*goldap.Entry, error) {
	filter := strings.ReplaceAll(a.cfg.UserFilter, UsernamePlaceholder, goldap.EscapeFilter(username))
	req := goldap.NewSearchRequest(
		a.cfg.UserBaseDN,
		goldap.ScopeWholeSubtree,
		goldap.NeverDerefAliases,
		2, // one match is expected; a second one is enough to detect ambiguity
		int(a.cfg.timeout().Seconds()),
		false,
		filter,
		a.requestedAttributes(),
		nil,
	)

	result, err := conn.Search(req)
	if err != nil {
		// With SizeLimit 2 the server answers sizeLimitExceeded once a second entry exists.
		if goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) && result != nil && len(result.Entries) > 1 {
			return nil, ErrMultipleUsers
		}

		return nil, a.operationError(ctx, nil, "search user", err)
	}

	switch len(result.Entries) {
	case 0:
		return nil, ErrUserNotFound
	case 1:
		return result.Entries[0], nil
	default:
		return nil, ErrMultipleUsers
	}
}

func (a *Authenticator) requestedAttributes() []string {
	m := a.cfg.Attributes
	attrs := make([]string, 0, 5)

	for _, name := range []string{m.ID, m.Username, m.Email, m.DisplayName, m.MemberOf} {
		if name == "" {
			continue
		}

		duplicate := false

		for _, existing := range attrs {
			if strings.EqualFold(existing, name) {
				duplicate = true
				break
			}
		}

		if !duplicate {
			attrs = append(attrs, name)
		}
	}

	return attrs
}

func (a *Authenticator) mapEntry(entry *goldap.Entry) (*Identity, error) {
	m := a.cfg.Attributes

	id, err := externalID(entry, m.ID)
	if err != nil {
		return nil, err
	}

	identity := &Identity{
		ExternalID: id,
		DN:         entry.DN,
		Username:   entry.GetEqualFoldAttributeValue(m.Username),
	}

	if identity.Username == "" {
		return nil, fmt.Errorf("%w: %s", ErrMissingAttribute, m.Username)
	}

	if m.Email != "" {
		identity.Email = entry.GetEqualFoldAttributeValue(m.Email)
	}

	if m.DisplayName != "" {
		identity.DisplayName = entry.GetEqualFoldAttributeValue(m.DisplayName)
	}

	if m.MemberOf != "" {
		if groups := entry.GetEqualFoldAttributeValues(m.MemberOf); len(groups) > 0 {
			identity.Groups = groups
		}
	}

	return identity, nil
}

func externalID(entry *goldap.Entry, attribute string) (string, error) {
	if strings.EqualFold(attribute, objectGUIDAttribute) {
		raw := entry.GetEqualFoldRawAttributeValues(attribute)
		if len(raw) == 0 {
			return "", fmt.Errorf("%w: %s", ErrMissingAttribute, attribute)
		}

		return formatGUID(raw[0])
	}

	id := entry.GetEqualFoldAttributeValue(attribute)
	if id == "" {
		return "", fmt.Errorf("%w: %s", ErrMissingAttribute, attribute)
	}

	return id, nil
}

// formatGUID renders a 16-byte AD objectGUID in canonical form. Per MS-DTYP the first
// three fields are stored little-endian and the last two as-is.
func formatGUID(b []byte) (string, error) {
	if len(b) != 16 {
		return "", fmt.Errorf("%w: objectGUID has %d bytes, want 16", ErrMissingAttribute, len(b))
	}

	return fmt.Sprintf("%08x-%04x-%04x-%x-%x",
		binary.LittleEndian.Uint32(b[0:4]),
		binary.LittleEndian.Uint16(b[4:6]),
		binary.LittleEndian.Uint16(b[6:8]),
		b[8:10],
		b[10:16],
	), nil
}

// operationError classifies a failed LDAP operation: cancellation and network/TLS failures
// win over the operation-specific sentinel, which may be nil for "no specific class".
func (a *Authenticator) operationError(ctx context.Context, sentinel error, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%w: %s: %w", ErrConnection, op, ctxErr)
	}

	if isTLSError(err) || isNetworkError(err) {
		return connectionError(op, err)
	}

	if sentinel == nil {
		return fmt.Errorf("ldap: %s: %w", op, err)
	}

	return fmt.Errorf("%w: %w", sentinel, err)
}

// connectionError wraps err as ErrTLS when it is a TLS failure, otherwise as ErrConnection.
func connectionError(op string, err error) error {
	if isTLSError(err) {
		return fmt.Errorf("%w: %s: %w", ErrTLS, op, err)
	}

	return fmt.Errorf("%w: %s: %w", ErrConnection, op, err)
}

func isTLSError(err error) bool {
	var (
		verifyErr      *tls.CertificateVerificationError
		recordErr      tls.RecordHeaderError
		alertErr       tls.AlertError
		unknownAuthErr x509.UnknownAuthorityError
		invalidErr     x509.CertificateInvalidError
		hostnameErr    x509.HostnameError
	)

	return errors.As(err, &verifyErr) ||
		errors.As(err, &recordErr) ||
		errors.As(err, &alertErr) ||
		errors.As(err, &unknownAuthErr) ||
		errors.As(err, &invalidErr) ||
		errors.As(err, &hostnameErr)
}

func isNetworkError(err error) bool {
	var netErr net.Error

	return goldap.IsErrorWithCode(err, goldap.ErrorNetwork) || errors.As(err, &netErr)
}
