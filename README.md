# Daggerer

Build, publish, and deploy an application from a self-hosted GitHub Actions runner. Daggerer builds the application's Dockerfile with Dagger, publishes the image, then deploys it over SSH with Docker Compose or Podman Compose.

## Prerequisites

You need a [self-hosted GitHub Actions runner](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/add-runners) and a deployment host reachable from it over SSH. They can be the same machine. The runner executes the workflow and Dagger CLI; the deployment host pulls the image and runs Compose.

Install the [Dagger CLI](https://docs.dagger.io/getting-started/install) for the Linux user that owns the runner service. Daggerer was built with Dagger `v1.0.0-beta.13`; run `dagger version` as that user to verify CLI and Engine access.

The deployment host needs Docker with the Compose plugin, or Podman with a compatible Compose provider. The Quick Start uses Docker. Its SSH user must already have a deployment directory containing `compose.yml`, and must be able to log in to the registry and pull the application image.

The runner needs a registry credential with push access, plus a private SSH key and a verified `known_hosts` file for the deployment host. Keep those files readable only by the runner service user. The Quick Start assumes they live below `$HOME/actions/secrets`.

If you still need to install the runner service, run these commands from its directory:

```bash
sudo ./svc.sh install "$USER"
sudo ./svc.sh start
sudo ./svc.sh status
```

## Mental Model

Use `release` for the normal path: it builds the checked-out source, publishes the image, and deploys it. `deploy` skips the build and deploys an image that already exists in the registry. `build` publishes without deploying, while `build-only` returns the built [`*dagger.Container`][dagger-container] for another Dagger pipeline to consume.

The examples use Dagger's [`-W`/`--workspace` option][dagger-workspace] to load `github.com/ninesl/daggerer@master` as the workspace for that command. The application repository does not need to install Daggerer or maintain its own `dagger.toml`.

Every direct call follows the same shape:

```text
dagger -W github.com/ninesl/daggerer@master api call <operation> <arguments>
```

The runner and deployment host may be one VPS or separate machines. Deployment always goes through the supplied SSH target, which must be reachable from the Dagger Engine's container network.

## Quick Start

The basic release uses Daggerer's defaults: the `Dockerfile` in the application root, the image tag `latest`, and `compose.yml` on the deployment host. Docker is selected explicitly because `--deploy-container-runtime` is required.

Create `$HOME/prod/<app-name>/compose.yml` for the SSH user on the deployment host:

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
```

This is the recommended Compose pattern for this pipeline. Daggerer runs the remote Compose command with `APP_IMAGE=registry/app:latest`, and [Compose interpolation][compose-interpolation] places that value in `image`. `APP_IMAGE` is not a general requirement of Daggerer or Compose; it is the key-value pair this deployment implementation provides so the Compose project can use the selected image.

Add this workflow to the application repository. Replace `<app-name>` and configure the referenced GitHub secrets. Each secret ending in `_FILE` contains a filename below `$HOME/actions/secrets`, not the credential itself.

```yaml
name: Deploy

on:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  deploy:
    runs-on: [self-hosted, linux, x64]
    env:
      APP_NAME: <app-name>
      REGISTRY_URL: ${{ secrets.REGISTRY_URL }}
      REGISTRY_USERNAME: ${{ secrets.REGISTRY_USERNAME }}
      REGISTRY_PASSWORD_FILE: ${{ secrets.REGISTRY_PASSWORD_FILE }}
      SSH_TARGET: ${{ secrets.SSH_TARGET }}
      SSH_KEY_FILE: ${{ secrets.SSH_KEY_FILE }}
      KNOWN_HOSTS_FILE: ${{ secrets.KNOWN_HOSTS_FILE }}

    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release
        shell: bash
        run: |
          set -euo pipefail
          secrets_path="$HOME/actions/secrets"

          dagger -W github.com/ninesl/daggerer@master api call release \
            --source=. \
            --registry="$REGISTRY_URL" \
            --app-name="$APP_NAME" \
            --ssh-target="$SSH_TARGET" \
            --ssh-key="file://${secrets_path}/${SSH_KEY_FILE}" \
            --known-hosts="file://${secrets_path}/${KNOWN_HOSTS_FILE}" \
            --deploy-directory="prod/$APP_NAME" \
            --deploy-container-runtime=docker \
            --registry-username="$REGISTRY_USERNAME" \
            --registry-password="file://${secrets_path}/${REGISTRY_PASSWORD_FILE}"
```

With `REGISTRY_URL=registry.example.com/team` and `APP_NAME=my-app`, this publishes and deploys `registry.example.com/team/my-app:latest`. Publishing replaces the registry's current `latest` tag, so the registry must allow tag updates. Use `--tag="$GITHUB_SHA"` when you want a commit-specific image instead.

The `file://` values are resolved on the runner by the Dagger CLI and passed to Daggerer as [`*dagger.Secret`][dagger-secret] inputs. `SSH_TARGET` has the form `<user>@<host>`. The corresponding `known_hosts` file must contain a verified key for that host, and the SSH public key must be authorized for that user.

## How Release Works

`release` first checks registry authentication, then calls `build-only`. `build-only` runs the application's Dockerfile through [`Directory.DockerBuild`][dagger-build]. `build` attaches the supplied registry credentials with [`WithRegistryAuth`][dagger-auth] and calls [`Publish`][dagger-publish]. If login or publishing fails, deployment never starts.

After publishing, `deploy` opens fresh SSH connections to the selected host. It checks that the Compose file exists, logs in to the registry with the selected deployment CLI, pulls the exact `registry/app:tag`, and runs:

```text
APP_IMAGE=registry/app:tag docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

For the Quick Start values, the complete remote command has this shape:

```bash
cd "$HOME/prod/my-app" && \
  APP_IMAGE=registry.example.com/team/my-app:latest \
  docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

The command runs from `--deploy-directory`, which is relative to the SSH user's home. Daggerer does not copy or rewrite the Compose project. In the recommended pattern above, the project's `image` expression reads `APP_IMAGE`; consumers can organize the rest of their Compose configuration however they need.

Daggerer performs these remote commands from a temporary Alpine SSH client. The SSH key, `known_hosts`, and registry password are attached with [`WithMountedSecret`][dagger-mount-secret], and each command runs through [`WithExec`][dagger-exec]. The temporary container is discarded after the call. A successful registry login may persist credentials in the deployment user's Docker or Podman authentication storage.

```text
self-hosted runner
    -> Dagger Engine
        -> temporary SSH client container
            -> SSH deployment host
                -> Docker or Podman Compose
```

Each deployment supplies the SSH connection in the same form:

```text
--ssh-target=<user>@<host>
--ssh-key=<private-key-secret>
--known-hosts=<verified-known-hosts-secret>
```

Build work for unchanged inputs can reuse Dagger's cache. Registry authentication, publication, SSH authentication, image pull, and Compose deployment execute for each release. This lets an unchanged image build quickly without turning deployment into a cached result.

The remote workspace selected by `-W` has its own Dagger-managed source and cache behavior on the self-hosted runner. When the Git reference resolves to the same commit, Dagger can reuse the fetched source and eligible cached work. When it resolves to a different commit, Dagger retrieves and evaluates the newer source. This is Dagger behavior, not logic implemented by Daggerer.

## Runtime Configuration

Daggerer selects the image for the documented Compose command. The Compose project remains responsible for the running application's environment and secrets. For example:

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
    env_file: ./runtime.env
    secrets:
      - app_token

secrets:
  app_token:
    file: ./secrets/app_token
```

Provision `runtime.env` and `secrets/app_token` with the Compose project on each deployment host. These values never need to pass through the image build. See the Compose documentation for [`env_file`](https://docs.docker.com/reference/compose-file/services/#env_file) and [secrets](https://docs.docker.com/reference/compose-file/secrets/).

Build inputs are different. Public values become Dockerfile build arguments, and build secrets exist only for Dockerfile instructions that mount them. SSH and registry credentials authenticate the pipeline itself; they are not application configuration.

## Build Inputs

Daggerer is language-independent and builds any Dockerfile supported by [`Directory.DockerBuild`][dagger-build]. The following Go example demonstrates public build values, a private GitHub dependency token, and another named build secret. A simpler application can use an ordinary Dockerfile and omit every build-input flag, as the Quick Start does.

```dockerfile
# syntax=docker/dockerfile:1.7

ARG GO_VERSION
FROM golang:${GO_VERSION}-alpine AS builder

RUN apk add --no-cache ca-certificates git
WORKDIR /app

COPY go.mod go.sum ./
# Replace your-org with the owner of the private dependency in go.mod.
ARG GOPRIVATE=github.com/your-org/*
ENV GOPRIVATE=$GOPRIVATE
RUN --mount=type=secret,id=github_token,required=true \
    set -eu; \
    token="$(cat /run/secrets/github_token)"; \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.https://x-access-token:${token}@github.com/.insteadOf" \
    GIT_CONFIG_VALUE_0="https://github.com/" \
    go mod download

COPY . .
ARG APP_PACKAGE=.
RUN --mount=type=secret,id=BAR,required=true \
    test -s /run/secrets/BAR && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -o /app/server "$APP_PACKAGE"

FROM alpine:3.24.1
ARG FOO=hello
ENV FOO=$FOO
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/server /usr/local/bin/server
ENTRYPOINT ["/usr/local/bin/server"]
```

The GitHub personal access token must belong to an account that can read the private dependency. Prefer a [fine-grained PAT](https://github.com/settings/personal-access-tokens/new) restricted to the required repositories with **Contents: Read-only**, including organization approval when required. Store the raw token in a runner-local file rather than a build argument.

Pass public values as a dotenv file with `--build-env-file`. Dagger parses it with [`AsEnvFile`](https://docs.dagger.io/reference/api/file#asEnvFile), and the Dockerfile consumes matching `ARG` names:

```bash
go_version="$(awk '$1 == "go" { print $2 }' go.mod)"
: "${go_version:?go.mod must contain a go directive}"
build_env_file="$(mktemp)"
trap 'rm -f "$build_env_file"' EXIT
printf 'GO_VERSION=%s\nAPP_PACKAGE=.\nFOO=hello\n' "$go_version" > "$build_env_file"

dagger -W github.com/ninesl/daggerer@master api call build-only \
  --source=. \
  --build-env-file="$build_env_file" \
  --build-secret-ids=github_token,BAR \
  --build-secrets="file://$HOME/actions/secrets/github_token,file://$HOME/actions/secrets/bar" \
  sync
```

`--build-secret-ids` and `--build-secrets` are positional pairs. IDs must be unique and match the Dockerfile mount IDs. The secret values are passed to [`DockerBuild`][dagger-build] and are available only to instructions using `RUN --mount=type=secret,id=...`; they are not copied into the resulting image.

```bash
--build-secret-ids=github_token,BAR \
--build-secrets="file://${secrets_path}/${GITHUB_TOKEN_SECRET},file://${secrets_path}/${BAR_SECRET}"
```

From a Go pipeline, public values can instead use Dagger's native [`EnvFile`](https://docs.dagger.io/reference/api/env-file). Explicit `BuildValues` override matching entries from `BuildEnvFile`:

```go
values := dag.EnvFile().
    WithVariable("GO_VERSION", goVersion).
    WithVariable("APP_PACKAGE", ".").
    WithVariable("FOO", "hello")

image := dag.Daggerer().BuildOnly(source, dagger.DaggererBuildOnlyOpts{
    BuildEnvFile: source.File(".env"),
    BuildValues: values,
    BuildSecretIds: []string{"github_token", "BAR"},
    BuildSecrets: []*dagger.Secret{pat, bar},
})
```

Neither `BuildEnvFile` nor `BuildValues` is secret. A public `.env` file can contain values such as:

```dotenv
FOO=hello
```

If that file is inside the application checkout and the Dockerfile uses `COPY . .`, exclude it from the build context while still passing it separately:

```dockerignore
.env
.git
```

For a private dotenv file, `--build-secret-env` mounts the complete file under the fixed BuildKit secret ID `build_env`. Only source a trusted, shell-compatible file:

```dockerfile
RUN --mount=type=secret,id=build_env,required=true \
    set -eu; \
    . /run/secrets/build_env; \
    test -n "$BAR"
```

Pass it as a Dagger Secret:

```bash
--build-secret-env="file://$HOME/actions/secrets/build.env"
```

Named secrets may be used alongside this file, but `build_env` is reserved whenever `--build-secret-env` is supplied.

## Workflow Variants

The Quick Start deliberately uses one production target, Docker, `compose.yml`, and the mutable `latest` tag. Change only the arguments your environment needs.

For an auditable staging image, add `--tag="$GITHUB_SHA"`. To deploy with Podman, install Podman and a compatible Compose provider on the target and use `--deploy-container-runtime=podman`. A different Compose filename uses `--compose-file=dev.compose.yml`; a different Dockerfile uses `--dockerfile=Containerfile`.

Separate environments should normally use separate SSH keys and target-specific, verified `known_hosts` files. The same runner can deploy to several hosts by changing `--ssh-target`, credentials, and `--deploy-directory`. Registry credentials need push access from the runner and pull access from every deployment host.

The following complete examples retain those choices rather than hiding them behind the basic workflow. Staging runs on the runner host with Podman and a commit-specific tag. Production runs on a separate host with Docker and the default `latest` tag. Both use the public values and named build secrets from the Go Dockerfile above.

```text
push to a branch other than master/main
    -> staging.yml
    -> release --tag=$GITHUB_SHA --deploy-container-runtime=podman ...

push to master/main
    -> prod.yml
    -> release --tag=latest --deploy-container-runtime=docker ...
```

### `staging.yml`

This example assumes the runner service user has `actions/secrets/runner_deploy_key`, `actions/secrets/my-app_runner_known_hosts`, `actions/secrets/registry_token`, `actions/secrets/github_token`, and `actions/secrets/bar_secret`. Its public key is authorized for `runnervpsuser@runnerhost`, and the Compose project is at `$HOME/staging/my-app/dev.compose.yml` on that host.

```yaml
name: Deploy staging

on:
  push:
    branches-ignore: [master, main]

permissions:
  contents: read

jobs:
  deploy:
    runs-on: [self-hosted, linux, x64]
    env:
      APP_NAME: my-app
      DEPLOY_CONTAINER_RUNTIME: podman

      REGISTRY_URL: ${{ secrets.REGISTRY_URL }}
      REGISTRY_USERNAME: ${{ secrets.REGISTRY_USERNAME }}
      SECRETS_DIR: ${{ secrets.RUNNERVPS_SECRETS_DIR }}
      REGISTRY_PASSWORD_SECRET: ${{ secrets.RUNNERVPS_DAGGERER_REGISTRY_AUTH_SECRET }}

      GITHUB_TOKEN_SECRET: ${{ secrets.GITHUB_TOKEN_SECRET }}
      BAR_SECRET: ${{ secrets.BAR_SECRET }}

      SSH_TARGET: runnervpsuser@runnerhost
      SSH_KEY_SECRET: ${{ secrets.RUNNERVPS_DAGGERER_SSH_KEY_SECRET }}
      KNOWN_HOSTS_SECRET: ${{ secrets.STAGEVPS_DAGGERER_KNOWN_HOSTS_SECRET }}

      # Optional paths inside Dagger's temporary SSH client.
      SSH_KEY_MOUNT_PATH: /run/secrets/runner_ssh_key
      KNOWN_HOSTS_MOUNT_PATH: /run/secrets/app_runner_known_hosts

    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release staging
        shell: bash
        run: |
          set -euo pipefail
          secrets_path="$HOME/$SECRETS_DIR"

          go_version="$(awk '$1 == "go" { print $2 }' go.mod)"
          : "${go_version:?go.mod must contain a go directive}"
          build_env_file="$(mktemp)"
          trap 'rm -f "$build_env_file"' EXIT
          printf 'GO_VERSION=%s\nAPP_PACKAGE=.\nFOO=hello\n' "$go_version" > "$build_env_file"

          dagger -W github.com/ninesl/daggerer@master api call release \
            --source=. \
            --registry="$REGISTRY_URL" \
            --app-name="$APP_NAME" \
            --tag="$GITHUB_SHA" \
            --build-env-file="$build_env_file" \
            --build-secret-ids=github_token,BAR \
            --build-secrets="file://${secrets_path}/${GITHUB_TOKEN_SECRET},file://${secrets_path}/${BAR_SECRET}" \
            --ssh-target="$SSH_TARGET" \
            --ssh-key="file://${secrets_path}/${SSH_KEY_SECRET}" \
            --known-hosts="file://${secrets_path}/${KNOWN_HOSTS_SECRET}" \
            --ssh-key-mount-path="$SSH_KEY_MOUNT_PATH" \
            --known-hosts-mount-path="$KNOWN_HOSTS_MOUNT_PATH" \
            --deploy-directory="staging/$APP_NAME" \
            --registry-username="$REGISTRY_USERNAME" \
            --registry-password="file://${secrets_path}/${REGISTRY_PASSWORD_SECRET}" \
            --compose-file=dev.compose.yml \
            --deploy-container-runtime="$DEPLOY_CONTAINER_RUNTIME"
```

Even though the runner and staging target are the same machine, Daggerer's temporary client reaches it over SSH. Podman stores the pulled image, and the staging Compose project can keep its own `runtime.env` and application secrets beside `dev.compose.yml`.

### `prod.yml`

Production uses separate SSH credentials and host verification. The private key, `known_hosts`, registry token, PAT, and `BAR` file remain on the runner. `prodvpsuser@prodhost` needs the authorized public key, Docker with Compose, and `$HOME/prod/my-app/prod.compose.yml` plus any runtime configuration used by that project.

```yaml
name: Deploy production

on:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  deploy:
    runs-on: [self-hosted, linux, x64]
    env:
      APP_NAME: my-app
      DEPLOY_CONTAINER_RUNTIME: docker

      REGISTRY_URL: ${{ secrets.REGISTRY_URL }}
      REGISTRY_USERNAME: ${{ secrets.REGISTRY_USERNAME }}
      SECRETS_DIR: ${{ secrets.RUNNERVPS_SECRETS_DIR }}
      REGISTRY_PASSWORD_SECRET: ${{ secrets.RUNNERVPS_DAGGERER_REGISTRY_AUTH_SECRET }}

      GITHUB_TOKEN_SECRET: ${{ secrets.GITHUB_TOKEN_SECRET }}
      BAR_SECRET: ${{ secrets.BAR_SECRET }}

      SSH_TARGET: prodvpsuser@prodhost
      SSH_KEY_SECRET: ${{ secrets.PRODVPS_DAGGERER_SSH_KEY_SECRET }}
      KNOWN_HOSTS_SECRET: ${{ secrets.PRODVPS_DAGGERER_KNOWN_HOSTS_SECRET }}

      # Optional paths inside Dagger's temporary SSH client.
      SSH_KEY_MOUNT_PATH: /run/secrets/prod_ssh_key
      KNOWN_HOSTS_MOUNT_PATH: /run/secrets/app_prod_known_hosts

    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release production
        shell: bash
        run: |
          set -euo pipefail
          secrets_path="$HOME/$SECRETS_DIR"

          go_version="$(awk '$1 == "go" { print $2 }' go.mod)"
          : "${go_version:?go.mod must contain a go directive}"
          build_env_file="$(mktemp)"
          trap 'rm -f "$build_env_file"' EXIT
          printf 'GO_VERSION=%s\nAPP_PACKAGE=.\nFOO=hello\n' "$go_version" > "$build_env_file"

          dagger -W github.com/ninesl/daggerer@master api call release \
            --source=. \
            --registry="$REGISTRY_URL" \
            --app-name="$APP_NAME" \
            --ssh-target="$SSH_TARGET" \
            --ssh-key="file://${secrets_path}/${SSH_KEY_SECRET}" \
            --known-hosts="file://${secrets_path}/${KNOWN_HOSTS_SECRET}" \
            --ssh-key-mount-path="$SSH_KEY_MOUNT_PATH" \
            --known-hosts-mount-path="$KNOWN_HOSTS_MOUNT_PATH" \
            --deploy-directory="prod/$APP_NAME" \
            --build-env-file="$build_env_file" \
            --build-secret-ids=github_token,BAR \
            --build-secrets="file://${secrets_path}/${GITHUB_TOKEN_SECRET},file://${secrets_path}/${BAR_SECRET}" \
            --registry-username="$REGISTRY_USERNAME" \
            --registry-password="file://${secrets_path}/${REGISTRY_PASSWORD_SECRET}" \
            --compose-file=prod.compose.yml \
            --deploy-container-runtime="$DEPLOY_CONTAINER_RUNTIME"
```

This workflow omits `--tag`, so it publishes and deploys `latest`. The staging and production examples are intentionally complete: they can live in separate workflow files without relying on an undocumented shared setup.

To deploy an existing image without building it again, call `deploy` with the same registry, application name, tag, and deployment arguments used by `release`:

```bash
dagger -W github.com/ninesl/daggerer@master api call deploy --help
```

## API Reference

Inspect the module and each operation directly from Git:

```bash
dagger -W github.com/ninesl/daggerer@master api functions
dagger -W github.com/ninesl/daggerer@master api call build-only --help
dagger -W github.com/ninesl/daggerer@master api call build --help
dagger -W github.com/ninesl/daggerer@master api call deploy --help
dagger -W github.com/ninesl/daggerer@master api call release --help
```

The caller supplies the source checkout, registry and image name, deployment target, credentials, and deployment directory. `release` and `deploy` default to `latest` and `compose.yml`; build operations default to `Dockerfile`. Secret mount paths and the Alpine SSH helper image also have defaults, so most callers should leave them alone.

`--deploy-container-runtime` is required and accepts `docker` or `podman`. It selects the command used for registry login, image pull, and Compose on the deployment host; it is independent of the runtime hosting the Dagger Engine.

To install Daggerer into a consumer workspace instead of selecting it with `-W`:

```bash
dagger init
dagger module install github.com/ninesl/daggerer@master
dagger api call daggerer release --help
```

The installed module is namespaced as `daggerer`. The `-W` form instead selects Daggerer as the entrypoint for that command, so `release` is called directly.

[dagger-workspace]: https://docs.dagger.io/reference/cli#options
[dagger-build]: https://docs.dagger.io/reference/api/directory#dockerBuild
[dagger-container]: https://docs.dagger.io/reference/api/container
[dagger-auth]: https://docs.dagger.io/reference/api/container#withRegistryAuth
[dagger-publish]: https://docs.dagger.io/reference/api/container#publish
[dagger-exec]: https://docs.dagger.io/reference/api/container#withExec
[dagger-secret]: https://docs.dagger.io/reference/api/secret
[dagger-mount-secret]: https://docs.dagger.io/reference/api/container#withMountedSecret
[compose-interpolation]: https://docs.docker.com/reference/compose-file/interpolation/
