package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGoTrue answers generate_link and verify with the given status and body,
// and records what it was sent.
type fakeGoTrue struct {
	status int
	body   string

	path   string
	auth   string
	params map[string]string
}

func (f *fakeGoTrue) start(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path = r.URL.Path
		f.auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&f.params)

		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/"
}

func TestGenerateMagicLink(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    *MagicLink
		wantErr bool
	}{
		{
			name:   "ok",
			status: http.StatusOK,
			body:   `{"id":"u1","email":"a@b.c","hashed_token":"h1","action_link":"x"}`,
			want:   &MagicLink{UserID: "u1", HashedToken: "h1"},
		},
		{name: "gotrue error", status: http.StatusUnprocessableEntity, body: `{"msg":"bad"}`, wantErr: true},
		{name: "no hashed token", status: http.StatusOK, body: `{"id":"u1"}`, wantErr: true},
		{name: "not json", status: http.StatusOK, body: `nope`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGoTrue{status: tc.status, body: tc.body}
			issuer := NewSessionIssuer(f.start(t), "svc")

			link, err := issuer.GenerateMagicLink(context.Background(), "a@b.c")

			assert.Equal(t, "/admin/generate_link", f.path)
			assert.Equal(t, "Bearer svc", f.auth)
			assert.Equal(t, map[string]string{"type": "magiclink", "email": "a@b.c"}, f.params)

			if tc.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, link)
		})
	}
}

func TestVerifyMagicLink(t *testing.T) {
	const session = `{"access_token":"at","refresh_token":"rt","user":{"id":"u1"}}`

	t.Run("returns the session body as is", func(t *testing.T) {
		f := &fakeGoTrue{status: http.StatusOK, body: session}
		issuer := NewSessionIssuer(f.start(t), "svc")

		body, err := issuer.VerifyMagicLink(context.Background(), "h1")

		require.NoError(t, err)
		assert.Equal(t, session, string(body))
		assert.Equal(t, "/verify", f.path)
		assert.Empty(t, f.auth, "the service token must not be sent to a public endpoint")
		assert.Equal(t, map[string]string{"type": "magiclink", "token_hash": "h1"}, f.params)
	})

	t.Run("expired link", func(t *testing.T) {
		f := &fakeGoTrue{status: http.StatusForbidden, body: `{"error_code":"otp_expired"}`}
		issuer := NewSessionIssuer(f.start(t), "svc")

		_, err := issuer.VerifyMagicLink(context.Background(), "h1")

		assert.ErrorContains(t, err, "HTTP 403")
	})
}
