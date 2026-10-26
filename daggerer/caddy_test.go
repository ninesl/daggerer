package main

import (
	"context"
	"strings"
	"testing"

	"dagger/daggerer/internal/dagger"
)

// skipWithoutEngine skips engine-backed cases when no Dagger engine is
// reachable, so `go test` still runs the pure-logic cases anywhere.
func skipWithoutEngine(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, err := dag.Directory().WithNewFile("probe", "probe").File("probe").Name(ctx); err != nil {
		t.Skipf("requires a Dagger engine: %v", err)
	}
}

// newCaddyFixture creates a *dagger.File with the given name and contents.
func newCaddyFixture(t *testing.T, ctx context.Context, name, contents string) *dagger.File {
	t.Helper()
	file := dag.Directory().WithNewFile(name, contents).File(name)
	if _, err := file.Name(ctx); err != nil {
		t.Fatalf("create fixture %s: %v", name, err)
	}
	return file
}

const (
	fixtureRootCaddyfile = `
{
	email ninescoding@gmail.com
}

(site_json_log) {
	log {
		output file /var/log/caddy/{args.0}.access.json {
			roll_size 50MiB
			roll_keep 5
		}
	}
}

import /etc/caddy/sites-enabled/*.caddy
`
	fixtureAppCaddyfile = `horsemoneyball.example.com {
	import site_json_log horsemoneyball
	reverse_proxy 127.0.0.1:6767
}
`
)

// TestWithCaddyRequiresAFile covers the guard: with-caddy with no files is
// a configuration error and must never mutate the Daggerer.
func TestWithCaddyRequiresAFile(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		app  *dagger.File
		root *dagger.File
	}{
		{name: "no files"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := &Daggerer{}
			_, err := m.WithCaddy(ctx, test.app, test.root)
			if err == nil || !strings.Contains(err.Error(), "with-caddy requires app-caddyfile, caddyfile, or both") {
				t.Fatalf("expected missing-file error, got %v", err)
			}
			if m.AppCaddyfile != nil || m.RootCaddyfile != nil || m.AppCaddyfileBase != "" {
				t.Fatal("failed WithCaddy must not mutate the Daggerer")
			}
		})
	}
}

// TestWithCaddyFilenames covers app-caddyfile name handling: exact filenames
// are preserved verbatim, and the reserved Caddyfile name is rejected.
func TestWithCaddyFilenames(t *testing.T) {
	ctx := context.Background()
	skipWithoutEngine(t, ctx)

	tests := []struct {
		name     string
		filename string
		contents string
		wantErr  string
		wantBase string
	}{
		{
			name:     "plain site name",
			filename: "horsemoneyball.caddy",
			contents: fixtureAppCaddyfile,
			wantBase: "horsemoneyball.caddy",
		},
		{
			name:     "single-service root-only target still allows app files",
			filename: "lance-vm1.caddy",
			contents: fixtureAppCaddyfile,
			wantBase: "lance-vm1.caddy",
		},
		{
			name:     "dots and dashes preserved verbatim",
			filename: "the-shroommelier.v2.caddy",
			contents: fixtureAppCaddyfile,
			wantBase: "the-shroommelier.v2.caddy",
		},
		{
			name:     "reserved root Caddyfile name rejected",
			filename: "Caddyfile",
			contents: fixtureRootCaddyfile,
			wantErr:  "app-caddyfile must not be named Caddyfile",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := newCaddyFixture(t, ctx, test.filename, test.contents)
			m, err := (&Daggerer{}).WithCaddy(ctx, app, nil)

			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("expected error %q, got %v", test.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.AppCaddyfileBase != test.wantBase {
				t.Fatalf("AppCaddyfileBase = %q, want %q (no renaming)", m.AppCaddyfileBase, test.wantBase)
			}
			if got, err := m.AppCaddyfile.Name(ctx); err != nil || got != test.wantBase {
				t.Fatalf("AppCaddyfile name = %q (%v), want %q", got, err, test.wantBase)
			}
			if m.RootCaddyfile != nil {
				t.Fatal("RootCaddyfile must stay nil when only app-caddyfile is passed")
			}
		})
	}
}

// TestWithCaddyInputMatrix covers the root/app combinations from the plan:
// app only, root only, and both (bootstrap).
func TestWithCaddyInputMatrix(t *testing.T) {
	ctx := context.Background()
	skipWithoutEngine(t, ctx)

	tests := []struct {
		name     string
		app      bool
		root     bool
		wantHas  bool
		wantBase string
	}{
		{name: "app only", app: true, wantHas: true, wantBase: "horsemoneyball.caddy"},
		{name: "root only", root: true, wantHas: true, wantBase: ""},
		{name: "bootstrap both", app: true, root: true, wantHas: true, wantBase: "horsemoneyball.caddy"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := &Daggerer{}

			var app, root *dagger.File
			if test.app {
				app = newCaddyFixture(t, ctx, "horsemoneyball.caddy", fixtureAppCaddyfile)
			}
			if test.root {
				root = newCaddyFixture(t, ctx, "Caddyfile", fixtureRootCaddyfile)
			}

			m, err := m.WithCaddy(ctx, app, root)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if m.hasCaddy() != test.wantHas {
				t.Fatalf("hasCaddy = %v, want %v", m.hasCaddy(), test.wantHas)
			}
			if m.AppCaddyfileBase != test.wantBase {
				t.Fatalf("AppCaddyfileBase = %q, want %q", m.AppCaddyfileBase, test.wantBase)
			}
			if (m.AppCaddyfile != nil) != test.app {
				t.Fatalf("AppCaddyfile set = %v, want %v", m.AppCaddyfile != nil, test.app)
			}
			if (m.RootCaddyfile != nil) != test.root {
				t.Fatalf("RootCaddyfile set = %v, want %v", m.RootCaddyfile != nil, test.root)
			}
		})
	}
}

// TestWithCaddyChainingPinsFileState pins the direct-assignment chaining
// semantics: a second with-caddy replaces the files, and other Daggerer state
// (build secrets) survives.
func TestWithCaddyChainingPinsFileState(t *testing.T) {
	ctx := context.Background()
	skipWithoutEngine(t, ctx)

	first := newCaddyFixture(t, ctx, "horsemoneyball.caddy", fixtureAppCaddyfile)
	second := newCaddyFixture(t, ctx, "vinylkeeper.caddy", fixtureAppCaddyfile)
	root := newCaddyFixture(t, ctx, "Caddyfile", fixtureRootCaddyfile)

	m := &Daggerer{BuildSecretIDs: []string{"github_token"}}
	m, err := m.WithCaddy(ctx, first, nil)
	if err != nil {
		t.Fatalf("first with-caddy: %v", err)
	}

	// A chained with-build-secret equivalent: state added before with-caddy
	// must survive (WithCaddy assigns its fields directly).
	if len(m.BuildSecretIDs) != 1 || m.BuildSecretIDs[0] != "github_token" {
		t.Fatalf("BuildSecretIDs = %v, want [github_token]", m.BuildSecretIDs)
	}

	m, err = m.WithCaddy(ctx, second, root)
	if err != nil {
		t.Fatalf("second with-caddy: %v", err)
	}
	if m.AppCaddyfileBase != "vinylkeeper.caddy" {
		t.Fatalf("AppCaddyfileBase = %q, want replacement file vinylkeeper.caddy", m.AppCaddyfileBase)
	}
	if m.RootCaddyfile == nil {
		t.Fatal("RootCaddyfile must be set by the second call")
	}
	if len(m.BuildSecretIDs) != 1 {
		t.Fatalf("BuildSecretIDs = %v, want [github_token] preserved across with-caddy", m.BuildSecretIDs)
	}
}

// TestHasCaddy covers the flag release and deploy branch on.
func TestHasCaddy(t *testing.T) {
	tests := []struct {
		name string
		app  bool
		root bool
		want bool
	}{
		{name: "no caddy configured"},
		{name: "app configured", app: true, want: true},
		{name: "root configured", root: true, want: true},
		{name: "both configured", app: true, root: true, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := &Daggerer{}
			if test.app {
				m.AppCaddyfile = &dagger.File{}
			}
			if test.root {
				m.RootCaddyfile = &dagger.File{}
			}
			if got := m.hasCaddy(); got != test.want {
				t.Fatalf("hasCaddy = %v, want %v", got, test.want)
			}
		})
	}
}

// TestValidateScriptInCaddyContainer runs the real provisioning script inside
// the real caddy:2-alpine validation container against a synced-tree fixture:
// the full validateCaddy behavior minus the SSH target.
func TestValidateScriptInCaddyContainer(t *testing.T) {
	ctx := context.Background()
	skipWithoutEngine(t, ctx)

	// A synced tree as it would be pulled from a bootstrapped target: an
	// existing root Caddyfile.
	bootstrapped := dag.Directory().WithNewFile("Caddyfile", fixtureRootCaddyfile)

	tests := []struct {
		name      string
		synced    *dagger.Directory
		app       string
		root      bool
		wantValid bool
	}{
		{
			name:      "bootstrap root and app against empty target tree",
			synced:    dag.Directory(),
			app:       "horsemoneyball.caddy",
			root:      true,
			wantValid: true,
		},
		{
			name:      "app only against bootstrapped tree",
			synced:    bootstrapped,
			app:       "horsemoneyball.caddy",
			wantValid: true,
		},
		{
			name:   "app only against empty tree must fail: no root Caddyfile",
			synced: dag.Directory(),
			app:    "horsemoneyball.caddy",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := &Daggerer{AppCaddyfileBase: test.app}
			if test.app != "" {
				m.AppCaddyfile = newCaddyFixture(t, ctx, test.app, fixtureAppCaddyfile)
			}
			if test.root {
				m.RootCaddyfile = newCaddyFixture(t, ctx, "Caddyfile", fixtureRootCaddyfile)
			}

			script := caddyProvisionCommand(test.root, test.app, false)
			container := m.caddyValidationContainer(test.synced)
			_, err := container.WithExec([]string{"sh", "-c", script}).Sync(ctx)

			if test.wantValid && err != nil {
				t.Fatalf("expected the provisioned config to validate, got: %v", err)
			}
			if !test.wantValid && err == nil {
				t.Fatal("expected failure: the target tree has no Caddyfile to hang the site off")
			}
		})
	}
}

// TestCaddyProvisionCommand pins the shared provisioning script: the exact
// same generated script runs in the validation container (remote=false) and
// on the target (remote=true).
func TestCaddyProvisionCommand(t *testing.T) {
	tests := []struct {
		name     string
		hasRoot  bool
		appFile  string
		remote   bool
		contains []string
		absent   []string
	}{
		{
			name:    "bootstrap root and app, validation container",
			hasRoot: true,
			appFile: "horsemoneyball.caddy",
			contains: []string{
				"set -e",
				"mkdir -p '/etc/caddy/sites-available' '/etc/caddy/sites-enabled' '/var/log/caddy'",
				"cp '/tmp/caddy-stage/Caddyfile' '/etc/caddy/Caddyfile'",
				"cp '/tmp/caddy-stage/horsemoneyball.caddy' '/etc/caddy/sites-available/horsemoneyball.caddy'",
				"ln -sfn '../sites-available/horsemoneyball.caddy' '/etc/caddy/sites-enabled/horsemoneyball.caddy'",
				"caddy validate --config '/etc/caddy/Caddyfile' --adapter caddyfile",
			},
			absent: []string{"systemctl", "$HOME", "rm -rf"},
		},
		{
			name:    "app only against existing root, validation container",
			appFile: "vinylkeeper.caddy",
			contains: []string{
				"test -f '/etc/caddy/Caddyfile' || {",
				"bootstrap the target with --caddyfile first",
				"cp '/tmp/caddy-stage/vinylkeeper.caddy' '/etc/caddy/sites-available/vinylkeeper.caddy'",
			},
			absent: []string{
				"cp '/tmp/caddy-stage/Caddyfile'",
				"systemctl",
			},
		},
		{
			name:    "root only single-service target, remote",
			hasRoot: true,
			remote:  true,
			contains: []string{
				"cp '/tmp/caddy-stage/Caddyfile' '/etc/caddy/Caddyfile'",
				"systemctl reload caddy || systemctl restart caddy",
				"ln -sfn /etc/caddy \"$HOME\"/caddy",
				"rm -rf '/tmp/caddy-stage'",
			},
			absent: []string{"sites-available/"},
		},
		{
			name:    "app only, remote",
			appFile: "the-shroommelier.v2.caddy",
			remote:  true,
			contains: []string{
				"ln -sfn '../sites-available/the-shroommelier.v2.caddy' '/etc/caddy/sites-enabled/the-shroommelier.v2.caddy'",
				"systemctl reload caddy || systemctl restart caddy",
			},
			absent: []string{"cp '/tmp/caddy-stage/Caddyfile'"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := caddyProvisionCommand(test.hasRoot, test.appFile, test.remote)
			for _, want := range test.contains {
				if !strings.Contains(script, want) {
					t.Errorf("script missing %q:\n%s", want, script)
				}
			}
			for _, banned := range test.absent {
				if strings.Contains(script, banned) {
					t.Errorf("script must not contain %q:\n%s", banned, script)
				}
			}
		})
	}
}

// TestServeCaddyRequiresConfiguredFiles covers the standalone entry point's
// guard: without files (here or chained via with-caddy) it must fail before
// touching SSH.
func TestServeCaddyRequiresConfiguredFiles(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		app  *dagger.File
		root *dagger.File
	}{
		{name: "no files anywhere"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&Daggerer{}).ServeCaddy(ctx, test.app, test.root, "user@target", 22, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "serve-caddy requires app-caddyfile or caddyfile") {
				t.Fatalf("expected missing-file guard error, got %v", err)
			}
		})
	}
}
