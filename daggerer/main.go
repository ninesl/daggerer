package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"dagger/daggerer/internal/dagger"
	"dagger/daggerer/internal/envmerge"
)

type Daggerer struct{}

const (
	sshKeyMountPath           = "/run/secrets/ssh_key"
	knownHostsMountPath       = "/run/secrets/known_hosts"
	registryPasswordMountPath = "/run/secrets/registry_password"
)

// Build a container without publishing it.
func (m *Daggerer) BuildOnly(
	ctx context.Context,
	// Application checkout to use as the build context.
	source *dagger.Directory,
	// Public .env file, parsed by Dagger. Never supply credentials here.
	// +optional
	buildEnvFile *dagger.File,
	// Public dotenv text, parsed by Dagger; overrides buildEnvFile values. Never supply credentials here.
	// +optional
	buildValues string,
	// Private dotenv Secret parsed and merged into the BuildKit secret build_env.
	// +optional
	buildSecretEnvFile *dagger.Secret,
	// Private dotenv Secret text; overrides buildSecretEnvFile values.
	// +optional
	buildSecretValues *dagger.Secret,
	// Dockerfile path relative to the build context.
	// +default="Dockerfile"
	dockerfile string,
) (*dagger.Container, error) {
	opts := dagger.DirectoryDockerBuildOpts{
		Dockerfile: dockerfile,
	}
	args, err := publicEnvArguments(ctx, buildEnvFile, buildValues)
	if err != nil {
		return nil, err
	}
	opts.BuildArgs = args
	secretValues, err := secretEnvArguments(ctx, buildSecretEnvFile, buildSecretValues)
	if err != nil {
		return nil, err
	}
	if err := rejectSecretCollisions("build", args, secretValues); err != nil {
		return nil, err
	}
	if len(secretValues) > 0 {
		contents, err := shellDotenv(secretValues)
		if err != nil {
			return nil, err
		}
		secret := dag.SetSecret("build_env", contents)
		opts.Secrets = []*dagger.Secret{secret}
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
	// Public dotenv text, parsed by Dagger; overrides buildEnvFile values. Never supply credentials here.
	// +optional
	buildValues string,
	// Private dotenv Secret parsed and merged into the BuildKit secret build_env.
	// +optional
	buildSecretEnvFile *dagger.Secret,
	// Private dotenv Secret text; overrides buildSecretEnvFile values.
	// +optional
	buildSecretValues *dagger.Secret,
) (string, error) {
	container, err := m.BuildOnly(ctx, source, buildEnvFile, buildValues, buildSecretEnvFile, buildSecretValues, dockerfile)
	if err != nil {
		return "", err
	}
	if err := m.checkRegistryAccess(ctx, registryHost(registry), registryUsername, registryPassword); err != nil {
		return "", err
	}
	image := registry + "/" + appName + ":" + tag
	published, err := container.
		WithRegistryAuth(registryHost(registry), registryUsername, registryPassword).
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
	// OCI image registry host name.
	registry string,
	// OCI image repository name inside the registry.
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
	// Public dotenv file on the caller, forwarded to the remote Compose process. Never supply credentials here.
	// +optional
	deployEnvFile *dagger.File,
	// Public dotenv text; overrides deployEnvFile values. No application variable names are implied.
	// +optional
	deployValues string,
	// Private dotenv Secret base forwarded only to the remote Compose process.
	// +optional
	deploySecretEnvFile *dagger.Secret,
	// Private dotenv text; overrides deploySecretEnvFile values.
	// +optional
	deploySecretValues *dagger.Secret,
	// Compose file name that is in the deploy directory.
	// +default="compose.yml"
	composeFile string,
	// Required deployment CLI: docker or podman.
	deployContainerRuntime string,
	registryUsername string,
	registryPassword *dagger.Secret,
	// Public .env file, parsed by Dagger. You really shouldn't supply credentials here.
	// +optional
	buildEnvFile *dagger.File,
	// Public dotenv text, parsed by Dagger; overrides buildEnvFile values. Never supply credentials here.
	// +optional
	buildValues string,
	// Private dotenv Secret parsed and merged into the BuildKit secret build_env.
	// +optional
	buildSecretEnvFile *dagger.Secret,
	// Private dotenv Secret text; overrides buildSecretEnvFile values.
	// +optional
	buildSecretValues *dagger.Secret,
	// +default="Dockerfile"
	dockerfile string,
) error {
	if err := validateDeployRuntime(deployContainerRuntime); err != nil {
		return err
	}
	if _, _, err := deploymentEnv(ctx, deployEnvFile, deployValues, deploySecretEnvFile, deploySecretValues); err != nil {
		return err
	}
	_, err := m.Build(ctx, source, registry, appName, tag, registryUsername, registryPassword, dockerfile, buildEnvFile, buildValues, buildSecretEnvFile, buildSecretValues)
	if err != nil {
		return err
	}
	return m.Deploy(ctx, registry, appName, tag, sshTarget, sshKey, knownHosts, deployDirectory, deployEnvFile, deployValues, deploySecretEnvFile, deploySecretValues, deployContainerRuntime, composeFile, registryUsername, registryPassword)
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
	// Public dotenv file on the caller, forwarded to the remote Compose process. Never supply credentials here.
	// +optional
	deployEnvFile *dagger.File,
	// Public dotenv text; overrides deployEnvFile values. No application variable names are implied.
	// +optional
	deployValues string,
	// Private dotenv Secret base forwarded only to the remote Compose process.
	// +optional
	deploySecretEnvFile *dagger.Secret,
	// Private dotenv text; overrides deploySecretEnvFile values.
	// +optional
	deploySecretValues *dagger.Secret,
	// Required deployment CLI: docker or podman. Supplied by the workflow preset.
	deployContainerRuntime string,
	// +default="compose.yml"
	composeFile string,
	registryUsername string,
	registryPassword *dagger.Secret,
) error {
	if err := validateDeployRuntime(deployContainerRuntime); err != nil {
		return err
	}
	image := registry + "/" + appName + ":" + tag
	deployEnv, deploySecrets, err := deploymentEnv(ctx, deployEnvFile, deployValues, deploySecretEnvFile, deploySecretValues)
	if err != nil {
		return err
	}
	return m.deployComposeImage(deployComposeParams{
		ctx: ctx, sshTarget: sshTarget, deployDirectory: deployDirectory,
		image: image, composeRuntime: deployContainerRuntime, composeFile: composeFile,
		registry: registryHost(registry), registryUsername: registryUsername,
		sshKey: sshKey, knownHosts: knownHosts, registryPassword: registryPassword,
		deployEnv:     deployEnv,
		deploySecrets: deploySecrets,
	})
}

func deploymentEnv(ctx context.Context, publicFile *dagger.File, publicValues string, privateFile, privateValues *dagger.Secret) ([]dagger.BuildArg, []dagger.BuildArg, error) {
	public, err := publicEnvArguments(ctx, publicFile, publicValues)
	if err != nil {
		return nil, nil, err
	}
	private, err := secretEnvArguments(ctx, privateFile, privateValues)
	if err != nil {
		return nil, nil, err
	}
	if err := rejectSecretCollisions("deployment", public, private); err != nil {
		return nil, nil, err
	}
	if _, err := deploymentSecretScript(private); err != nil {
		return nil, nil, err
	}
	return public, private, nil
}

func validateDeployRuntime(runtime string) error {
	if runtime != "docker" && runtime != "podman" {
		return fmt.Errorf("deploy-container-runtime must be docker or podman, got %q", runtime)
	}
	return nil
}

// Authentication uses the host; image references retain the namespace.
func registryHost(registry string) string {
	host, _, _ := strings.Cut(registry, "/")
	return host
}

func publicEnvArguments(ctx context.Context, file *dagger.File, explicit string) ([]dagger.BuildArg, error) {
	var sources []*dagger.EnvFile
	if file != nil {
		sources = append(sources, file.AsEnvFile())
	}
	if explicit != "" {
		sources = append(sources, dag.Directory().WithNewFile("values.env", explicit).File("values.env").AsEnvFile())
	}
	var merged []dagger.BuildArg
	for _, source := range sources {
		variables, err := source.Variables(ctx)
		if err != nil {
			return nil, fmt.Errorf("read public environment values: %w", err)
		}
		values := make([]dagger.BuildArg, 0, len(variables))
		for _, variable := range variables {
			name, err := variable.Name(ctx)
			if err != nil {
				return nil, err
			}
			value, err := variable.Value(ctx)
			if err != nil {
				return nil, err
			}
			values = append(values, dagger.BuildArg{Name: name, Value: value})
		}
		merged = mergeEnvironmentValues(merged, values)
	}
	return merged, nil
}

func mergeEnvironmentValues(sources ...[]dagger.BuildArg) []dagger.BuildArg {
	converted := make([][]envmerge.Variable, 0, len(sources))
	for _, source := range sources {
		values := make([]envmerge.Variable, 0, len(source))
		for _, variable := range source {
			values = append(values, envmerge.Variable{Name: variable.Name, Value: variable.Value})
		}
		converted = append(converted, values)
	}
	values := envmerge.Merge(converted...)
	merged := make([]dagger.BuildArg, 0, len(values))
	for _, variable := range values {
		merged = append(merged, dagger.BuildArg{Name: variable.Name, Value: variable.Value})
	}
	return merged
}

func secretEnvArguments(ctx context.Context, file, explicit *dagger.Secret) ([]dagger.BuildArg, error) {
	var merged []dagger.BuildArg
	for _, secret := range []*dagger.Secret{file, explicit} {
		if secret == nil {
			continue
		}
		plaintext, err := secret.Plaintext(ctx)
		if err != nil {
			return nil, fmt.Errorf("read private environment input: %w", err)
		}
		values, err := parseDotenv(plaintext)
		if err != nil {
			// Parser details can contain source text; never include them for private inputs.
			return nil, fmt.Errorf("parse private environment input: invalid dotenv syntax")
		}
		merged = mergeEnvironmentValues(merged, values)
	}
	return merged, nil
}

func parseDotenv(contents string) ([]dagger.BuildArg, error) {
	values, err := envmerge.Parse(contents)
	if err != nil {
		return nil, err
	}
	args := make([]dagger.BuildArg, 0, len(values))
	for _, variable := range values {
		args = append(args, dagger.BuildArg{Name: variable.Name, Value: variable.Value})
	}
	return args, nil
}

func rejectSecretCollisions(scope string, public, private []dagger.BuildArg) error {
	toVariables := func(args []dagger.BuildArg) []envmerge.Variable {
		values := make([]envmerge.Variable, 0, len(args))
		for _, variable := range args {
			values = append(values, envmerge.Variable{Name: variable.Name, Value: variable.Value})
		}
		return values
	}
	if name, exists := envmerge.Collision(toVariables(public), toVariables(private)); exists {
		return fmt.Errorf("%s secret collision for %q: a variable cannot exist in both public and private inputs", scope, name)
	}
	return nil
}

func shellDotenv(values []dagger.BuildArg) (string, error) {
	converted := make([]envmerge.Variable, 0, len(values))
	for _, variable := range values {
		converted = append(converted, envmerge.Variable{Name: variable.Name, Value: variable.Value})
	}
	return envmerge.ShellDotenv(converted)
}

type deployComposeParams struct {
	ctx                                                                             context.Context
	sshTarget                                                                       string
	deployDirectory, composeRuntime, composeFile, registry, registryUsername, image string
	sshKey, knownHosts, registryPassword                                            *dagger.Secret
	deployEnv                                                                       []dagger.BuildArg
	deploySecrets                                                                   []dagger.BuildArg
}

func (m *Daggerer) deployComposeImage(p deployComposeParams) error {
	remoteDirectory := `"$HOME"/` + shellQuote(p.deployDirectory)
	// Forward only caller-supplied values; the application's Compose file defines their meaning.
	publicEnv := []string{"env", "--"}
	for _, variable := range p.deployEnv {
		publicEnv = append(publicEnv, variable.Name+"="+variable.Value)
	}
	compose := []string{p.composeRuntime, "compose", "-f", p.composeFile, "up", "-d", "--force-recreate", "--remove-orphans"}
	var command string
	if len(p.deploySecrets) > 0 {
		// The private export script arrives on stdin; only non-secret Compose arguments
		// are visible in the remote process command line.
		command = shellCommand(append(publicEnv, append([]string{"sh", "-s", "--"}, compose...)...)...)
	} else {
		command = shellCommand(append(publicEnv, compose...)...)
	}
	runCompose := fmt.Sprintf(
		"cd %s && %s",
		remoteDirectory,
		command,
	)

	sshClient, err := dag.Container().From("alpine:3.24.1").
		WithExec([]string{"apk", "add", "--no-cache", "openssh-client"}).
		Sync(p.ctx)
	if err != nil {
		return fmt.Errorf("sshClient could not be created %w", err)
	}

	// SSH must connect again: cached success cannot revalidate authorization.
	sshClient = sshClient.WithEnvVariable("DAGGERER_EXEC_NONCE", rand.Text()).
		WithMountedSecret(sshKeyMountPath, p.sshKey, dagger.ContainerWithMountedSecretOpts{Mode: 0600}).
		WithMountedSecret(knownHostsMountPath, p.knownHosts).
		WithMountedSecret(registryPasswordMountPath, p.registryPassword)
	var deploySecretPath string
	if len(p.deploySecrets) > 0 {
		deploySecretPath = "/run/secrets/deploy_env"
		script, err := deploymentSecretScript(p.deploySecrets)
		if err != nil {
			return err
		}
		sshClient = sshClient.WithMountedSecret(deploySecretPath, dag.SetSecret("deploy_env", script))
	}

	if err := runSSH(p.ctx, sshClient, p.sshTarget,
		fmt.Sprintf("cd %s && test -f %s", remoteDirectory, shellQuote(p.composeFile))); err != nil {
		return fmt.Errorf("compose file unavailable: %w", err)
	}
	if err := runSSH(p.ctx, sshClient, p.sshTarget,
		shellCommand(p.composeRuntime, "login", p.registry, "-u", p.registryUsername, "--password-stdin"),
		dagger.ContainerWithExecOpts{RedirectStdin: registryPasswordMountPath}); err != nil {
		return fmt.Errorf("registry unavailable or authentication failed: %w", err)
	}

	if err := runSSH(p.ctx, sshClient, p.sshTarget, shellCommand(p.composeRuntime, "pull", p.image)); err != nil {
		return fmt.Errorf("image unavailable: %s: %w", p.image, err)
	}

	composeOpts := []dagger.ContainerWithExecOpts(nil)
	if deploySecretPath != "" {
		composeOpts = append(composeOpts, dagger.ContainerWithExecOpts{RedirectStdin: deploySecretPath})
	}
	if err := runSSH(p.ctx, sshClient, p.sshTarget, runCompose, composeOpts...); err != nil {
		return fmt.Errorf("compose deployment failed: %w", err)
	}
	return nil
}

func deploymentSecretScript(values []dagger.BuildArg) (string, error) {
	converted := make([]envmerge.Variable, 0, len(values))
	for _, variable := range values {
		converted = append(converted, envmerge.Variable{Name: variable.Name, Value: variable.Value})
	}
	contents, err := envmerge.ShellExports(converted)
	if err != nil {
		return "", err
	}
	return contents + "exec \"$@\"\n", nil
}

func runSSH(ctx context.Context, client *dagger.Container, target, command string, opts ...dagger.ContainerWithExecOpts) error {
	_, err := client.WithExec(sshExec(target, command), opts...).Sync(ctx)
	return err
}

func (m *Daggerer) checkRegistryAccess(
	ctx context.Context,
	registry, username string,
	password *dagger.Secret,
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

func sshExec(target, command string) []string {
	return []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHostsMountPath,
		"-i", sshKeyMountPath,
		target,
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
