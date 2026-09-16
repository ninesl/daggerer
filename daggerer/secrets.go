package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"

	"dagger/daggerer/internal/dagger"
)

const (
	privateEnvPath = "/run/secrets/.env"
)

var shellVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var buildSecretID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// WithBuildSecret adds a named BuildKit secret to the next chained build, build-only, or release call.
func (m *Daggerer) WithBuildSecret(
	ctx context.Context,
	// Dockerfile secret ID used by RUN --mount=type=secret,id=<id>.
	id string,
	// Caller-local secret file. Must use file://.
	secret *dagger.Secret,
) (*Daggerer, error) {
	if !buildSecretID.MatchString(id) {
		return nil, fmt.Errorf("build secret ID %q must match %s", id, buildSecretID)
	}
	if secret == nil {
		return nil, fmt.Errorf("build secret %q is required", id)
	}
	if err := requireFileSecret(ctx, "build secret "+id, secret); err != nil {
		return nil, err
	}
	for _, existing := range m.BuildSecretIDs {
		if existing == id {
			return nil, fmt.Errorf("build secret ID %q is already configured", id)
		}
	}

	next := &Daggerer{
		BuildSecretIDs:    append([]string(nil), m.BuildSecretIDs...),
		BuildSecretValues: append([]*dagger.Secret(nil), m.BuildSecretValues...),
	}
	next.BuildSecretIDs = append(next.BuildSecretIDs, id)
	next.BuildSecretValues = append(next.BuildSecretValues, secret)
	return next, nil
}

func (m *Daggerer) dockerBuildSecrets(ctx context.Context) ([]*dagger.Secret, error) {
	if len(m.BuildSecretIDs) != len(m.BuildSecretValues) {
		return nil, fmt.Errorf("invalid build secret configuration")
	}
	secrets := make([]*dagger.Secret, len(m.BuildSecretIDs))
	for i, id := range m.BuildSecretIDs {
		// WARN: This follows Dagger's documented workaround for using secrets in
		// Dockerfile builds and may be deprecated once DockerBuild accepts an
		// {id, secret} pair. Plaintext exists briefly in module memory and must
		// never be logged, parsed, written to a file, or included in errors.
		plaintext, err := m.BuildSecretValues[i].Plaintext(ctx)
		if err != nil {
			return nil, fmt.Errorf("read build secret %q: %w", id, err)
		}
		secrets[i] = dag.SetSecret(id, plaintext)
	}
	return secrets, nil
}

func validateDeploymentSecrets(ctx context.Context, public []dagger.BuildArg, secret *dagger.Secret) error {
	return validateSecretFiles(ctx, "deployment", public, []secretFile{
		{label: "deploy-secret-env-file", path: privateEnvPath, secret: secret},
	})
}

type secretFile struct {
	label  string
	path   string
	secret *dagger.Secret
}

func validateSecretFiles(ctx context.Context, scope string, public []dagger.BuildArg, inputs []secretFile) error {
	container := dag.Container().From("alpine:3.24.1")
	var sources []string
	for _, input := range inputs {
		if input.secret == nil {
			continue
		}
		if err := requireFileSecret(ctx, input.label, input.secret); err != nil {
			return err
		}
		container = container.WithMountedSecret(input.path, input.secret)
		sources = append(sources, input.path)
	}
	if len(sources) == 0 {
		return nil
	}

	for _, variable := range public {
		if !shellVariableName.MatchString(variable.Name) {
			return fmt.Errorf("public variable %q is not a valid shell variable name", variable.Name)
		}
	}
	args := []string{"/bin/busybox", "awk", "-v", fmt.Sprintf("file_count=%d", len(sources)), "-v", "scope=" + scope, validationAwk}
	args = append(args, sources...)
	for _, variable := range public {
		args = append(args, variable.Name)
	}
	if _, err := container.WithEnvVariable("DAGGERER_SECRET_VALIDATION_NONCE", rand.Text()).WithExec(args).Sync(ctx); err != nil {
		return fmt.Errorf("validate %s secrets: %w", scope, err)
	}
	return nil
}

func runSSHWithDeploymentSecrets(ctx context.Context, client *dagger.Container, target, command string, secret *dagger.Secret) error {
	var sources []string
	if secret != nil {
		client = client.WithMountedSecret(privateEnvPath, secret)
		sources = append(sources, privateEnvPath)
	}
	if len(sources) == 0 {
		return runSSH(ctx, client, target, command)
	}

	// exportCommand writes a shell script to stdout; the pipeline's "$@" is the
	// SSH process, so that script becomes stdin for the remote `sh -s` command.
	args := append([]string{"env", "-i", "sh", "-c", exportCommand(sources), "sh"}, sshExec(target, command)...)
	_, err := client.WithExec(args).Sync(ctx)
	return err
}

func requireFileSecret(ctx context.Context, label string, secret *dagger.Secret) error {
	uri, err := secret.URI(ctx)
	if err != nil {
		return fmt.Errorf("read %s source: %w", label, err)
	}
	if !strings.HasPrefix(uri, "file://") || strings.TrimPrefix(uri, "file://") == "" {
		return fmt.Errorf("%s must use file:// with the actual dotenv filename", label)
	}
	return nil
}

func exportCommand(sources []string) string {
	quotedSources := make([]string, len(sources))
	for i, source := range sources {
		quotedSources[i] = shellQuote(source)
	}
	return "/bin/busybox awk " + shellQuote(exportAwk) + " " + strings.Join(quotedSources, " ") + ` | "$@"`
}

const validationAwk = `
BEGIN {
  for (i = file_count + 1; i < ARGC; i++) {
    public[ARGV[i]] = 1
    delete ARGV[i]
  }
}
/^[[:space:]]*($|#)/ { next }
/^[A-Za-z_][A-Za-z0-9_]*=/ {
  name = substr($0, 1, index($0, "=") - 1)
  if (name in public) {
    printf "%s secret collision for \"%s\": a variable cannot exist in both public and private inputs\n", scope, name > "/dev/stderr"
    exit 1
  }
  next
}
{
  print "invalid private dotenv input: expected NAME=literal value" > "/dev/stderr"
  exit 1
}`

const exportAwk = `
function quote(value) {
  gsub(/\047/, "\047\"\047\"\047", value)
  return "\047" value "\047"
}
/^[[:space:]]*($|#)/ { next }
/^[A-Za-z_][A-Za-z0-9_]*=/ {
  separator = index($0, "=")
  name = substr($0, 1, separator - 1)
  value = substr($0, separator + 1)
  printf "export %s=%s\n", name, quote(value)
  next
}
{
  print "invalid private dotenv input" > "/dev/stderr"
  failed = 1
  exit 1
}
END {
  if (!failed) print "exec \"$@\""
}`
