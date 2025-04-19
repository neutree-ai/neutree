package v1

import "database/sql"

type AuthSchemaMigrationsSelect struct {
	Version string `json:"version"`
}

type AuthSchemaMigrationsInsert struct {
	Version string `json:"version"`
}

type AuthSchemaMigrationsUpdate struct {
	Version sql.NullString `json:"version"`
}

type AuthUsersSelect struct {
	Aud                      sql.NullString `json:"aud"`
	BannedUntil              sql.NullString `json:"banned_until"`
	ConfirmationSentAt       sql.NullString `json:"confirmation_sent_at"`
	ConfirmationToken        sql.NullString `json:"confirmation_token"`
	ConfirmedAt              sql.NullString `json:"confirmed_at"`
	CreatedAt                sql.NullString `json:"created_at"`
	DeletedAt                sql.NullString `json:"deleted_at"`
	Email                    sql.NullString `json:"email"`
	EmailChange              sql.NullString `json:"email_change"`
	EmailChangeConfirmStatus sql.NullInt32  `json:"email_change_confirm_status"`
	EmailChangeSentAt        sql.NullString `json:"email_change_sent_at"`
	EmailChangeTokenCurrent  sql.NullString `json:"email_change_token_current"`
	EmailChangeTokenNew      sql.NullString `json:"email_change_token_new"`
	EmailConfirmedAt         sql.NullString `json:"email_confirmed_at"`
	EncryptedPassword        sql.NullString `json:"encrypted_password"`
	Id                       string         `json:"id"`
	InstanceId               sql.NullString `json:"instance_id"`
	InvitedAt                sql.NullString `json:"invited_at"`
	IsAnonymous              bool           `json:"is_anonymous"`
	IsSsoUser                bool           `json:"is_sso_user"`
	IsSuperAdmin             sql.NullBool   `json:"is_super_admin"`
	LastSignInAt             sql.NullString `json:"last_sign_in_at"`
	Phone                    sql.NullString `json:"phone"`
	PhoneChange              sql.NullString `json:"phone_change"`
	PhoneChangeSentAt        sql.NullString `json:"phone_change_sent_at"`
	PhoneChangeToken         sql.NullString `json:"phone_change_token"`
	PhoneConfirmedAt         sql.NullString `json:"phone_confirmed_at"`
	RawAppMetaData           interface{}    `json:"raw_app_meta_data"`
	RawUserMetaData          interface{}    `json:"raw_user_meta_data"`
	ReauthenticationSentAt   sql.NullString `json:"reauthentication_sent_at"`
	ReauthenticationToken    sql.NullString `json:"reauthentication_token"`
	RecoverySentAt           sql.NullString `json:"recovery_sent_at"`
	RecoveryToken            sql.NullString `json:"recovery_token"`
	Role                     sql.NullString `json:"role"`
	UpdatedAt                sql.NullString `json:"updated_at"`
}

type AuthUsersInsert struct {
	Aud                      sql.NullString `json:"aud"`
	BannedUntil              sql.NullString `json:"banned_until"`
	ConfirmationSentAt       sql.NullString `json:"confirmation_sent_at"`
	ConfirmationToken        sql.NullString `json:"confirmation_token"`
	ConfirmedAt              sql.NullString `json:"confirmed_at"`
	CreatedAt                sql.NullString `json:"created_at"`
	DeletedAt                sql.NullString `json:"deleted_at"`
	Email                    sql.NullString `json:"email"`
	EmailChange              sql.NullString `json:"email_change"`
	EmailChangeConfirmStatus sql.NullInt32  `json:"email_change_confirm_status"`
	EmailChangeSentAt        sql.NullString `json:"email_change_sent_at"`
	EmailChangeTokenCurrent  sql.NullString `json:"email_change_token_current"`
	EmailChangeTokenNew      sql.NullString `json:"email_change_token_new"`
	EmailConfirmedAt         sql.NullString `json:"email_confirmed_at"`
	EncryptedPassword        sql.NullString `json:"encrypted_password"`
	Id                       string         `json:"id"`
	InstanceId               sql.NullString `json:"instance_id"`
	InvitedAt                sql.NullString `json:"invited_at"`
	IsAnonymous              sql.NullBool   `json:"is_anonymous"`
	IsSsoUser                sql.NullBool   `json:"is_sso_user"`
	IsSuperAdmin             sql.NullBool   `json:"is_super_admin"`
	LastSignInAt             sql.NullString `json:"last_sign_in_at"`
	Phone                    sql.NullString `json:"phone"`
	PhoneChange              sql.NullString `json:"phone_change"`
	PhoneChangeSentAt        sql.NullString `json:"phone_change_sent_at"`
	PhoneChangeToken         sql.NullString `json:"phone_change_token"`
	PhoneConfirmedAt         sql.NullString `json:"phone_confirmed_at"`
	RawAppMetaData           interface{}    `json:"raw_app_meta_data"`
	RawUserMetaData          interface{}    `json:"raw_user_meta_data"`
	ReauthenticationSentAt   sql.NullString `json:"reauthentication_sent_at"`
	ReauthenticationToken    sql.NullString `json:"reauthentication_token"`
	RecoverySentAt           sql.NullString `json:"recovery_sent_at"`
	RecoveryToken            sql.NullString `json:"recovery_token"`
	Role                     sql.NullString `json:"role"`
	UpdatedAt                sql.NullString `json:"updated_at"`
}

type AuthUsersUpdate struct {
	Aud                      sql.NullString `json:"aud"`
	BannedUntil              sql.NullString `json:"banned_until"`
	ConfirmationSentAt       sql.NullString `json:"confirmation_sent_at"`
	ConfirmationToken        sql.NullString `json:"confirmation_token"`
	ConfirmedAt              sql.NullString `json:"confirmed_at"`
	CreatedAt                sql.NullString `json:"created_at"`
	DeletedAt                sql.NullString `json:"deleted_at"`
	Email                    sql.NullString `json:"email"`
	EmailChange              sql.NullString `json:"email_change"`
	EmailChangeConfirmStatus sql.NullInt32  `json:"email_change_confirm_status"`
	EmailChangeSentAt        sql.NullString `json:"email_change_sent_at"`
	EmailChangeTokenCurrent  sql.NullString `json:"email_change_token_current"`
	EmailChangeTokenNew      sql.NullString `json:"email_change_token_new"`
	EmailConfirmedAt         sql.NullString `json:"email_confirmed_at"`
	EncryptedPassword        sql.NullString `json:"encrypted_password"`
	Id                       sql.NullString `json:"id"`
	InstanceId               sql.NullString `json:"instance_id"`
	InvitedAt                sql.NullString `json:"invited_at"`
	IsAnonymous              sql.NullBool   `json:"is_anonymous"`
	IsSsoUser                sql.NullBool   `json:"is_sso_user"`
	IsSuperAdmin             sql.NullBool   `json:"is_super_admin"`
	LastSignInAt             sql.NullString `json:"last_sign_in_at"`
	Phone                    sql.NullString `json:"phone"`
	PhoneChange              sql.NullString `json:"phone_change"`
	PhoneChangeSentAt        sql.NullString `json:"phone_change_sent_at"`
	PhoneChangeToken         sql.NullString `json:"phone_change_token"`
	PhoneConfirmedAt         sql.NullString `json:"phone_confirmed_at"`
	RawAppMetaData           interface{}    `json:"raw_app_meta_data"`
	RawUserMetaData          interface{}    `json:"raw_user_meta_data"`
	ReauthenticationSentAt   sql.NullString `json:"reauthentication_sent_at"`
	ReauthenticationToken    sql.NullString `json:"reauthentication_token"`
	RecoverySentAt           sql.NullString `json:"recovery_sent_at"`
	RecoveryToken            sql.NullString `json:"recovery_token"`
	Role                     sql.NullString `json:"role"`
	UpdatedAt                sql.NullString `json:"updated_at"`
}

type AuthRefreshTokensSelect struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         int64          `json:"id"`
	InstanceId sql.NullString `json:"instance_id"`
	Parent     sql.NullString `json:"parent"`
	Revoked    sql.NullBool   `json:"revoked"`
	SessionId  sql.NullString `json:"session_id"`
	Token      sql.NullString `json:"token"`
	UpdatedAt  sql.NullString `json:"updated_at"`
	UserId     sql.NullString `json:"user_id"`
}

type AuthRefreshTokensInsert struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         sql.NullInt64  `json:"id"`
	InstanceId sql.NullString `json:"instance_id"`
	Parent     sql.NullString `json:"parent"`
	Revoked    sql.NullBool   `json:"revoked"`
	SessionId  sql.NullString `json:"session_id"`
	Token      sql.NullString `json:"token"`
	UpdatedAt  sql.NullString `json:"updated_at"`
	UserId     sql.NullString `json:"user_id"`
}

type AuthRefreshTokensUpdate struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         sql.NullInt64  `json:"id"`
	InstanceId sql.NullString `json:"instance_id"`
	Parent     sql.NullString `json:"parent"`
	Revoked    sql.NullBool   `json:"revoked"`
	SessionId  sql.NullString `json:"session_id"`
	Token      sql.NullString `json:"token"`
	UpdatedAt  sql.NullString `json:"updated_at"`
	UserId     sql.NullString `json:"user_id"`
}

type AuthInstancesSelect struct {
	CreatedAt     sql.NullString `json:"created_at"`
	Id            string         `json:"id"`
	RawBaseConfig sql.NullString `json:"raw_base_config"`
	UpdatedAt     sql.NullString `json:"updated_at"`
	Uuid          sql.NullString `json:"uuid"`
}

type AuthInstancesInsert struct {
	CreatedAt     sql.NullString `json:"created_at"`
	Id            string         `json:"id"`
	RawBaseConfig sql.NullString `json:"raw_base_config"`
	UpdatedAt     sql.NullString `json:"updated_at"`
	Uuid          sql.NullString `json:"uuid"`
}

type AuthInstancesUpdate struct {
	CreatedAt     sql.NullString `json:"created_at"`
	Id            sql.NullString `json:"id"`
	RawBaseConfig sql.NullString `json:"raw_base_config"`
	UpdatedAt     sql.NullString `json:"updated_at"`
	Uuid          sql.NullString `json:"uuid"`
}

type AuthAuditLogEntriesSelect struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         string         `json:"id"`
	InstanceId sql.NullString `json:"instance_id"`
	IpAddress  string         `json:"ip_address"`
	Payload    interface{}    `json:"payload"`
}

type AuthAuditLogEntriesInsert struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         string         `json:"id"`
	InstanceId sql.NullString `json:"instance_id"`
	IpAddress  sql.NullString `json:"ip_address"`
	Payload    interface{}    `json:"payload"`
}

type AuthAuditLogEntriesUpdate struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         sql.NullString `json:"id"`
	InstanceId sql.NullString `json:"instance_id"`
	IpAddress  sql.NullString `json:"ip_address"`
	Payload    interface{}    `json:"payload"`
}

type AuthIdentitiesSelect struct {
	CreatedAt    sql.NullString `json:"created_at"`
	Email        sql.NullString `json:"email"`
	Id           string         `json:"id"`
	IdentityData interface{}    `json:"identity_data"`
	LastSignInAt sql.NullString `json:"last_sign_in_at"`
	Provider     string         `json:"provider"`
	ProviderId   string         `json:"provider_id"`
	UpdatedAt    sql.NullString `json:"updated_at"`
	UserId       string         `json:"user_id"`
}

type AuthIdentitiesInsert struct {
	CreatedAt    sql.NullString `json:"created_at"`
	Email        sql.NullString `json:"email"`
	Id           sql.NullString `json:"id"`
	IdentityData interface{}    `json:"identity_data"`
	LastSignInAt sql.NullString `json:"last_sign_in_at"`
	Provider     string         `json:"provider"`
	ProviderId   string         `json:"provider_id"`
	UpdatedAt    sql.NullString `json:"updated_at"`
	UserId       string         `json:"user_id"`
}

type AuthIdentitiesUpdate struct {
	CreatedAt    sql.NullString `json:"created_at"`
	Email        sql.NullString `json:"email"`
	Id           sql.NullString `json:"id"`
	IdentityData interface{}    `json:"identity_data"`
	LastSignInAt sql.NullString `json:"last_sign_in_at"`
	Provider     sql.NullString `json:"provider"`
	ProviderId   sql.NullString `json:"provider_id"`
	UpdatedAt    sql.NullString `json:"updated_at"`
	UserId       sql.NullString `json:"user_id"`
}

type AuthSessionsSelect struct {
	Aal         sql.NullString `json:"aal"`
	CreatedAt   sql.NullString `json:"created_at"`
	FactorId    sql.NullString `json:"factor_id"`
	Id          string         `json:"id"`
	Ip          interface{}    `json:"ip"`
	NotAfter    sql.NullString `json:"not_after"`
	RefreshedAt sql.NullString `json:"refreshed_at"`
	Tag         sql.NullString `json:"tag"`
	UpdatedAt   sql.NullString `json:"updated_at"`
	UserAgent   sql.NullString `json:"user_agent"`
	UserId      string         `json:"user_id"`
}

type AuthSessionsInsert struct {
	Aal         sql.NullString `json:"aal"`
	CreatedAt   sql.NullString `json:"created_at"`
	FactorId    sql.NullString `json:"factor_id"`
	Id          string         `json:"id"`
	Ip          interface{}    `json:"ip"`
	NotAfter    sql.NullString `json:"not_after"`
	RefreshedAt sql.NullString `json:"refreshed_at"`
	Tag         sql.NullString `json:"tag"`
	UpdatedAt   sql.NullString `json:"updated_at"`
	UserAgent   sql.NullString `json:"user_agent"`
	UserId      string         `json:"user_id"`
}

type AuthSessionsUpdate struct {
	Aal         sql.NullString `json:"aal"`
	CreatedAt   sql.NullString `json:"created_at"`
	FactorId    sql.NullString `json:"factor_id"`
	Id          sql.NullString `json:"id"`
	Ip          interface{}    `json:"ip"`
	NotAfter    sql.NullString `json:"not_after"`
	RefreshedAt sql.NullString `json:"refreshed_at"`
	Tag         sql.NullString `json:"tag"`
	UpdatedAt   sql.NullString `json:"updated_at"`
	UserAgent   sql.NullString `json:"user_agent"`
	UserId      sql.NullString `json:"user_id"`
}

type AuthMfaFactorsSelect struct {
	CreatedAt          string         `json:"created_at"`
	FactorType         string         `json:"factor_type"`
	FriendlyName       sql.NullString `json:"friendly_name"`
	Id                 string         `json:"id"`
	LastChallengedAt   sql.NullString `json:"last_challenged_at"`
	Phone              sql.NullString `json:"phone"`
	Secret             sql.NullString `json:"secret"`
	Status             string         `json:"status"`
	UpdatedAt          string         `json:"updated_at"`
	UserId             string         `json:"user_id"`
	WebAuthnAaguid     sql.NullString `json:"web_authn_aaguid"`
	WebAuthnCredential interface{}    `json:"web_authn_credential"`
}

type AuthMfaFactorsInsert struct {
	CreatedAt          string         `json:"created_at"`
	FactorType         string         `json:"factor_type"`
	FriendlyName       sql.NullString `json:"friendly_name"`
	Id                 string         `json:"id"`
	LastChallengedAt   sql.NullString `json:"last_challenged_at"`
	Phone              sql.NullString `json:"phone"`
	Secret             sql.NullString `json:"secret"`
	Status             string         `json:"status"`
	UpdatedAt          string         `json:"updated_at"`
	UserId             string         `json:"user_id"`
	WebAuthnAaguid     sql.NullString `json:"web_authn_aaguid"`
	WebAuthnCredential interface{}    `json:"web_authn_credential"`
}

type AuthMfaFactorsUpdate struct {
	CreatedAt          sql.NullString `json:"created_at"`
	FactorType         sql.NullString `json:"factor_type"`
	FriendlyName       sql.NullString `json:"friendly_name"`
	Id                 sql.NullString `json:"id"`
	LastChallengedAt   sql.NullString `json:"last_challenged_at"`
	Phone              sql.NullString `json:"phone"`
	Secret             sql.NullString `json:"secret"`
	Status             sql.NullString `json:"status"`
	UpdatedAt          sql.NullString `json:"updated_at"`
	UserId             sql.NullString `json:"user_id"`
	WebAuthnAaguid     sql.NullString `json:"web_authn_aaguid"`
	WebAuthnCredential interface{}    `json:"web_authn_credential"`
}

type AuthMfaChallengesSelect struct {
	CreatedAt           string         `json:"created_at"`
	FactorId            string         `json:"factor_id"`
	Id                  string         `json:"id"`
	IpAddress           interface{}    `json:"ip_address"`
	OtpCode             sql.NullString `json:"otp_code"`
	VerifiedAt          sql.NullString `json:"verified_at"`
	WebAuthnSessionData interface{}    `json:"web_authn_session_data"`
}

type AuthMfaChallengesInsert struct {
	CreatedAt           string         `json:"created_at"`
	FactorId            string         `json:"factor_id"`
	Id                  string         `json:"id"`
	IpAddress           interface{}    `json:"ip_address"`
	OtpCode             sql.NullString `json:"otp_code"`
	VerifiedAt          sql.NullString `json:"verified_at"`
	WebAuthnSessionData interface{}    `json:"web_authn_session_data"`
}

type AuthMfaChallengesUpdate struct {
	CreatedAt           sql.NullString `json:"created_at"`
	FactorId            sql.NullString `json:"factor_id"`
	Id                  sql.NullString `json:"id"`
	IpAddress           interface{}    `json:"ip_address"`
	OtpCode             sql.NullString `json:"otp_code"`
	VerifiedAt          sql.NullString `json:"verified_at"`
	WebAuthnSessionData interface{}    `json:"web_authn_session_data"`
}

type AuthMfaAmrClaimsSelect struct {
	AuthenticationMethod string `json:"authentication_method"`
	CreatedAt            string `json:"created_at"`
	Id                   string `json:"id"`
	SessionId            string `json:"session_id"`
	UpdatedAt            string `json:"updated_at"`
}

type AuthMfaAmrClaimsInsert struct {
	AuthenticationMethod string `json:"authentication_method"`
	CreatedAt            string `json:"created_at"`
	Id                   string `json:"id"`
	SessionId            string `json:"session_id"`
	UpdatedAt            string `json:"updated_at"`
}

type AuthMfaAmrClaimsUpdate struct {
	AuthenticationMethod sql.NullString `json:"authentication_method"`
	CreatedAt            sql.NullString `json:"created_at"`
	Id                   sql.NullString `json:"id"`
	SessionId            sql.NullString `json:"session_id"`
	UpdatedAt            sql.NullString `json:"updated_at"`
}

type AuthSsoProvidersSelect struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         string         `json:"id"`
	ResourceId sql.NullString `json:"resource_id"`
	UpdatedAt  sql.NullString `json:"updated_at"`
}

type AuthSsoProvidersInsert struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         string         `json:"id"`
	ResourceId sql.NullString `json:"resource_id"`
	UpdatedAt  sql.NullString `json:"updated_at"`
}

type AuthSsoProvidersUpdate struct {
	CreatedAt  sql.NullString `json:"created_at"`
	Id         sql.NullString `json:"id"`
	ResourceId sql.NullString `json:"resource_id"`
	UpdatedAt  sql.NullString `json:"updated_at"`
}

type AuthSsoDomainsSelect struct {
	CreatedAt     sql.NullString `json:"created_at"`
	Domain        string         `json:"domain"`
	Id            string         `json:"id"`
	SsoProviderId string         `json:"sso_provider_id"`
	UpdatedAt     sql.NullString `json:"updated_at"`
}

type AuthSsoDomainsInsert struct {
	CreatedAt     sql.NullString `json:"created_at"`
	Domain        string         `json:"domain"`
	Id            string         `json:"id"`
	SsoProviderId string         `json:"sso_provider_id"`
	UpdatedAt     sql.NullString `json:"updated_at"`
}

type AuthSsoDomainsUpdate struct {
	CreatedAt     sql.NullString `json:"created_at"`
	Domain        sql.NullString `json:"domain"`
	Id            sql.NullString `json:"id"`
	SsoProviderId sql.NullString `json:"sso_provider_id"`
	UpdatedAt     sql.NullString `json:"updated_at"`
}

type AuthSamlProvidersSelect struct {
	AttributeMapping interface{}    `json:"attribute_mapping"`
	CreatedAt        sql.NullString `json:"created_at"`
	EntityId         string         `json:"entity_id"`
	Id               string         `json:"id"`
	MetadataUrl      sql.NullString `json:"metadata_url"`
	MetadataXml      string         `json:"metadata_xml"`
	NameIdFormat     sql.NullString `json:"name_id_format"`
	SsoProviderId    string         `json:"sso_provider_id"`
	UpdatedAt        sql.NullString `json:"updated_at"`
}

type AuthSamlProvidersInsert struct {
	AttributeMapping interface{}    `json:"attribute_mapping"`
	CreatedAt        sql.NullString `json:"created_at"`
	EntityId         string         `json:"entity_id"`
	Id               string         `json:"id"`
	MetadataUrl      sql.NullString `json:"metadata_url"`
	MetadataXml      string         `json:"metadata_xml"`
	NameIdFormat     sql.NullString `json:"name_id_format"`
	SsoProviderId    string         `json:"sso_provider_id"`
	UpdatedAt        sql.NullString `json:"updated_at"`
}

type AuthSamlProvidersUpdate struct {
	AttributeMapping interface{}    `json:"attribute_mapping"`
	CreatedAt        sql.NullString `json:"created_at"`
	EntityId         sql.NullString `json:"entity_id"`
	Id               sql.NullString `json:"id"`
	MetadataUrl      sql.NullString `json:"metadata_url"`
	MetadataXml      sql.NullString `json:"metadata_xml"`
	NameIdFormat     sql.NullString `json:"name_id_format"`
	SsoProviderId    sql.NullString `json:"sso_provider_id"`
	UpdatedAt        sql.NullString `json:"updated_at"`
}

type AuthSamlRelayStatesSelect struct {
	CreatedAt     sql.NullString `json:"created_at"`
	FlowStateId   sql.NullString `json:"flow_state_id"`
	ForEmail      sql.NullString `json:"for_email"`
	Id            string         `json:"id"`
	RedirectTo    sql.NullString `json:"redirect_to"`
	RequestId     string         `json:"request_id"`
	SsoProviderId string         `json:"sso_provider_id"`
	UpdatedAt     sql.NullString `json:"updated_at"`
}

type AuthSamlRelayStatesInsert struct {
	CreatedAt     sql.NullString `json:"created_at"`
	FlowStateId   sql.NullString `json:"flow_state_id"`
	ForEmail      sql.NullString `json:"for_email"`
	Id            string         `json:"id"`
	RedirectTo    sql.NullString `json:"redirect_to"`
	RequestId     string         `json:"request_id"`
	SsoProviderId string         `json:"sso_provider_id"`
	UpdatedAt     sql.NullString `json:"updated_at"`
}

type AuthSamlRelayStatesUpdate struct {
	CreatedAt     sql.NullString `json:"created_at"`
	FlowStateId   sql.NullString `json:"flow_state_id"`
	ForEmail      sql.NullString `json:"for_email"`
	Id            sql.NullString `json:"id"`
	RedirectTo    sql.NullString `json:"redirect_to"`
	RequestId     sql.NullString `json:"request_id"`
	SsoProviderId sql.NullString `json:"sso_provider_id"`
	UpdatedAt     sql.NullString `json:"updated_at"`
}

type AuthFlowStateSelect struct {
	AuthCode             string         `json:"auth_code"`
	AuthCodeIssuedAt     sql.NullString `json:"auth_code_issued_at"`
	AuthenticationMethod string         `json:"authentication_method"`
	CodeChallenge        string         `json:"code_challenge"`
	CodeChallengeMethod  string         `json:"code_challenge_method"`
	CreatedAt            sql.NullString `json:"created_at"`
	Id                   string         `json:"id"`
	ProviderAccessToken  sql.NullString `json:"provider_access_token"`
	ProviderRefreshToken sql.NullString `json:"provider_refresh_token"`
	ProviderType         string         `json:"provider_type"`
	UpdatedAt            sql.NullString `json:"updated_at"`
	UserId               sql.NullString `json:"user_id"`
}

type AuthFlowStateInsert struct {
	AuthCode             string         `json:"auth_code"`
	AuthCodeIssuedAt     sql.NullString `json:"auth_code_issued_at"`
	AuthenticationMethod string         `json:"authentication_method"`
	CodeChallenge        string         `json:"code_challenge"`
	CodeChallengeMethod  string         `json:"code_challenge_method"`
	CreatedAt            sql.NullString `json:"created_at"`
	Id                   string         `json:"id"`
	ProviderAccessToken  sql.NullString `json:"provider_access_token"`
	ProviderRefreshToken sql.NullString `json:"provider_refresh_token"`
	ProviderType         string         `json:"provider_type"`
	UpdatedAt            sql.NullString `json:"updated_at"`
	UserId               sql.NullString `json:"user_id"`
}

type AuthFlowStateUpdate struct {
	AuthCode             sql.NullString `json:"auth_code"`
	AuthCodeIssuedAt     sql.NullString `json:"auth_code_issued_at"`
	AuthenticationMethod sql.NullString `json:"authentication_method"`
	CodeChallenge        sql.NullString `json:"code_challenge"`
	CodeChallengeMethod  sql.NullString `json:"code_challenge_method"`
	CreatedAt            sql.NullString `json:"created_at"`
	Id                   sql.NullString `json:"id"`
	ProviderAccessToken  sql.NullString `json:"provider_access_token"`
	ProviderRefreshToken sql.NullString `json:"provider_refresh_token"`
	ProviderType         sql.NullString `json:"provider_type"`
	UpdatedAt            sql.NullString `json:"updated_at"`
	UserId               sql.NullString `json:"user_id"`
}

type AuthOneTimeTokensSelect struct {
	CreatedAt string `json:"created_at"`
	Id        string `json:"id"`
	RelatesTo string `json:"relates_to"`
	TokenHash string `json:"token_hash"`
	TokenType string `json:"token_type"`
	UpdatedAt string `json:"updated_at"`
	UserId    string `json:"user_id"`
}

type AuthOneTimeTokensInsert struct {
	CreatedAt sql.NullString `json:"created_at"`
	Id        string         `json:"id"`
	RelatesTo string         `json:"relates_to"`
	TokenHash string         `json:"token_hash"`
	TokenType string         `json:"token_type"`
	UpdatedAt sql.NullString `json:"updated_at"`
	UserId    string         `json:"user_id"`
}

type AuthOneTimeTokensUpdate struct {
	CreatedAt sql.NullString `json:"created_at"`
	Id        sql.NullString `json:"id"`
	RelatesTo sql.NullString `json:"relates_to"`
	TokenHash sql.NullString `json:"token_hash"`
	TokenType sql.NullString `json:"token_type"`
	UpdatedAt sql.NullString `json:"updated_at"`
	UserId    sql.NullString `json:"user_id"`
}

type PublicSchemaMigrationsSelect struct {
	Dirty   bool  `json:"dirty"`
	Version int64 `json:"version"`
}

type PublicSchemaMigrationsInsert struct {
	Dirty   bool  `json:"dirty"`
	Version int64 `json:"version"`
}

type PublicSchemaMigrationsUpdate struct {
	Dirty   sql.NullBool  `json:"dirty"`
	Version sql.NullInt64 `json:"version"`
}

type ApiWorkspacesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Status     map[string]interface{} `json:"status"`
}

type ApiWorkspacesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Status     map[string]interface{} `json:"status"`
}

type ApiWorkspacesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRolesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRolesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRolesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRoleAssignmentsSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRoleAssignmentsInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRoleAssignmentsUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiUserProfilesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiUserProfilesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiUserProfilesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullString         `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiApiKeysSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
	UserId     string                 `json:"user_id"`
}

type ApiApiKeysInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullString         `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
	UserId     string                 `json:"user_id"`
}

type ApiApiKeysUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullString         `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
	UserId     sql.NullString         `json:"user_id"`
}

type ApiApiUsageRecordsSelect struct {
	ApiKeyId     string         `json:"api_key_id"`
	CreatedAt    string         `json:"created_at"`
	Id           int64          `json:"id"`
	IsAggregated sql.NullBool   `json:"is_aggregated"`
	Metadata     interface{}    `json:"metadata"`
	Model        sql.NullString `json:"model"`
	RequestId    sql.NullString `json:"request_id"`
	UsageAmount  int32          `json:"usage_amount"`
	Workspace    sql.NullString `json:"workspace"`
}

type ApiApiUsageRecordsInsert struct {
	ApiKeyId     string         `json:"api_key_id"`
	CreatedAt    sql.NullString `json:"created_at"`
	Id           sql.NullInt64  `json:"id"`
	IsAggregated sql.NullBool   `json:"is_aggregated"`
	Metadata     interface{}    `json:"metadata"`
	Model        sql.NullString `json:"model"`
	RequestId    sql.NullString `json:"request_id"`
	UsageAmount  int32          `json:"usage_amount"`
	Workspace    sql.NullString `json:"workspace"`
}

type ApiApiUsageRecordsUpdate struct {
	ApiKeyId     sql.NullString `json:"api_key_id"`
	CreatedAt    sql.NullString `json:"created_at"`
	Id           sql.NullInt64  `json:"id"`
	IsAggregated sql.NullBool   `json:"is_aggregated"`
	Metadata     interface{}    `json:"metadata"`
	Model        sql.NullString `json:"model"`
	RequestId    sql.NullString `json:"request_id"`
	UsageAmount  sql.NullInt32  `json:"usage_amount"`
	Workspace    sql.NullString `json:"workspace"`
}

type ApiApiDailyUsageSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiApiDailyUsageInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiApiDailyUsageUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEndpointsSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEndpointsInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEndpointsUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiImageRegistriesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiImageRegistriesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiImageRegistriesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiModelRegistriesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiModelRegistriesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiModelRegistriesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEnginesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEnginesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEnginesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiClustersSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiClustersInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiClustersUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiMetadata struct {
	Name              string      `json:"name"`
	DisplayName       string      `json:"display_name"`
	Workspace         string      `json:"workspace"`
	DeletionTimestamp interface{} `json:"deletion_timestamp"`
	CreationTimestamp interface{} `json:"creation_timestamp"`
	UpdateTimestamp   interface{} `json:"update_timestamp"`
	Labels            interface{} `json:"labels"`
}

type ApiWorkspaceStatus struct {
	Phase        string `json:"phase"`
	ServiceUrl   string `json:"service_url"`
	ErrorMessage string `json:"error_message"`
}

type ApiRoleSpec struct {
	PresetKey   interface{} `json:"preset_key"`
	Permissions interface{} `json:"permissions"`
}

type ApiRoleStatus struct {
	Phase        string `json:"phase"`
	ServiceUrl   string `json:"service_url"`
	ErrorMessage string `json:"error_message"`
}

type ApiRoleAssignmentSpec struct {
	UserId    string      `json:"user_id"`
	Workspace string      `json:"workspace"`
	Global    interface{} `json:"global"`
	Role      string      `json:"role"`
}

type ApiRoleAssignmentStatus struct {
	Phase        string `json:"phase"`
	ServiceUrl   string `json:"service_url"`
	ErrorMessage string `json:"error_message"`
}

type ApiUserProfileSpec struct {
	Email string `json:"email"`
}

type ApiUserProfileStatus struct {
	Phase        string `json:"phase"`
	ServiceUrl   string `json:"service_url"`
	ErrorMessage string `json:"error_message"`
}

type ApiApiKeySpec struct {
	Quota interface{} `json:"quota"`
}

type ApiApiKeyStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
	SkValue            string      `json:"sk_value"`
	Usage              interface{} `json:"usage"`
	LastUsedAt         interface{} `json:"last_used_at"`
	LastSyncAt         interface{} `json:"last_sync_at"`
}

type ApiApiDailyUsageSpec struct {
	ApiKeyId         string      `json:"api_key_id"`
	UsageDate        string      `json:"usage_date"`
	TotalUsage       interface{} `json:"total_usage"`
	DimensionalUsage interface{} `json:"dimensional_usage"`
}

type ApiApiDailyUsageStatus struct {
	LastSyncTime interface{} `json:"last_sync_time"`
}

type ApiModelSpec struct {
	Registry string `json:"registry"`
	Name     string `json:"name"`
	File     string `json:"file"`
	Version  string `json:"version"`
	Task     string `json:"task"`
}

type ApiEndpointEngineSpec struct {
	Engine  string `json:"engine"`
	Version string `json:"version"`
}

type ApiResourceSpec struct {
	Cpu         interface{} `json:"cpu"`
	Gpu         interface{} `json:"gpu"`
	Accelerator interface{} `json:"accelerator"`
	Memory      interface{} `json:"memory"`
}

type ApiReplicaSpec struct {
	Num interface{} `json:"num"`
}

type ApiEndpointSpec struct {
	Cluster           string      `json:"cluster"`
	Model             interface{} `json:"model"`
	Engine            interface{} `json:"engine"`
	Resources         interface{} `json:"resources"`
	Replicas          interface{} `json:"replicas"`
	DeploymentOptions interface{} `json:"deployment_options"`
	Variables         interface{} `json:"variables"`
}

type ApiEndpointStatus struct {
	Phase              string      `json:"phase"`
	ServiceUrl         string      `json:"service_url"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiImageRegistrySpec struct {
	Url        string      `json:"url"`
	Repository string      `json:"repository"`
	Authconfig interface{} `json:"authconfig"`
	Ca         string      `json:"ca"`
}

type ApiImageRegistryStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiModelRegistrySpec struct {
	Type        string `json:"type"`
	Url         string `json:"url"`
	Credentials string `json:"credentials"`
}

type ApiModelRegistryStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiEngineVersion struct {
	Version      string      `json:"version"`
	ValuesSchema interface{} `json:"values_schema"`
}

type ApiEngineSpec struct {
	Versions       interface{} `json:"versions"`
	SupportedTasks interface{} `json:"supported_tasks"`
}

type ApiEngineStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiClusterSpec struct {
	Type          string      `json:"type"`
	Config        interface{} `json:"config"`
	ImageRegistry string      `json:"image_registry"`
	Version       string      `json:"version"`
}

type ApiClusterStatus struct {
	Phase               string      `json:"phase"`
	Image               string      `json:"image"`
	DashboardUrl        string      `json:"dashboard_url"`
	LastTransitionTime  interface{} `json:"last_transition_time"`
	ErrorMessage        string      `json:"error_message"`
	ReadyNodes          interface{} `json:"ready_nodes"`
	DesiredNodes        interface{} `json:"desired_nodes"`
	Version             string      `json:"version"`
	RayVersion          string      `json:"ray_version"`
	Initialized         interface{} `json:"initialized"`
	NodeProvisionStatus string      `json:"node_provision_status"`
}
