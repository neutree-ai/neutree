// Package identitysource turns an IdentitySource resource into the LDAP and
// OIDC client configurations of pkg/identity, and tests its connection. It is
// shared by the login routes of neutree-api and the IdentitySource controller
// of neutree-core, so both read a source the same way.
package identitysource

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
	"github.com/neutree-ai/neutree/pkg/identity/oidc"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// maxMessageLength bounds a connection test message written to the status.
const maxMessageLength = 512

// LDAPConfig builds the directory configuration of an LDAP identity source.
// Attribute names the spec leaves empty get the defaults the database applies.
func LDAPConfig(spec *v1.IdentitySourceLDAPSpec, bindPassword string) ldap.Config {
	cfg := ldap.Config{
		URL:                spec.URL,
		StartTLS:           spec.StartTLS,
		InsecureSkipVerify: spec.InsecureSkipVerify,
		Timeout:            time.Duration(spec.Timeout) * time.Second,
		BindDN:             spec.BindDN,
		BindPassword:       bindPassword,
		UserBaseDN:         spec.UserBaseDN,
		UserFilter:         spec.UserFilter,
		Attributes: ldap.AttributeMapping{
			ID:          v1.DefaultIdentitySourceLDAPIDAttribute,
			Username:    v1.DefaultIdentitySourceLDAPUsernameAttr,
			Email:       v1.DefaultIdentitySourceLDAPEmailAttr,
			DisplayName: v1.DefaultIdentitySourceLDAPDisplayAttr,
		},
	}

	if strings.TrimSpace(spec.CACert) != "" {
		cfg.RootCAs = []byte(spec.CACert)
	}

	if attrs := spec.Attributes; attrs != nil {
		cfg.Attributes.ID = orDefault(attrs.ID, cfg.Attributes.ID)
		cfg.Attributes.Username = orDefault(attrs.Username, cfg.Attributes.Username)
		cfg.Attributes.Email = orDefault(attrs.Email, cfg.Attributes.Email)
		cfg.Attributes.DisplayName = orDefault(attrs.DisplayName, cfg.Attributes.DisplayName)
		cfg.Attributes.MemberOf = strings.TrimSpace(attrs.MemberOf)
	}

	return cfg
}

// OIDCConfig builds the relying party configuration of an OIDC identity
// source. Scopes and claims the spec leaves empty get the database defaults.
func OIDCConfig(spec *v1.IdentitySourceOIDCSpec, clientSecret string) oidc.Config {
	cfg := oidc.Config{
		Issuer:       spec.Issuer,
		ClientID:     spec.ClientID,
		ClientSecret: clientSecret,
		RedirectURL:  spec.RedirectURL,
		Scopes:       spec.Scopes,
		Claims: oidc.ClaimMapping{
			Username:    v1.DefaultIdentitySourceOIDCUsernameClaim,
			DisplayName: v1.DefaultIdentitySourceOIDCDisplayClaim,
			Email:       v1.DefaultIdentitySourceOIDCEmailClaim,
		},
	}

	if len(cfg.Scopes) == 0 {
		cfg.Scopes = append([]string(nil), v1.DefaultIdentitySourceOIDCScopes...)
	}

	if strings.TrimSpace(spec.CACert) != "" {
		cfg.RootCAs = []byte(spec.CACert)
	}

	if claims := spec.Claims; claims != nil {
		cfg.Claims.Username = orDefault(claims.Username, cfg.Claims.Username)
		cfg.Claims.DisplayName = orDefault(claims.DisplayName, cfg.Claims.DisplayName)
		cfg.Claims.Email = orDefault(claims.Email, cfg.Claims.Email)
	}

	return cfg
}

func orDefault(value, fallback string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}

	return fallback
}

// Fingerprint identifies what a source's clients are built from: its spec and
// its secrets. Two equal fingerprints build the same clients. It is kept in
// memory only; it is a hash of the secrets, so it is never written anywhere.
func Fingerprint(spec *v1.IdentitySourceSpec, secrets *storage.IdentitySourceSecrets) [sha256.Size]byte {
	specJSON, _ := json.Marshal(spec)       //nolint:errcheck // plain structs always marshal
	secretsJSON, _ := json.Marshal(secrets) //nolint:errcheck // plain structs always marshal

	h := sha256.New()
	h.Write(specJSON)
	h.Write([]byte{0})
	h.Write(secretsJSON)

	var sum [sha256.Size]byte

	copy(sum[:], h.Sum(nil))

	return sum
}

// ConnectionTest checks that the source answers with its configuration: an
// LDAP source dials and binds as the service account, an OIDC source fetches
// its discovery document and signing keys. It does not log anyone in.
func ConnectionTest(ctx context.Context, source *v1.IdentitySource, secrets *storage.IdentitySourceSecrets) error {
	if source == nil || source.Spec == nil {
		return errors.New("identity source has no spec")
	}

	if secrets == nil {
		secrets = &storage.IdentitySourceSecrets{}
	}

	switch source.Spec.Type {
	case v1.IdentitySourceTypeLDAP:
		if source.Spec.LDAP == nil {
			return errors.New("spec.ldap is missing")
		}

		cfg := LDAPConfig(source.Spec.LDAP, secrets.LDAPBindPassword)

		authenticator, err := ldap.New(cfg, ldap.NewDialer(cfg))
		if err != nil {
			return err
		}

		return authenticator.Ping(ctx)
	case v1.IdentitySourceTypeOIDC:
		if source.Spec.OIDC == nil {
			return errors.New("spec.oidc is missing")
		}

		rp, err := oidc.New(OIDCConfig(source.Spec.OIDC, secrets.OIDCClientSecret))
		if err != nil {
			return err
		}

		return rp.Ping(ctx)
	default:
		return fmt.Errorf("unknown identity source type %q", source.Spec.Type)
	}
}

// SafeMessage renders err for the status of a source, which every reader of
// the resource sees. The pkg/identity errors never carry a secret; the stored
// secrets are still cut out literally, and the message is bounded.
func SafeMessage(err error, secrets *storage.IdentitySourceSecrets) string {
	if err == nil {
		return ""
	}

	msg := err.Error()

	if secrets != nil {
		for _, secret := range []string{secrets.LDAPBindPassword, secrets.OIDCClientSecret} {
			if secret != "" {
				msg = strings.ReplaceAll(msg, secret, "[redacted]")
			}
		}
	}

	if len(msg) > maxMessageLength {
		msg = strings.ToValidUTF8(msg[:maxMessageLength], "") + "..."
	}

	return msg
}
