package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"dagger/daggerer/internal/dagger"
)

type Daggerer struct {
	// +private
	BuildSecretIDs []string
	// +private
	BuildSecretValues []*dagger.Secret
}

const (
	sshKeyMountPath           = "/run/secrets/ssh_key"
	knownHostsMountPath       = "/run/secrets/known_hosts"
	registryPasswordMountPath = "/run/secrets/registry_password"
)

// Build a container image from a Dockerfile without publishing it.
func (m *Daggerer) BuildDockerfile(
	ctx context.Context,
	// Application checkout to use as the build context.
	source *dagger.Directory,
	// Public .env file, parsed by Dagger. Never supply credentials here.
	// +optional
	buildEnvFile *dagger.File,
	// Public dotenv text, parsed by Dagger; overrides buildEnvFile values. Never supply credentials here.
	// +optional
	buildValues string,
	// Dockerfile path relative to the build context.
	// +default="Dockerfile"
	dockerfile string,
) (*dagger.Container, error) {
	opts := dagger.DirectoryDockerBuildOpts{
		Dockerfile: dockerfile,
	}
	_, args, err := publicEnvironment(ctx, buildEnvFile, buildValues)
	if err != nil {
		return nil, err
	}
	opts.BuildArgs = args
	secrets, err := m.dockerBuildSecrets(ctx, args)
	if err != nil {
		return nil, err
	}
	opts.Secrets = secrets
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
) (string, error) {
	container, err := m.BuildDockerfile(ctx, source, buildEnvFile, buildValues, dockerfile)
	if err != nil {
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
	// SSH destination port.
	// +default=22
	sshTargetPort int,
	sshKey *dagger.Secret,
	knownHosts *dagger.Secret,
	// Remote directory relative to the SSH user's home
	deployDirectory string,
	// Public dotenv file on the caller, forwarded to the remote `compose` process. Never supply credentials here.
	// +optional
	deployEnvFile *dagger.File,
	// Public dotenv text; overrides deployEnvFile values. No application variable names are implied.
	// +optional
	deployValues string,
	// Private NAME=literal-value dotenv base file Secret; must use file://.
	// +optional
	deploySecretEnvFile *dagger.Secret,
	// `compose` file name that is in the deploy directory.
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
	// +default="Dockerfile"
	dockerfile string,
) error {
	if err := validateDeployRuntime(deployContainerRuntime); err != nil {
		return err
	}
	deployEnv, err := deploymentEnv(ctx, deployEnvFile, deployValues, deploySecretEnvFile)
	if err != nil {
		return err
	}
	_, err = m.Build(ctx, source, registry, appName, tag, registryUsername, registryPassword, dockerfile, buildEnvFile, buildValues)
	if err != nil {
		return err
	}
	return m.deployComposeImage(deployComposeParams{
		ctx: ctx, sshTarget: sshTarget, sshTargetPort: sshTargetPort, deployDirectory: deployDirectory,
		image: registry + "/" + appName + ":" + tag, composeRuntime: deployContainerRuntime, composeFile: composeFile,
		registry: registryHost(registry), registryUsername: registryUsername,
		sshKey: sshKey, knownHosts: knownHosts, registryPassword: registryPassword,
		deployEnv: deployEnv, deploySecretEnvFile: deploySecretEnvFile,
	})
}

// Deploy an existing image with `compose`.
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
	// SSH destination port.
	// +default=22
	sshTargetPort int,
	sshKey *dagger.Secret,
	knownHosts *dagger.Secret,
	// Remote directory relative to the SSH user's home.
	deployDirectory string,
	// Public dotenv file on the caller, forwarded to the remote `compose` process. Never supply credentials here.
	// +optional
	deployEnvFile *dagger.File,
	// Public dotenv text; overrides deployEnvFile values. No application variable names are implied.
	// +optional
	deployValues string,
	// Private NAME=literal-value dotenv base file Secret; must use file://.
	// +optional
	deploySecretEnvFile *dagger.Secret,
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
	deployEnv, err := deploymentEnv(ctx, deployEnvFile, deployValues, deploySecretEnvFile)
	if err != nil {
		return err
	}
	return m.deployComposeImage(deployComposeParams{
		ctx: ctx, sshTarget: sshTarget, sshTargetPort: sshTargetPort, deployDirectory: deployDirectory,
		image: image, composeRuntime: deployContainerRuntime, composeFile: composeFile,
		registry: registryHost(registry), registryUsername: registryUsername,
		sshKey: sshKey, knownHosts: knownHosts, registryPassword: registryPassword,
		deployEnv: deployEnv, deploySecretEnvFile: deploySecretEnvFile,
	})
}

func deploymentEnv(ctx context.Context, publicFile *dagger.File, publicValues string, privateFile *dagger.Secret) ([]dagger.BuildArg, error) {
	_, public, err := publicEnvironment(ctx, publicFile, publicValues)
	if err != nil {
		return nil, err
	}
	if err := validateDeploymentSecrets(ctx, public, privateFile); err != nil {
		return nil, err
	}
	return public, nil
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

func publicEnvironment(ctx context.Context, file *dagger.File, explicit string) (*dagger.EnvFile, []dagger.BuildArg, error) {
	var sources []*dagger.EnvFile
	if file != nil {
		sources = append(sources, file.AsEnvFile())
	}
	if explicit != "" {
		sources = append(sources, dag.Directory().WithNewFile("values.env", explicit).File("values.env").AsEnvFile())
	}
	merged, err := mergeEnvFiles(ctx, sources...)
	if err != nil {
		return nil, nil, fmt.Errorf("read public environment values: %w", err)
	}
	values, err := envFileArguments(ctx, merged)
	if err != nil {
		return nil, nil, fmt.Errorf("read public environment values: %w", err)
	}
	return merged, values, nil
}

func mergeEnvFiles(ctx context.Context, sources ...*dagger.EnvFile) (*dagger.EnvFile, error) {
	merged := dag.EnvFile()
	for _, source := range sources {
		variables, err := source.Variables(ctx)
		if err != nil {
			return nil, err
		}
		for i := range variables {
			name, err := variables[i].Name(ctx)
			if err != nil {
				return nil, err
			}
			value, err := variables[i].Value(ctx)
			if err != nil {
				return nil, err
			}
			merged = merged.WithoutVariable(name).WithVariable(name, value)
		}
	}
	return merged, nil
}

func envFileArguments(ctx context.Context, env *dagger.EnvFile) ([]dagger.BuildArg, error) {
	variables, err := env.Variables(ctx)
	if err != nil {
		return nil, err
	}
	values := make([]dagger.BuildArg, 0, len(variables))
	for i := range variables {
		name, err := variables[i].Name(ctx)
		if err != nil {
			return nil, err
		}
		if !shellVariableName.MatchString(name) {
			return nil, fmt.Errorf("public variable %q is not a valid shell variable name", name)
		}
		value, err := variables[i].Value(ctx)
		if err != nil {
			return nil, err
		}
		values = append(values, dagger.BuildArg{Name: name, Value: value})
	}
	return values, nil
}

type deployComposeParams struct {
	ctx                                                                             context.Context
	sshTarget                                                                       string
	sshTargetPort                                                                   int
	deployDirectory, composeRuntime, composeFile, registry, registryUsername, image string
	sshKey, knownHosts, registryPassword, deploySecretEnvFile                       *dagger.Secret
	deployEnv                                                                       []dagger.BuildArg
}

func (m *Daggerer) deployComposeImage(p deployComposeParams) error {
	remoteDirectory := `"$HOME"/` + shellQuote(p.deployDirectory)
	// Forward only caller-supplied values; the application's `compose` file defines their meaning.
	publicEnv := []string{"env", "--"}
	for _, variable := range p.deployEnv {
		publicEnv = append(publicEnv, variable.Name+"="+variable.Value)
	}
	compose := []string{p.composeRuntime, "compose", "-f", p.composeFile, "up", "-d", "--force-recreate", "--remove-orphans"}
	var command string
	if p.deploySecretEnvFile != nil {
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

	if err := runSSH(p.ctx, sshClient, p.sshTarget, p.sshTargetPort,
		fmt.Sprintf("cd %s && test -f %s", remoteDirectory, shellQuote(p.composeFile))); err != nil {
		return fmt.Errorf("compose file unavailable: %w", err)
	}
	if err := runSSH(p.ctx, sshClient, p.sshTarget, p.sshTargetPort,
		shellCommand(p.composeRuntime, "login", p.registry, "-u", p.registryUsername, "--password-stdin"),
		dagger.ContainerWithExecOpts{RedirectStdin: registryPasswordMountPath}); err != nil {
		return fmt.Errorf("registry unavailable or authentication failed: %w", err)
	}

	if err := runSSH(p.ctx, sshClient, p.sshTarget, p.sshTargetPort, shellCommand(p.composeRuntime, "pull", p.image)); err != nil {
		return fmt.Errorf("image unavailable: %s: %w", p.image, err)
	}

	if err := runSSHWithDeploymentSecrets(p.ctx, sshClient, p.sshTarget, p.sshTargetPort, runCompose, p.deploySecretEnvFile); err != nil {
		return fmt.Errorf("compose deployment failed: %w", err)
	}
	return nil
}

func runSSH(ctx context.Context, client *dagger.Container, target string, port int, command string, opts ...dagger.ContainerWithExecOpts) error {
	_, err := client.WithExec(sshExec(target, port, command), opts...).Sync(ctx)
	return err
}

func sshExec(target string, port int, command string) []string {
	return []string{
		"ssh",
		"-p", strconv.Itoa(port),
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
