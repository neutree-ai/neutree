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
// The spec is a stored one: the database has filled in the defaults.
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
	}

	if spec.CACert != "" {
		cfg.RootCAs = []byte(spec.CACert)
	}

	if attrs := spec.Attributes; attrs != nil {
		cfg.Attributes = ldap.AttributeMapping{
			ID:          attrs.ID,
			Username:    attrs.Username,
			Email:       attrs.Email,
			DisplayName: attrs.DisplayName,
			MemberOf:    attrs.MemberOf,
		}
	}

	return cfg
}

// OIDCConfig builds the relying party configuration of an OIDC identity
// source. The spec is a stored one: the database has filled in the defaults.
func OIDCConfig(spec *v1.IdentitySourceOIDCSpec, clientSecret string) oidc.Config {
	cfg := oidc.Config{
		Issuer:       spec.Issuer,
		ClientID:     spec.ClientID,
		ClientSecret: clientSecret,
		RedirectURL:  spec.RedirectURL,
		Scopes:       spec.Scopes,
	}

	if spec.CACert != "" {
		cfg.RootCAs = []byte(spec.CACert)
	}

	if claims := spec.Claims; claims != nil {
		cfg.Claims = oidc.ClaimMapping{
			Username:    claims.Username,
			DisplayName: claims.DisplayName,
			Email:       claims.Email,
		}
	}

	return cfg
}

// Fingerprint identifies what a source's clients are built from: its spec and
// its secrets. Two equal fingerprints build the same clients. It is kept in
// memory only; it is a hash of the secrets, so it is never written anywhere.
func Fingerprint(spec *v1.IdentitySourceSpec, secrets *storage.IdentitySourceSecrets) [sha256.Size]byte {
	specJSON, _ := json.Marshal(spec)       //nolint:errcheck // plain structs always marshal
	secretsJSON, _ := json.Marshal(secrets) //nolint:errcheck // plain structs always marshal

	return sha256.Sum256(append(append(specJSON, 0), secretsJSON...))
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
