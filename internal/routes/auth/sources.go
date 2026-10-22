package auth

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/identitysource"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
	"github.com/neutree-ai/neutree/pkg/storage"
)

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
// database and keeps the LDAP authenticators built from them.
//
// Every login reads the source and its secrets, so a change, a disable or a
// delete applies to the next login. The built client is kept while neither
// changed. A source that is gone, or a failed read, drops its client.
type LoginSources struct {
	store storage.Storage

	newLDAP func(ldap.Config) (LDAPAuthenticator, error)

	mu      sync.Mutex
	entries map[string]*loginSource
}

// NewLoginSources reads identity sources from store.
func NewLoginSources(store storage.Storage) *LoginSources {
	return &LoginSources{
		store: store,
		newLDAP: func(cfg ldap.Config) (LDAPAuthenticator, error) {
			return ldap.New(cfg, ldap.NewDialer(cfg))
		},
		entries: map[string]*loginSource{},
	}
}

// loginSource is an enabled identity source ready for logins. It is not
// changed once built; a changed source gets a new one.
type loginSource struct {
	name        string
	sourceType  v1.IdentitySourceType
	fingerprint [sha256.Size]byte

	ldap LDAPAuthenticator
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

// get reads the enabled source with the given name and type. The clients
// built for it are reused while its spec and secrets are unchanged.
func (s *LoginSources) get(name string, sourceType v1.IdentitySourceType) (*loginSource, error) {
	if v1.ValidateIdentitySourceName(name) != nil {
		return nil, errSourceNotFound
	}

	entry, err := s.load(name)

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

	if entry.sourceType != sourceType {
		return nil, errSourceNotFound
	}

	return entry, nil
}

// load reads the source and its secrets and returns the entry kept for them,
// building a new one when either changed.
func (s *LoginSources) load(name string) (*loginSource, error) {
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

	s.mu.Lock()
	previous := s.entries[name]
	s.mu.Unlock()

	if previous != nil && previous.fingerprint == fingerprint {
		return previous, nil
	}

	entry := &loginSource{name: name, sourceType: source.Spec.Type, fingerprint: fingerprint}

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
		// There is no OIDC login yet; get refuses the source by its type.
		return nil
	default:
		return fmt.Errorf("unknown type %q", spec.Type)
	}
}
