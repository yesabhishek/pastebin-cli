package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/keystore"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/storetest"
)

func newBareApp(t *testing.T) (*App, *keystore.Memory) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", home)
	app, err := NewApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	ks := &keystore.Memory{}
	app.keys = ks
	return app, ks
}

func TestGitLabAndBitbucketEndToEnd(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		login    []string
		token    string
		setup    func(t *testing.T, app *App) *storetest.FakeGit
	}{
		{
			name: "gitlab", provider: "gitlab", token: "gl-token",
			login: []string{"login", "gitlab", "--stdin"},
			setup: func(t *testing.T, app *App) *storetest.FakeGit {
				srv, g := storetest.NewFakeGitLab(t, "alice", "pastebin-cli-store")
				app.gitlabAPIOverride = srv.URL + "/api/v4"
				return g
			},
		},
		{
			name: "bitbucket", provider: "bitbucket", token: "bb-token",
			login: []string{"login", "bitbucket", "--email", "me@example.com", "--stdin"},
			setup: func(t *testing.T, app *App) *storetest.FakeGit {
				srv, g := storetest.NewFakeBitbucket(t, "alice", "pastebin-cli-store")
				app.bitbucketAPI = srv.URL
				return g
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, ks := newBareApp(t)
			fake := tc.setup(t, app)

			if _, err := run(t, app, "wrong-token\n", tc.login...); err == nil {
				t.Fatalf("a bad token must be rejected")
			}
			out, err := run(t, app, tc.token+"\n", append([]string{"--json"}, tc.login...)...)
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			if !strings.Contains(out, `"id": "`+tc.provider+`:alice"`) {
				t.Fatalf("login output: %s", out)
			}
			cfg, _ := config.Load(app.paths)
			raw, _ := json.Marshal(cfg)
			if strings.Contains(string(raw), tc.token) {
				t.Fatalf("token must never be written to config.json")
			}
			found := false
			for k, v := range ks.M {
				if strings.HasPrefix(k, "cred:"+tc.provider) && strings.Contains(v, tc.token) {
					found = true
				}
			}
			if !found {
				t.Fatalf("token should be stored in the keystore")
			}

			out, err = run(t, app, "", "--json", "account")
			if err != nil || !strings.Contains(out, tc.provider+":alice") {
				t.Fatalf("account list: %s %v", out, err)
			}

			// Switching creates the private repo and the vault in one step.
			if _, err := run(t, app, "correct horse battery\n", "account", "switch", tc.provider+":alice", "--stdin"); err != nil {
				t.Fatalf("switch: %v", err)
			}
			fake.Mu.Lock()
			exists, private := fake.Exists, fake.Private
			fake.Mu.Unlock()
			if !exists || !private {
				t.Fatalf("repository should exist and be private")
			}

			out, err = run(t, app, "", "--json", "note", "buy", "oat", "milk", "--title", "Shopping")
			if err != nil || !strings.Contains(out, `"remote_saved": true`) {
				t.Fatalf("note: %s %v", out, err)
			}
			for _, leak := range []string{"Shopping", "oat milk", "shopping.md", "notes/"} {
				if fake.Contains(leak) {
					t.Fatalf("%s stored %q in clear", tc.name, leak)
				}
			}

			out, err = run(t, app, "", "--json", "status")
			if err != nil || !strings.Contains(out, `"provider": "`+tc.provider+`"`) || !strings.Contains(out, `"unlocked": true`) {
				t.Fatalf("status: %s %v", out, err)
			}

			// A second device unlocks with the passphrase and reads the note.
			dev2, _ := newBareApp(t)
			dev2.gitlabAPIOverride, dev2.bitbucketAPI = app.gitlabAPIOverride, app.bitbucketAPI
			if _, err := run(t, dev2, tc.token+"\n", tc.login...); err != nil {
				t.Fatalf("device 2 login: %v", err)
			}
			if _, err := run(t, dev2, "correct horse battery\n", "account", "switch", tc.provider+":alice", "--stdin"); err != nil {
				t.Fatalf("device 2 switch: %v", err)
			}
			out, err = run(t, dev2, "", "--json", "sync")
			if err != nil || !strings.Contains(out, "shopping.md") {
				t.Fatalf("device 2 sync: %s %v", out, err)
			}
		})
	}
}

func TestAccountIDParsing(t *testing.T) {
	cases := map[string][2]string{
		"alice":            {model.ProviderGitHub, "alice"},
		"@alice":           {model.ProviderGitHub, "alice"},
		"github:alice":     {model.ProviderGitHub, "alice"},
		"gitlab:bob.smith": {model.ProviderGitLab, "bob.smith"},
		"bb:carol":         {model.ProviderBitbucket, "carol"},
	}
	for in, want := range cases {
		p, l := parseAccountID(in)
		if p != want[0] || l != want[1] {
			t.Errorf("parseAccountID(%q) = %s,%s", in, p, l)
		}
	}
	if validOwner("github", "a/b") || !validOwner("gitlab", "group/sub") || validOwner("gitlab", "../x") {
		t.Errorf("owner validation is wrong")
	}
	if validHost("evil.com/path") || validHost("http://x") || !validHost("gitlab.example.com") || !validHost("git.corp.io:8443") {
		t.Errorf("host validation is wrong")
	}
}
