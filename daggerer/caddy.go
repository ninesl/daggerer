package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"path"
	"strings"

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

	// Staging path for the candidate files, inside the SSH client container,
	// the validation container, AND on the remote target. /tmp is the one
	// path writable by a non-root user in all three places.
	caddyStageDir = "/tmp/caddy-stage"

	// Where the synced remote tree lands inside the SSH client container
	// before it is extracted into the validation container.
	caddySyncDir = "/tmp/caddy-sync"

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
	validation := m.caddyValidationContainer(synced)
	script := caddyProvisionCommand(m.RootCaddyfile != nil, m.AppCaddyfileBase, false)
	if _, err := validation.WithExec([]string{"sh", "-c", script}).Sync(p.ctx); err != nil {
		return fmt.Errorf("caddy config invalid for %s: %w", p.sshTarget, err)
	}
	return nil
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
	p.sshClient = m.stageCaddyFiles(sshClient)
	return m.serveCaddy(p)
}

// newSSHClient builds the shared SSH client container with the ssh key and
// known-hosts secrets mounted. Release, deploy, validate-caddy, and
// serve-caddy all reuse one client per call chain instead of rebuilding it.
//
// The nonce env variable keeps the engine from caching SSH exec layers
// across runs: a cached success cannot revalidate authorization.
func newSSHClient(ctx context.Context, sshKey, knownHosts *dagger.Secret) (*dagger.Container, error) {
	client, err := dag.Container().From("alpine:3.24.1").
		WithExec([]string{"apk", "add", "--no-cache", "openssh-client"}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("ssh client container could not be created: %w", err)
	}
	return client.
		WithEnvVariable("DAGGERER_EXEC_NONCE", rand.Text()).
		WithMountedSecret(sshKeyMountPath, sshKey, dagger.ContainerWithMountedSecretOpts{Mode: 0600}).
		WithMountedSecret(knownHostsMountPath, knownHosts), nil
}

// caddyParams carries everything the Caddy steps need. All values are shared
// with deployComposeImage so SSH credentials and target details are only
// resolved once per call chain.
type caddyParams struct {
	ctx           context.Context
	sshClient     *dagger.Container
	sshTarget     string
	sshTargetPort int
}

// serveCaddy applies the already-validated Caddy files on the target after
// compose up succeeded:
//
//  1. Stream the staged RootCaddyfile and AppCaddyfile from the SSH client
//     container onto the target at the same staging path.
//  2. Run caddyProvisionCommand(remote=true) on the target: the exact same
//     provisioning as the validation container, plus the $HOME/caddy ->
//     /etc/caddy convenience symlink and systemctl reload caddy (restart
//     fallback) after a final on-target caddy validate.
func (m *Daggerer) serveCaddy(p caddyParams) error {
	// Stream the staged files over the SSH connection as a tar pipe. The
	// remote side lands them at the identical staging path.
	remoteExtract := shellCommand("mkdir", "-p", caddyStageDir) + " && " +
		shellCommand("tar", "-C", caddyStageDir, "-xf", "-")
	stream := shellCommand("tar", "-C", caddyStageDir, "-cf", "-", ".") + " | " +
		shellCommand(sshExec(p.sshTarget, p.sshTargetPort, remoteExtract)...)
	if _, err := p.sshClient.WithExec([]string{"sh", "-c", stream}).Sync(p.ctx); err != nil {
		return fmt.Errorf("stream caddy files to %s: %w", p.sshTarget, err)
	}

	script := caddyProvisionCommand(m.RootCaddyfile != nil, m.AppCaddyfileBase, true)
	if err := runSSH(p.ctx, p.sshClient, p.sshTarget, p.sshTargetPort, script); err != nil {
		return fmt.Errorf("apply caddy config on %s: %w", p.sshTarget, err)
	}
	return nil
}

// caddyProvisionCommand generates the single shell script shared by the
// caddy:2-alpine validation container and the remote target. Identical
// paths and identical behavior mean a validated config applies verbatim.
//
// The script:
//  1. mkdir -p /etc/caddy/sites-available /etc/caddy/sites-enabled /var/log/caddy
//  2. If hasRoot: cp the staged root file to /etc/caddy/Caddyfile.
//  3. Fail with a clear error if /etc/caddy/Caddyfile still does not exist
//     (the target must be bootstrapped with --caddyfile first).
//  4. If appFilename != "": cp the staged app file to
//     /etc/caddy/sites-available/<appFilename> and
//     ln -sfn ../sites-available/<appFilename> sites-enabled/<appFilename>.
//  5. caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile.
//  6. remote only: ln -sfn /etc/caddy "$HOME"/caddy and
//     systemctl reload caddy (restart fallback), then clean the stage dir.
//
// cp (not mv) is used for the staged files because they arrive on a
// read-only mount in the validation container; cp works identically in both
// places.
func caddyProvisionCommand(hasRoot bool, appFilename string, remote bool) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString("mkdir -p " + shellCommand(caddySitesAvail, caddySitesEnabled, caddyLogDir) + "\n")
	if hasRoot {
		b.WriteString("cp " + shellCommand(caddyStageDir+"/Caddyfile", caddyRootFile) + "\n")
	}
	b.WriteString("test -f " + shellQuote(caddyRootFile) + " || { echo " +
		shellQuote(caddyRootFile+" does not exist; bootstrap the target with --caddyfile first") + " >&2; exit 1; }\n")
	if appFilename != "" {
		b.WriteString("cp " + shellCommand(path.Join(caddyStageDir, appFilename), path.Join(caddySitesAvail, appFilename)) + "\n")
		b.WriteString("ln -sfn " + shellCommand(path.Join("..", "sites-available", appFilename), path.Join(caddySitesEnabled, appFilename)) + "\n")
	}
	b.WriteString("caddy validate --config " + shellQuote(caddyRootFile) + " --adapter caddyfile\n")
	if remote {
		b.WriteString("ln -sfn /etc/caddy \"$HOME\"/caddy\n")
		b.WriteString("systemctl reload caddy || systemctl restart caddy\n")
		b.WriteString("rm -rf " + shellQuote(caddyStageDir) + "\n")
	}
	return b.String()
}

// syncRemoteCaddy pulls the target's existing /etc/caddy tree into the SSH
// client container so candidate files are validated against the real sibling
// sites, snippets, and imports — not an empty directory.
//
// Returns an empty directory when /etc/caddy does not exist on the target yet
// (first bootstrap with --caddyfile). remoteCaddyInstalled has already run,
// so a failed connection is not mistaken for a missing tree.
func syncRemoteCaddy(p caddyParams) (*dagger.Directory, error) {
	if err := runSSH(p.ctx, p.sshClient, p.sshTarget, p.sshTargetPort, "test -d "+shellQuote(caddyConfigDir)); err != nil {
		return dag.Directory(), nil
	}
	remoteTar := shellCommand("tar", "-C", "/etc", "-cf", "-", "caddy")
	stream := shellCommand(sshExec(p.sshTarget, p.sshTargetPort, remoteTar)...) + " | tar -C " + shellQuote(caddySyncDir) + " -xf -"
	client := p.sshClient.
		WithExec([]string{"mkdir", "-p", caddySyncDir}).
		WithExec([]string{"sh", "-c", stream})
	if _, err := client.Sync(p.ctx); err != nil {
		return nil, fmt.Errorf("pull %s from %s: %w", caddyConfigDir, p.sshTarget, err)
	}
	return client.Directory(caddySyncDir + "/caddy"), nil
}

// caddyValidationContainer builds the caddy:2-alpine container with the
// target's synced /etc/caddy tree materialized at the identical /etc/caddy
// path and the candidate files staged for caddyProvisionCommand(remote=false).
//
// The image's stock /etc/caddy (default welcome-page Caddyfile) is removed
// first so the container's /etc/caddy is exactly the target's tree — byte
// identical state, including the no-config-yet bootstrap case. The synced
// tree is then copied from its read-only mount into the container's writable
// /etc/caddy layer, preserving symlinks, so the provisioning script sees
// the same state it will see on the target.
func (m *Daggerer) caddyValidationContainer(synced *dagger.Directory) *dagger.Container {
	container := dag.Container().From(caddyValidateImage).
		WithMountedDirectory(caddySyncDir, synced).
		WithExec([]string{"sh", "-c", "rm -rf " + shellQuote(caddyConfigDir) +
			" && mkdir -p " + shellQuote(caddyConfigDir) +
			" && cp -a " + shellQuote(caddySyncDir+"/.") + " " + shellQuote(caddyConfigDir+"/")})
	if m.RootCaddyfile != nil {
		container = container.WithMountedFile(caddyStageDir+"/Caddyfile", m.RootCaddyfile)
	}
	if m.AppCaddyfile != nil {
		container = container.WithMountedFile(path.Join(caddyStageDir, m.AppCaddyfileBase), m.AppCaddyfile)
	}
	return container
}

// stageCaddyFiles mounts the RootCaddyfile and AppCaddyfile into the SSH
// client container's caddyStageDir so they can be streamed to the target by
// serveCaddy. The exact source filenames are preserved.
func (m *Daggerer) stageCaddyFiles(sshClient *dagger.Container) *dagger.Container {
	if m.RootCaddyfile != nil {
		sshClient = sshClient.WithMountedFile(caddyStageDir+"/Caddyfile", m.RootCaddyfile)
	}
	if m.AppCaddyfile != nil {
		sshClient = sshClient.WithMountedFile(path.Join(caddyStageDir, m.AppCaddyfileBase), m.AppCaddyfile)
	}
	return sshClient
}

// remoteCaddyInstalled verifies the caddy binary exists on the target.
// Daggerer never installs caddy; a missing binary requires human
// intervention and aborts the call chain.
func remoteCaddyInstalled(p caddyParams) error {
	if err := runSSH(p.ctx, p.sshClient, p.sshTarget, p.sshTargetPort, "command -v caddy"); err != nil {
		return fmt.Errorf("caddy is not installed on %s: daggerer never installs it, install caddy on the target manually and retry: %w", p.sshTarget, err)
	}
	return nil
}
