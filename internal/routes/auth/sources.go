package auth

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/identitysource"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
	"github.com/neutree-ai/neutree/pkg/identity/oidc"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// DefaultLoginSourceTTL is how long a login reuses what it read about an
// identity source before reading it again. A change to a source therefore
// reaches logins within this time, with no restart.
const DefaultLoginSourceTTL = 30 * time.Second

var (
	// errSourceNotFound covers a source that does not exist, is disabled, is
	// being deleted, or has another type than the login asks for. They are
	// not told apart: the login page only offers enabled sources anyway.
	errSourceNotFound = errors.New("identity source not found")
	// errSourceRequired means the login named no source and more than one
	// enabled source of its type exists.
	errSourceRequired = errors.New("identity source is required")
	// errSourceUnusable means the stored configuration cannot build a client.
	errSourceUnusable = errors.New("identity source configuration is unusable")
)

// LoginSources resolves the identity sources users log in with from the
// database and keeps the LDAP authenticators and OIDC relying parties built
// from them.
//
// An entry is reused for ttl after it was read. After that, the next login
// reads the source and its secrets again and keeps the built client when
// neither changed, so an OIDC provider is not discovered again just because
// time passed. A source that is gone, or a failed read, drops the entry.
type LoginSources struct {
	store storage.Storage
	ttl   time.Duration
	// codec seals the OIDC login state cookie of every source; the source
	// name is bound into each sealed state.
	codec *stateCodec
	now   func() time.Time

	newLDAP func(ldap.Config) (LDAPAuthenticator, error)
	newOIDC func(oidc.Config) (OIDCRelyingParty, error)

	mu      sync.Mutex
	entries map[string]*loginSource
}

// NewLoginSources reads identity sources from store. stateSecret keys the
// OIDC login state cookie; it is the JWT secret neutree-api shares with
// GoTrue and PostgREST, so every replica can finish a login another started.
func NewLoginSources(store storage.Storage, stateSecret string, ttl time.Duration) (*LoginSources, error) {
	codec, err := newStateCodec(stateSecret)
	if err != nil {
		return nil, err
	}

	if ttl <= 0 {
		ttl = DefaultLoginSourceTTL
	}

	return &LoginSources{
		store: store,
		ttl:   ttl,
		codec: codec,
		now:   time.Now,
		newLDAP: func(cfg ldap.Config) (LDAPAuthenticator, error) {
			return ldap.New(cfg, ldap.NewDialer(cfg))
		},
		newOIDC: func(cfg oidc.Config) (OIDCRelyingParty, error) {
			return oidc.New(cfg)
		},
		entries: map[string]*loginSource{},
	}, nil
}

// loginSource is an enabled identity source ready for logins. It is not
// changed once cached; a refresh replaces it.
type loginSource struct {
	name        string
	sourceType  v1.IdentitySourceType
	fingerprint [sha256.Size]byte
	fetchedAt   time.Time

	ldap LDAPAuthenticator
	oidc *oidcSource
}

// oidcSource is what an OIDC login needs of its source.
type oidcSource struct {
	name string
	rp   OIDCRelyingParty
	// allowedRedirects are the UI locations a login may return to; the first
	// one is the default.
	allowedRedirects []*url.URL
	// cookiePath and cookieSecure scope the state cookie to the callback of
	// spec.oidc.redirect_url.
	cookiePath   string
	cookieSecure bool
}

// resolve returns the enabled source of the given type a login asked for.
// A login that names no source gets the only enabled source of its type, so a
// single directory or provider works without the client knowing its name;
// with several, the client has to choose.
func (s *LoginSources) resolve(name string, sourceType v1.IdentitySourceType) (*loginSource, error) {
	if name == "" {
		var err error

		if name, err = s.defaultSource(sourceType); err != nil {
			return nil, err
		}
	}

	return s.get(name, sourceType)
}

func (s *LoginSources) defaultSource(sourceType v1.IdentitySourceType) (string, error) {
	enabled, err := s.store.ListLoginIdentitySources()
	if err != nil {
		return "", fmt.Errorf("list identity sources: %w", err)
	}

	var names []string

	for _, source := range enabled {
		if source.Type == sourceType {
			names = append(names, source.Name)
		}
	}

	switch len(names) {
	case 0:
		return "", errSourceNotFound
	case 1:
		return names[0], nil
	default:
		return "", errSourceRequired
	}
}

// get returns the enabled source with the given name and type.
func (s *LoginSources) get(name string, sourceType v1.IdentitySourceType) (*loginSource, error) {
	if v1.ValidateIdentitySourceName(name) != nil {
		return nil, errSourceNotFound
	}

	now := s.now()

	s.mu.Lock()
	cached := s.entries[name]
	s.mu.Unlock()

	if cached != nil && now.Sub(cached.fetchedAt) < s.ttl {
		return ofType(cached, sourceType)
	}

	entry, err := s.load(name, cached, now)

	s.mu.Lock()
	if err != nil {
		delete(s.entries, name)
	} else {
		s.entries[name] = entry
	}
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}

	return ofType(entry, sourceType)
}

func ofType(entry *loginSource, sourceType v1.IdentitySourceType) (*loginSource, error) {
	if entry.sourceType != sourceType {
		return nil, errSourceNotFound
	}

	return entry, nil
}

// load reads the source and its secrets, reusing the clients of previous when
// both are unchanged.
func (s *LoginSources) load(name string, previous *loginSource, now time.Time) (*loginSource, error) {
	sources, err := s.store.ListIdentitySource(storage.ListOption{
		Filters: []storage.Filter{{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(name)}},
	})
	if err != nil {
		return nil, fmt.Errorf("look up identity source %s: %w", name, err)
	}

	if len(sources) == 0 {
		return nil, errSourceNotFound
	}

	source := sources[0]
	if source.Metadata == nil || source.Metadata.DeletionTimestamp != "" || source.Spec == nil || !source.Spec.Enabled {
		return nil, errSourceNotFound
	}

	secrets, err := s.store.GetIdentitySourceSecrets(name)
	if errors.Is(err, storage.ErrResourceNotFound) {
		return nil, errSourceNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("read secrets of identity source %s: %w", name, err)
	}

	fingerprint := identitysource.Fingerprint(source.Spec, secrets)

	if previous != nil && previous.fingerprint == fingerprint {
		refreshed := *previous
		refreshed.fetchedAt = now

		return &refreshed, nil
	}

	entry := &loginSource{
		name:        name,
		sourceType:  source.Spec.Type,
		fingerprint: fingerprint,
		fetchedAt:   now,
	}

	if err := s.build(entry, source.Spec, secrets); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errSourceUnusable, name, err)
	}

	return entry, nil
}

func (s *LoginSources) build(entry *loginSource, spec *v1.IdentitySourceSpec, secrets *storage.IdentitySourceSecrets) error {
	switch spec.Type {
	case v1.IdentitySourceTypeLDAP:
		if spec.LDAP == nil {
			return errors.New("spec.ldap is missing")
		}

		authenticator, err := s.newLDAP(identitysource.LDAPConfig(spec.LDAP, secrets.LDAPBindPassword))
		if err != nil {
			return err
		}

		entry.ldap = authenticator

		return nil
	case v1.IdentitySourceTypeOIDC:
		if spec.OIDC == nil {
			return errors.New("spec.oidc is missing")
		}

		callback, err := url.Parse(spec.OIDC.RedirectURL)
		if err != nil || callback.Path == "" {
			return fmt.Errorf("invalid OIDC redirect URL %q", spec.OIDC.RedirectURL)
		}

		allowed, err := parseOIDCAllowedRedirects(spec.OIDC.AllowedRedirects)
		if err != nil {
			return err
		}

		rp, err := s.newOIDC(identitysource.OIDCConfig(spec.OIDC, secrets.OIDCClientSecret))
		if err != nil {
			return err
		}

		entry.oidc = &oidcSource{
			name:             entry.name,
			rp:               rp,
			allowedRedirects: allowed,
			cookiePath:       callback.Path,
			cookieSecure:     callback.Scheme == "https",
		}

		return nil
	default:
		return fmt.Errorf("unknown type %q", spec.Type)
	}
}
