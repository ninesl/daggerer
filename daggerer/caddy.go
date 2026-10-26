package main

import (
	"context"
	"fmt"
	"path"

	"dagger/daggerer/internal/dagger"
)

const (
	// Remote and validation-container paths must be identical so the same
	// provisioning script runs byte-for-byte in both places.
	caddyConfigDir    = "/etc/caddy"
	caddyRootFile     = caddyConfigDir + "/Caddyfile"
	caddySitesAvail   = caddyConfigDir + "/sites-available"
	caddySitesEnabled = caddyConfigDir + "/sites-enabled"
	caddyLogDir       = "/var/log/caddy"

	// Staging paths inside the SSH client container; files are streamed from
	// here onto the target before provisioning runs.
	caddyStageDir = "/run/caddy-stage"

	// Image used for validation; it must contain the caddy binary.
	caddyValidateImage = "caddy:2-alpine"
)

// WithCaddy configures Caddyfile deployment for the next chained release or
// deploy call. At least one file must be provided.
//
//   - app-caddyfile is deployed verbatim to /etc/caddy/sites-available/<its
//     exact filename> and symlinked into /etc/caddy/sites-enabled using the
//     same filename. No renaming occurs.
//   - caddyfile overwrites the target's root /etc/caddy/Caddyfile. It is the
//     only Caddy input on single-service targets; combined with app-caddyfile
//     it bootstraps or updates the shared root config on multi-site targets.
//
// WARNING: caddyfile overwrites the parent Caddyfile shared by every enabled
// site on the target. Validation pulls the target's existing /etc/caddy tree
// into the validation container, so changes that would break other enabled
// sites fail before build or deploy. It is easy to forget that other apps share this file.
func (m *Daggerer) WithCaddy(
	ctx context.Context,
	// Site-specific *.caddy file, placed under sites-available using its exact
	// filename and symlinked into sites-enabled.
	// +optional
	appCaddyfile *dagger.File,
	// Root /etc/caddy/Caddyfile for the target. Overwrites the shared parent
	// Caddyfile when provided.
	// +optional
	caddyfile *dagger.File,
) (*Daggerer, error) {
	if appCaddyfile == nil && caddyfile == nil {
		return nil, fmt.Errorf("with-caddy requires app-caddyfile, caddyfile, or both")
	}

	appBase := ""
	if appCaddyfile != nil {
		name, err := appCaddyfile.Name(ctx)
		if err != nil {
			return nil, fmt.Errorf("read app-caddyfile name: %w", err)
		}
		if name != path.Base(name) || name == "." || name == ".." || name == "" {
			return nil, fmt.Errorf("app-caddyfile %q must be a plain filename without directories", name)
		}
		if name == "Caddyfile" {
			return nil, fmt.Errorf("app-caddyfile must not be named Caddyfile; pass it as caddyfile instead")
		}
		appBase = name
	}

	m.AppCaddyfile = appCaddyfile
	m.RootCaddyfile = caddyfile
	m.AppCaddyfileBase = appBase

	return m, nil
}

// hasCaddy reports whether the next release or deploy call has Caddy
// configured and must run validateCaddy and serveCaddy.
func (m *Daggerer) hasCaddy() bool {
	return m.AppCaddyfile != nil || m.RootCaddyfile != nil
}

// validateCaddy is the private pre-build DAG step: it validates the configured
// Caddy files against the target's real config in a caddy:2-alpine Dagger
// container, without touching the target or building anything.
//
// release and deploy run it BEFORE Build and image pull so an invalid
// Caddyfile, a target without caddy installed, or a root Caddyfile that would
// break other enabled sites aborts the call immediately.
//
// The target must already have caddy installed; validateCaddy never installs
// it. The target's existing /etc/caddy tree is pulled into the validation
// container so candidate files are checked against the real sibling sites,
// snippets, and imports before any build or deploy runs.
func (m *Daggerer) validateCaddy(p caddyParams) error {
	if err := remoteCaddyInstalled(p); err != nil {
		return err
	}
	synced, err := syncRemoteCaddy(p)
	if err != nil {
		return err
	}
	validationContainer, err := m.caddyValidationContainer(p.ctx, synced)
	if err != nil {
		return err
	}
	// TODO: run caddyProvisionCommand(m.RootCaddyfile != nil, m.AppCaddyfileBase, false)
	// inside validationContainer: create sites-available, sites-enabled, and
	// /var/log/caddy; move the staged files into place using their exact
	// filenames; symlink sites-enabled; then
	// caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile.
	_ = validationContainer
	return fmt.Errorf("validateCaddy: not implemented")
}

// ServeCaddy applies the configured Caddy files on the target and reloads
// caddy, after a final on-target caddy validate.
//
// Use it standalone by passing the files here, or chained after with-caddy:
//
//	dagger api call with-caddy --caddyfile=Caddyfile serve-caddy \
//	  --ssh-target=user@host --ssh-key=file://... --known-hosts=file://...
//
// ServeCaddy never installs caddy; the target must already have it. It is the
// same serving logic release and deploy run after compose up.
func (m *Daggerer) ServeCaddy(
	ctx context.Context,
	// Site-specific *.caddy file; overrides the with-caddy app file when provided.
	// +optional
	appCaddyfile *dagger.File,
	// Root /etc/caddy/Caddyfile; overrides the with-caddy root file when provided.
	// +optional
	caddyfile *dagger.File,
	// ssh destination: user@host
	sshTarget string,
	// SSH destination port.
	// +default=22
	sshTargetPort int,
	sshKey *dagger.Secret,
	knownHosts *dagger.Secret,
) error {
	if appCaddyfile != nil || caddyfile != nil {
		var err error
		if m, err = m.WithCaddy(ctx, appCaddyfile, caddyfile); err != nil {
			return err
		}
	}
	if !m.hasCaddy() {
		return fmt.Errorf("serve-caddy requires app-caddyfile or caddyfile, either here or chained with with-caddy")
	}

	sshClient, err := newSSHClient(ctx, sshKey, knownHosts)
	if err != nil {
		return err
	}
	p := caddyParams{
		ctx:           ctx,
		sshClient:     sshClient,
		sshTarget:     sshTarget,
		sshTargetPort: sshTargetPort,
	}

	if err := remoteCaddyInstalled(p); err != nil {
		return err
	}
	if _, err := m.stageCaddyFiles(sshClient); err != nil {
		return err
	}
	return m.serveCaddy(p)
}

// newSSHClient builds the shared SSH client container with the ssh key and
// known-hosts secrets mounted. Release, deploy, validate-caddy, and
// serve-caddy all reuse one client per call chain instead of rebuilding it.
func newSSHClient(ctx context.Context, sshKey, knownHosts *dagger.Secret) (*dagger.Container, error) {
	_ = ctx
	_ = sshKey
	_ = knownHosts
	return nil, fmt.Errorf("newSSHClient: not implemented")
}

// caddyParams carries everything the Caddy steps need. All values are shared
// with deployComposeImage so SSH credentials and target details are only
// resolved once per call chain.
type caddyParams struct {
	ctx           context.Context
	sshClient     *dagger.Container
	sshTarget     string
	sshTargetPort int
	remoteHome    string // unused until impl; reserved for the ~/caddy convenience symlink
}

// serveCaddy applies the already-validated Caddy files on the target after
// compose up succeeded.
//
// Steps once implemented:
//  1. Stream the staged RootCaddyfile and AppCaddyfile from the SSH client
//     container onto the target.
//  2. Run caddyProvisionCommand(remote=true) on the target: the exact same
//     provisioning as the validation container, plus the $HOME/caddy ->
//     /etc/caddy convenience symlink and systemctl reload caddy (restart
//     fallback) after a final on-target caddy validate.
func (m *Daggerer) serveCaddy(p caddyParams) error {
	return fmt.Errorf("serveCaddy: not implemented")
}

// caddyProvisionCommand generates the single shell script shared by the
// caddy:2-alpine validation container and the remote target. Identical
// paths and identical behavior mean a validated config applies verbatim.
//
// The script, once implemented, must:
//  1. mkdir -p /etc/caddy/sites-available /etc/caddy/sites-enabled /var/log/caddy
//  2. If hasRoot: mv the staged root file to /etc/caddy/Caddyfile.
//  3. Fail with a clear error if /etc/caddy/Caddyfile still does not exist
//     (the target must be bootstrapped with --caddyfile first).
//  4. If appFilename != "": mv the staged app file to
//     /etc/caddy/sites-available/<appFilename> and
//     ln -sf ../sites-available/<appFilename> sites-enabled/<appFilename>.
//  5. caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile.
//  6. remote only: ln -sfn /etc/caddy "$HOME"/caddy and
//     systemctl reload caddy (restart fallback).
func caddyProvisionCommand(hasRoot bool, appFilename string, remote bool) string {
	_ = hasRoot
	_ = appFilename
	_ = remote
	return "true" // TODO: generate the shared provisioning script
}

// syncRemoteCaddy pulls the target's existing /etc/caddy tree into the
// validation container so candidate files are validated against the real
// sibling sites, snippets, and imports — not an empty directory.
//
// Returns nil when /etc/caddy does not exist on the target yet (first
// bootstrap with --caddyfile).
func syncRemoteCaddy(p caddyParams) (*dagger.Directory, error) {
	return nil, fmt.Errorf("syncRemoteCaddy: not implemented")
}

// caddyValidationContainer builds the caddy:2-alpine container with the
// synced /etc/caddy tree mounted at the identical /etc/caddy path and the
// candidate files staged for caddyProvisionCommand(remote=false).
func (m *Daggerer) caddyValidationContainer(ctx context.Context, synced *dagger.Directory) (*dagger.Container, error) {
	_ = ctx
	_ = synced
	return nil, fmt.Errorf("caddyValidationContainer: not implemented")
}

// stageCaddyFiles mounts the RootCaddyfile and AppCaddyfile into the SSH
// client container's caddyStageDir so they can be streamed to the target by
// serveCaddy. The exact source filenames are preserved.
func (m *Daggerer) stageCaddyFiles(sshClient *dagger.Container) (*dagger.Container, error) {
	_ = sshClient
	return nil, fmt.Errorf("stageCaddyFiles: not implemented")
}

// remoteCaddyInstalled verifies the caddy binary exists on the target.
// WithCaddy never installs caddy; a missing binary requires human
// intervention and aborts the call chain.
func remoteCaddyInstalled(p caddyParams) error {
	_ = p
	return fmt.Errorf("remoteCaddyInstalled: not implemented")
}
