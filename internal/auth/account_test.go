package auth

import "testing"

func TestResolveAccount(t *testing.T) {
	cases := []struct {
		name, flag, env string
		want            Account
		wantErr         string
	}{
		{name: "default work", want: AccountWork},
		{name: "env personal", env: "personal", want: AccountPersonal},
		{name: "flag beats env", flag: "work", env: "personal", want: AccountWork},
		{name: "bad env is loud", env: "wrok", wantErr: "GWS_ACCOUNT must be 'work' or 'personal', got 'wrok'"},
		{name: "bad flag is loud", flag: "Work", wantErr: "--account must be 'work' or 'personal', got 'Work'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("GWS_ACCOUNT", tc.env)
			}
			got, err := ResolveAccount(tc.flag)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}
