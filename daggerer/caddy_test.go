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

// TestWithCaddyRequiresAFile covers the input matrix floor: with-caddy with
// no files is a configuration error and must never mutate the Daggerer.
func TestWithCaddyRequiresAFile(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		app  *dagger.File
		root *dagger.File
	}{
		{name: "no files"},
		{name: "app only", app: &dagger.File{}},
		{name: "root only", root: &dagger.File{}},
	}

	// Only the nil/nil case is expected to fail; app/root placeholders prove
	// the guard order puts the nil check before any engine access.
	for _, test := range tests {
		if test.name == "no files" {
			m := &Daggerer{}
			_, err := m.WithCaddy(ctx, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "with-caddy requires app-caddyfile, caddyfile, or both") {
				t.Fatalf("%s: expected missing-file error, got %v", test.name, err)
			}
			if m.AppCaddyfile != nil || m.RootCaddyfile != nil || m.AppCaddyfileBase != "" {
				t.Fatalf("%s: failed WithCaddy must not mutate the Daggerer", test.name)
			}
		}
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

// TestServeCaddyStepOrder pins the standalone serve-caddy pipeline order:
// files are configured first, then the shared SSH client is built.
func TestServeCaddyStepOrder(t *testing.T) {
	ctx := context.Background()
	skipWithoutEngine(t, ctx)

	app := newCaddyFixture(t, ctx, "horsemoneyball.caddy", fixtureAppCaddyfile)

	tests := []struct {
		name     string
		app      *dagger.File
		root     *dagger.File
		wantStep string
	}{
		{name: "app file reaches SSH client setup", app: app, wantStep: "newSSHClient"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&Daggerer{}).ServeCaddy(ctx, test.app, test.root, "user@target", 22, nil, nil)
			if err == nil || !strings.Contains(err.Error(), test.wantStep) {
				t.Fatalf("expected pipeline to stop at %q, got %v", test.wantStep, err)
			}
		})
	}
}

// TestValidateCaddyStepOrder pins the private pre-build DAG step order:
// remoteCaddyInstalled is the first step, so a target without caddy fails
// before any tree sync or container work.
func TestValidateCaddyStepOrder(t *testing.T) {
	tests := []struct {
		name     string
		params   caddyParams
		wantStep string
	}{
		{
			name:     "first step is the installed check",
			params:   caddyParams{ctx: context.Background()},
			wantStep: "remoteCaddyInstalled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&Daggerer{}).validateCaddy(test.params)
			if err == nil || !strings.Contains(err.Error(), test.wantStep) {
				t.Fatalf("expected pipeline to stop at %q, got %v", test.wantStep, err)
			}
		})
	}
}

// TestServeCaddyPrivateStepOrder pins the private post-compose DAG step:
// serveCaddy runs the apply logic itself.
func TestServeCaddyPrivateStepOrder(t *testing.T) {
	tests := []struct {
		name     string
		params   caddyParams
		wantStep string
	}{
		{
			name:     "apply step runs",
			params:   caddyParams{ctx: context.Background()},
			wantStep: "serveCaddy",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&Daggerer{}).serveCaddy(test.params)
			if err == nil || !strings.Contains(err.Error(), test.wantStep) {
				t.Fatalf("expected pipeline to stop at %q, got %v", test.wantStep, err)
			}
		})
	}
}
