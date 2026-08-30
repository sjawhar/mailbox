package auth

import (
	"context"
	"strings"
	"testing"
)

func TestAcquireEnvRetainsOAuthScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "scope present",
			body: `{"access_token":"scoped-token","expires_in":3600,"scope":"https://www.googleapis.com/auth/gmail.send"}`,
			want: "https://www.googleapis.com/auth/gmail.send",
		},
		{
			name: "scope absent",
			body: `{"access_token":"unscoped-token","expires_in":3600}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acct := &AccountConfig{Name: "work"}
			source := &CredentialSource{Class: ClassSend, Kind: SourceEnv, EnvVar: "SEND_OAUTH", ConfigKey: "accounts.work.send_credential_env"}
			acct.Send = source
			cfg := &Config{Path: "/test/config.toml", Accounts: []*AccountConfig{acct}}
			t.Setenv("SEND_OAUTH", oauthJSON())
			t.Setenv("MAILBOX_TOKEN_URL", tokenServer(t, 200, tc.body))

			acquired, err := acquireEnv(context.Background(), cfg, acct, source)
			if err != nil {
				t.Fatal(err)
			}
			if acquired.Token.Scope != tc.want {
				t.Fatalf("Token.Scope = %q, want %q", acquired.Token.Scope, tc.want)
			}
		})
	}
}

func TestSendScopeWarning(t *testing.T) {
	key := "accounts.work.send_credential_cmd"
	for _, tc := range []struct {
		scope        string
		wantContains string
	}{
		{"", ""},
		{"https://www.googleapis.com/auth/gmail.send", ""},
		{"gmail.send", ""},
		{"https://www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.readonly", key},
		{"https://mail.google.com/", key},
	} {
		got := SendScopeWarning(tc.scope, key)
		if (tc.wantContains == "") != (got == "") || (tc.wantContains != "" && !strings.Contains(got, tc.wantContains)) {
			t.Fatalf("SendScopeWarning(%q) = %q", tc.scope, got)
		}
	}
}

func TestSendScopeComesFromLiveSlotAndClearsOnInvalidation(t *testing.T) {
	source, cfg, _ := sendTestSource(&CredentialSource{
		Class:     ClassSend,
		Kind:      SourceEnv,
		EnvVar:    "SEND_OAUTH",
		ConfigKey: "accounts.work.send_credential_env",
	})
	t.Setenv("SEND_OAUTH", oauthJSON())
	t.Setenv("MAILBOX_TOKEN_URL", tokenServer(t, 200, `{"access_token":"scoped-send-token","expires_in":3600,"scope":"https://www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.readonly"}`))

	if _, err := source.SendToken(context.Background(), EnvOnlyAcquirer{Cfg: cfg}); err != nil {
		t.Fatal(err)
	}
	want := "https://www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.readonly"
	if got := source.SendScope(); got != want {
		t.Fatalf("SendScope = %q, want %q", got, want)
	}
	if diagnostic := source.TakeDiagnostic(ClassSend); diagnostic != "granted scope: "+want {
		t.Fatalf("send diagnostic = %q, want granted scope", diagnostic)
	}
	source.InvalidateSend()
	if got := source.SendScope(); got != "" {
		t.Fatalf("SendScope after InvalidateSend = %q, want empty", got)
	}
}
