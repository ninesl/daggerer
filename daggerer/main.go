package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"time"

	"dagger/daggerer/internal/dagger"
)

type Daggerer struct{}

// Build a container without publishing it.
func (m *Daggerer) BuildOnly(
	ctx context.Context,
	// Application checkout to use as the build context.
	source *dagger.Directory,
	// Public .env file, parsed by Dagger. Never supply credentials here.
	// +optional
	buildEnvFile *dagger.File,
	// Public values constructed with EnvFile.WithVariable; override file values.
	// +optional
	buildValues *dagger.EnvFile,
	// Private .env mounted intact as the BuildKit secret build_env.
	// +optional
	buildSecretEnv *dagger.Secret,
	// BuildKit secret IDs, paired by position with buildSecrets.
	// +optional
	buildSecretIDs []string,
	// Secret values, paired by position with buildSecretIDs.
	// +optional
	buildSecrets []*dagger.Secret,
	// Dockerfile path relative to the build context.
	// +default="Dockerfile"
	dockerfile string,
) (*dagger.Container, error) {
	opts := dagger.DirectoryDockerBuildOpts{
		Dockerfile: dockerfile,
	}
	args, err := buildArguments(ctx, buildEnvFile, buildValues)
	if err != nil {
		return nil, err
	}
	opts.BuildArgs = args
	if len(buildSecretIDs) != len(buildSecrets) {
		return nil, fmt.Errorf("build-secret-ids and build-secrets must have equal lengths")
	}
	seen := map[string]bool{}
	if buildSecretEnv != nil {
		seen["build_env"] = true
		secret, err := namedBuildSecret(ctx, "build_env", buildSecretEnv)
		if err != nil {
			return nil, err
		}
		opts.Secrets = append(opts.Secrets, secret)
	}
	for i, id := range buildSecretIDs {

		// TODO: we probably have a better way to validate this
		if id == "" || strings.ContainsAny(id, "/\\\x00\r\n") || id == "." || id == ".." || seen[id] {
			return nil, fmt.Errorf("invalid or duplicate build secret ID %q", id)
		}
		seen[id] = true
		if buildSecrets[i] == nil {
			return nil, fmt.Errorf("missing build secret %q", id)
		}
		secret, err := namedBuildSecret(ctx, id, buildSecrets[i])
		if err != nil {
			return nil, err
		}
		opts.Secrets = append(opts.Secrets, secret)
	}
	return source.DockerBuild(opts), nil
}

// Build and publish an image. Returns the deployed image and tag.
// +cache="never"
func (m *Daggerer) Build(
	ctx context.Context,
	// Application checkout to use as the build context.
	source *dagger.Directory,
	// Registry hostname, optionally including its port.
	registry string,
	// Image repository name inside the registry.
	appName string,
	// +default="latest"
	tag string,
	registryUsername string,
	registryPassword *dagger.Secret,
	// Dockerfile path relative to the build context.
	// +default="Dockerfile"
	dockerfile string,
	// Public .env file, parsed by Dagger. Never supply credentials here.
	// +optional
	buildEnvFile *dagger.File,
	// Public values constructed with EnvFile.WithVariable; overrides .env file values.
	// +optional
	buildValues *dagger.EnvFile,
	// Private .env mounted intact as the BuildKit secret build_env.
	// +optional
	buildSecretEnv *dagger.Secret,
	// BuildKit secret IDs, paired by position with buildSecrets.
	// +optional
	buildSecretIDs []string,
	// Secret values, paired by position with buildSecretIDs.
	// +optional
	buildSecrets []*dagger.Secret,
) (string, error) {
	// TODO: we need a way to determine if the registry auth is valid before we build, this is wasting work otherwise?

	container, err := m.BuildOnly(ctx, source, buildEnvFile, buildValues, buildSecretEnv, buildSecretIDs, buildSecrets, dockerfile)
	if err != nil {
		return "", err
	}
	image := registry + "/" + appName + ":" + tag
	published, err := container.
		WithRegistryAuth(registry, registryUsername, registryPassword).
		Publish(ctx, image)
	if err != nil {
		return "", fmt.Errorf("publish %s: %w", image, err)
	}
	return published, nil
}

// Build, publish, and deploy an image tag.
// +cache="never"
func (m *Daggerer) Release(
	ctx context.Context,
	source *dagger.Directory,
	// OCR Image registry host name
	registry string,
	// OCR Image repository name inside the registry
	appName string,
	// What tag to use for this release
	// +default="latest"
	tag string,
	// ssh deploy destination target in user@host form
	sshTarget string,
	sshKey *dagger.Secret,
	knownHosts *dagger.Secret,
	// Remote directory relative to the SSH user's home
	deployDirectory string,
	// Compose file name that is in the deploy directory.
	// +default="compose.yml"
	composeFile string,
	// docker or podman. Is used on the VPS that has your `compose.yml`
	deployContainerRuntime string,
	// Container image used for the SSH client.
	// +default=
	sshImage string,
	registryUsername string,
	registryPassword *dagger.Secret,
	// Registry password mount path inside helper containers.
	// Public .env file, parsed by Dagger. You really shouldn't supply credentials here.
	// +optional
	buildEnvFile *dagger.File,
	// Public values constructed with EnvFile.WithVariable; collisions override file values.
	// +optional
	buildValues *dagger.EnvFile,
	// Private .env mounted intact as the BuildKit secret build_env.
	// +optional
	buildSecretEnv *dagger.Secret,
	// BuildKit secret IDs, paired by position with buildSecrets.
	// +optional
	buildSecretIDs []string,
	// Secret values, paired by position with buildSecretIDs.
	// +optional
	buildSecrets []*dagger.Secret,
	// +default="Dockerfile"
	dockerfile string,
) error {
	if err := validateDeployRuntime(deployContainerRuntime); err != nil {
		return err
	}
	_, err := m.Build(ctx, source, registry, appName, tag, registryUsername, registryPassword, buildEnvFile, buildValues, buildSecretEnv, buildSecretIDs, buildSecrets, dockerfile)
	if err != nil {
		return err
	}
	return m.Deploy(ctx, registry, appName, tag, sshTarget, sshKey, knownHosts, deployDirectory, deployContainerRuntime, composeFile, sshImage, registryUsername, registryPassword)
}

// Deploy an existing image with Compose.
// +cache="never"
func (m *Daggerer) Deploy(
	ctx context.Context,
	// Registry hostname, optionally including its port.
	registry string,
	// Image repository name inside the registry.
	appName string,
	// +default="latest"
	tag string,
	// ssh destination: user@host
	sshTarget string,
	sshKey *dagger.Secret,
	knownHosts *dagger.Secret,
	// Remote directory relative to the SSH user's home.
	deployDirectory string,
	// Required deployment CLI: docker or podman. Supplied by the workflow preset.
	deployContainerRuntime string,
	// +default="compose.yml"
	composeFile string,
	// Container image used for the SSH client.
	// +default="alpine:3.24.1"
	sshImage string,
	registryUsername string,
	registryPassword *dagger.Secret,
) error {
	if deployContainerRuntime != "docker" && deployContainerRuntime != "podman" {
		return fmt.Errorf("deploy-container-runtime must be docker or podman, got %q", deployContainerRuntime)
	}
	image := registry + "/" + appName + ":" + tag
	return m.deployComposeImage(ctx, sshTarget, deployDirectory, image, deployContainerRuntime, composeFile, sshImage, registry, registryUsername, sshKey, knownHosts, registryPassword)
}

func namedBuildSecret(ctx context.Context, name string, secret *dagger.Secret) (*dagger.Secret, error) {
	plaintext, err := secret.Plaintext(ctx)
	if err != nil {
		return nil, fmt.Errorf("read build secret %q: %w", name, err)
	}
	return dag.SetSecret(name, plaintext), nil
}

func buildArguments(ctx context.Context, file *dagger.File, explicit *dagger.EnvFile) ([]dagger.BuildArg, error) {
	var sources []*dagger.EnvFile
	if file != nil {
		sources = append(sources, file.AsEnvFile())
	}
	if explicit != nil {
		sources = append(sources, explicit)
	}
	values := map[string]string{}
	for _, source := range sources {
		variables, err := source.Variables(ctx)
		if err != nil {
			return nil, fmt.Errorf("read public build values: %w", err)
		}
		for _, variable := range variables {
			name, err := variable.Name(ctx)
			if err != nil {
				return nil, err
			}
			value, err := variable.Value(ctx)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
	}
	// Stable ordering preserves cache identity independently of insertion order.
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	args := make([]dagger.BuildArg, 0, len(names))
	for _, name := range names {
		args = append(args, dagger.BuildArg{Name: name, Value: values[name]})
	}
	return args, nil
}

type deployComposeParams struct {
	ctx                                                                             context.Context
	sshTarget                                                                       string
	deployDirectory, composeRuntime, composeFile, registry, registryUsername, image string
	sshKey, knownHosts, registryPassword                                            *dagger.Secret
}

func (m *Daggerer) deployComposeImage(p deployComposeParams) error {
	remoteDirectory := `"$HOME"/` + shellQuote(p.deployDirectory)
	// FIXME: we need to use the .env for this? what about the key value secrets etc, etc??
	// APP_IMAGE is not forced behavior, we just set it bc we have that key_value pair set in the release env variables...
	runCompose := fmt.Sprintf(
		"cd %s && APP_IMAGE=%s %s",
		p.remoteDirectory,
		shellQuote(p.image),
		shellCommand(p.composeRuntime, "compose", "-f", p.composeFile, "up", "-d", "--force-recreate", "--remove-orphans"),
	)

	sshClient, err := dag.Container().From("alpine:3.24.1").
		WithExec([]string{"apk", "add", "--no-cache", "openssh-client"}).
		Sync(p.ctx)
	if err != nil {
		return fmt.Errorf("sshClient could not be created %w", err)
	}

	// SSH must connect again: cached success cannot revalidate authorization.
	sshClient.WithEnvVariable("DAGGERER_EXEC_NONCE", time.Now().String()).
		WithSecretVariable("SSH_KEY", p.sshKey).
		WithSecretVariable("KNOWN_HOSTS", p.knownHosts).
		WithSecretVariable("REGISTRY_PASSWORD", p.registryPassword)

	if err := runSSH(p.ctx, p.sshClient, sshTarget, sshKeyMountPath, knownHostsMountPath,
		fmt.Sprintf("cd %s && test -f %s", remoteDirectory, shellQuote(composeFile))); err != nil {
		return fmt.Errorf("compose file unavailable: %w", err)
	}
	if err := runSSH(p.ctx, p.sshClient, sshTarget, sshKeyMountPath, knownHostsMountPath,
		shellCommand(composeRuntime, "login", registry, "-u", registryUsername, "--password-stdin"),
		dagger.ContainerWithExecOpts{RedirectStdin: registryPasswordMountPath}); err != nil {
		return fmt.Errorf("registry unavailable or authentication failed: %w", err)
	}

	if err := runSSH(p.ctx, p.sshClient, sshTarget, sshKeyMountPath, knownHostsMountPath, pullImage); err != nil {
		return fmt.Errorf("image unavailable: %s: %w", image, err)
	}

	if err := runSSH(p.ctx, p.sshClient, sshTarget, sshKeyMountPath, knownHostsMountPath,
		shellCommand(composeRuntime, "pull", image)); err != nil {
		return fmt.Errorf("compose deployment failed: %w", err)
	}
	return nil
}

func runSSH(ctx context.Context, client *dagger.Container, target, sshKeyMountPath, knownHostsMountPath, command string, opts ...dagger.ContainerWithExecOpts) error {
	_, err := client.WithExec(sshExec(target, sshKeyMountPath, knownHostsMountPath, command), opts...).Sync(ctx)
	return err
}

func (m *Daggerer) checkRegistryAccess(
	ctx context.Context,
	registry, username string,
	password *dagger.Secret,
	registryPasswordMountPath string,
) error {
	// Internal authentication helper; independent of the deployment runtime.
	_, err := dag.Container().From("docker:27.5.1-cli").
		WithEnvVariable("DAGGERER_EXEC_NONCE", rand.Text()).
		WithMountedSecret(registryPasswordMountPath, password).
		WithExec(
			[]string{"docker", "login", registry, "-u", username, "--password-stdin"},
			dagger.ContainerWithExecOpts{RedirectStdin: registryPasswordMountPath},
		).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("registry unavailable or authentication failed: %s: %w", registry, err)
	}
	return nil
}

func sshExec(target, sshKeyMountPath, knownHostsMountPath, command string) []string {
	return []string{
		"ssh",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHostsMountPath,
		"-i", sshKeyMountPath,
		sshTarget,
		command,
	}
}

func shellCommand(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
