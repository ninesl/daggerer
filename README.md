# Daggerer

Build, publish, and deploy an app from a self-hosted GitHub runner.

## Guide

- [Prerequisites](#prerequisites)
- [Mental Model](#mental-model)
  - [Release Sequence And Deployment Targets](#release-sequence-and-deployment-targets)
  - [Daggerer Inputs And Caller Choices](#daggerer-inputs-and-caller-choices)
- [Quick Start](#quick-start)
  - [Action](#action)
  - [How Compose Selects The App Image](#how-compose-selects-the-app-image)
  - [Application Runtime Values And Secrets](#application-runtime-values-and-secrets)
  - [Supported Deployment Tools](#supported-deployment-tools)
  - [How Deploy Works](#how-deploy-works)
- [Caching: Build Reuse And Live Deployment](#caching-build-reuse-and-live-deployment)
- [Shared Dockerfile And Build Inputs](#shared-dockerfile-and-build-inputs)
  - [Private Dependency And PAT Requirements](#private-dependency-and-pat-requirements)
  - [Public Values: Native EnvFile Or File Input](#public-values-native-envfile-or-file-input)
  - [Passing A Public `.env` From The CLI](#passing-a-public-env-from-the-cli)
  - [Private Values: Named Secrets](#private-values-named-secrets)
  - [Private Values: A Secret `.env` File](#private-values-a-secret-env-file)
- [Example GitHub Actions Workflows](#example-github-actions-workflows)
  - [`staging.yml`](#stagingyml)
  - [`prod.yml`](#prodyml)
- [Workspace Usage](#workspace-usage)

## Prerequisites

> Read through the entire documentation first. Your application's requirements will likely differ from these examples. Review the example Daggerer pipelines below to understand the available options to create the simplest pipeline for your needs.
>
> The examples favor [grug-brained](https://grugbrain.dev/) development and utilize [locality of behavior](https://htmx.org/essays/locality-of-behaviour/) so the resulting pipeline stays small and idiomatic for Dagger and Go.


Daggerer supports [`docker`](https://docs.docker.com/engine/install/), [`docker compose`](https://docs.docker.com/compose/install/linux/) and [`podman`](https://podman.io/docs/installation), [`podman compose`](https://docs.podman.io/en/latest/markdown/podman-compose.1.html) for `deploy` and `release`. Install one of these combinations on each deployment host:

Compose runs on the deployment host. Provision the host filesystem with the SSH user's deployment directory, Compose file, runtime environment file, and application secrets. The [example workflows](#example-github-actions-workflows) show the complete runner and deployment-host layouts.

Create a self-hosted runner for your repo: `https://github.com/<user>/<repo>/settings/actions/runners/new`

For the rest of this documentation, we use a remote VPS with the `linux` operating system and `x64` architecture, comparable to a basic [Amazon EC2 instance](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/EC2_GetStarted.html). The example workflows select it with the `[self-hosted, linux, x64]` runner labels.
Install and start the runner service from its directory.

```bash
sudo ./svc.sh install "$USER"
sudo ./svc.sh start
sudo ./svc.sh status
```

[Install the Dagger CLI](https://docs.dagger.io/getting-started/install) for the Linux user that owns the self-hosted runner service. The workflow invokes the CLI as that user, and the CLI starts or connects to the Dagger Engine.

Daggerer was built with Dagger `v1.0.0-beta.13`. Run `dagger version` as the runner service user to verify CLI and Engine access before running the GitHub Actions workflow.

`build-only` is deployment-runtime agnostic. It accepts an application source directory and builds any Dockerfile supported by [`Directory.DockerBuild`][dagger-build].

## Mental Model

Daggerer is a reusable Dagger module for applications that deploy with a Dockerfile and Compose. It exposes four operations:

- `build-only` calls [`DockerBuild`](https://docs.dagger.io/reference/api/directory#dockerBuild) and returns a [`*dagger.Container`][dagger-container] for use as a cached build stage in another pipeline.
- `build` checks registry authentication, calls `build-only`, applies [`WithRegistryAuth`][dagger-auth], calls [`Publish`][dagger-publish], and returns the published image reference.
- `deploy` constructs an image reference from the registry, application name, and tag, then updates a Compose deployment over SSH.
- `release` calls `build` and then `deploy` for the selected SSH target, using the same registry, application name, and tag.

[`DockerBuild`][dagger-build], [`WithRegistryAuth`][dagger-auth], and [`Publish`][dagger-publish] are Dagger APIs. Daggerer connects them to an SSH-based Compose deployment.

Load Daggerer directly from Git and inspect each operation:

```bash
dagger -W github.com/ninesl/daggerer@master api functions
dagger -W github.com/ninesl/daggerer@master api call build-only --help
dagger -W github.com/ninesl/daggerer@master api call build --help
dagger -W github.com/ninesl/daggerer@master api call deploy --help
dagger -W github.com/ninesl/daggerer@master api call release --help
```

The command shape is always:

```text
dagger -W github.com/ninesl/daggerer@master api call <operation> <arguments>
```

### Release Sequence And Deployment Targets

The workflow and `release` execute in this order:

1. GitHub checks out the application on the runner.
2. `build` checks registry authentication using an internal helper. A failed login stops the release before any application build work.
3. `build` calls `build-only`, reusing eligible cached Dockerfile build work.
4. `build` attaches registry credentials with [`WithRegistryAuth`][dagger-auth] and calls [`Publish`][dagger-publish] to upload the requested image tag.
5. `deploy` opens fresh SSH connections to the selected deployment host. It checks the Compose file, logs into the registry on that host, pulls the requested tag, and runs Compose with `APP_IMAGE` set to that image reference.

The initial login checks registry reachability and authentication. Publishing checks repository push permission; deployment checks the SSH target and repository pull permission.

`build-only` is the reusable build stage: other pipelines can consume its container directly, just as `build` consumes it here.

The runner and deployment host may be the same VPS or different machines. Each call supplies its SSH target, key, verified `known_hosts`, deployment runtime, directory, and Compose file. The consumer decides when to call Daggerer and which arguments to pass.

The examples use these role placeholders:

- `$RUNNERVPS` is the host running the GitHub runner service.
- `$RUNNER_USER` is the Linux user running that service on `$RUNNERVPS`.
- `$STAGEVPS` and `$STAGE_USER` are the staging SSH host and user.
- `$PRODVPS` and `$PROD_USER` are the production SSH host and user.

The hosts and users may be the same. The names are descriptive placeholders rather than Daggerer settings. In Quick Start, configure the example `PRODVPS_SSH_TARGET` variable to reach `$RUNNERVPS` as `$RUNNER_USER`, so the runner also serves as the deployment host.

Quick Start demonstrates one possible setup: one VPS, Docker, the default `Dockerfile`, `compose.yml`, and `latest` settings.

### Daggerer Inputs And Caller Choices

Names beginning with `--` are Daggerer API arguments. Names in uppercase are shell variables chosen by these example workflows. GitHub Actions secret names, filesystem paths, SSH users and hosts, image tags, Compose filenames, and BuildKit secret IDs are also caller choices.

| Daggerer argument | What the caller supplies |
| --- | --- |
| `--source` | Application checkout used as the Dockerfile build context |
| `--build-env-file` | Public Dockerfile build values in dotenv format |
| `--build-values` | Public build values constructed as a Dagger `EnvFile` by an API client |
| `--build-secret-ids` / `--build-secrets` | Dockerfile secret mount IDs and matching Dagger Secrets |
| `--build-secret-env` | Private dotenv file mounted under the fixed Dockerfile secret ID `build_env` |
| `--registry`, `--app-name`, `--tag` | Published and deployed image reference |
| `--registry-username`, `--registry-password` | Registry authentication |
| `--ssh-target`, `--ssh-key`, `--known-hosts` | Deployment host and SSH authentication |
| `--deploy-directory`, `--compose-file` | Existing Compose project under the SSH user's home |
| `--deploy-container-runtime` | `docker` or `podman` command installed on the deployment host |

For example, `REGISTRY_URL` is a workflow variable whose value is passed to Daggerer's `--registry` argument. `RUNNERVPS_DAGGERER_REGISTRY_AUTH_SECRET` is an example GitHub Actions secret name whose value is a runner-local filename; that file is passed to `--registry-password` through `file://`. Rename either workflow setting to fit your repository while keeping the Daggerer argument name.

The Compose variable `APP_IMAGE` is a key-value pair Daggerer sends to the remote Compose command. These examples read it in `image: ${APP_IMAGE:?APP_IMAGE is required}` to select the image; your Compose project can use another arrangement. Named build secret IDs such as `github_token` and `BAR` are chosen by the caller and must match the Dockerfile mount IDs. `build_env` is the fixed mount ID used by `--build-secret-env`.


## Quick Start

### Action

This workflow deploys an application when `master` or `main` is pushed. The self-hosted runner and production deployment are on the same VPS.

`dagger -W github.com/ninesl/daggerer@master` uses Dagger's [`-W` workspace option][dagger-workspace] to load this module directly from Git. `api call release` invokes [the `Release` API](daggerer/main.go). The SSH target is the same VPS, using its hostname or network address reachable from the Dagger container. Using SSH here keeps this example identical to a release targeting another server.

This basic example uses an application Dockerfile that needs no additional build inputs. `Release` defaults to `Dockerfile`, `compose.yml`, and `latest`; the workflow supplies Docker as the required deployment runtime. The [later workflows](#example-github-actions-workflows) show public build values and private dependencies.

Place this `compose.yml` under the SSH user's `$HOME/prod/<app-name>`:

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
```

Daggerer supplies `APP_IMAGE=registry/app-name:latest` to the remote Compose command. This example uses [Compose interpolation][compose-interpolation] to select that image; using `APP_IMAGE` in your Compose file is our recommendation, not a requirement of Daggerer.

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
      # Required application name. It forms the image and deployment directory.
      APP_NAME: <app-name>

      # Deployment preset; independent of the runtime hosting Dagger's engine.
      DEPLOY_CONTAINER_RUNTIME: docker

      # Registry hostname[:port]. Credentials need push/pull
      # access. Login checks authentication; publish/pull check image permissions.
      REGISTRY_URL: ${{ secrets.REGISTRY_URL }}
      REGISTRY_USERNAME: ${{ secrets.REGISTRY_USERNAME }}

      # This directory already exists under $RUNNER_USER's $HOME on $RUNNERVPS.
      SECRETS_DIR: ${{ secrets.RUNNERVPS_SECRETS_DIR }}

      # This shared file contains a dedicated registry token. Scope it to only
      # the image namespace every repo using this runner is allowed to publish.
      REGISTRY_PASSWORD_SECRET: ${{ secrets.RUNNERVPS_DAGGERER_REGISTRY_AUTH_SECRET }}

      # For this same-host example, configure this as <runner-user>@<runner-host>.
      # Use the runner host's network hostname/address reachable from Dagger.
      SSH_TARGET: ${{ secrets.PRODVPS_SSH_TARGET }}

      # Generate this key on $RUNNERVPS as $RUNNER_USER, then authorize its
      # public key for $PROD_USER on $PRODVPS. This value is its private filename.
      SSH_KEY_SECRET: ${{ secrets.RUNNERVPS_DAGGERER_SSH_KEY_SECRET }}

      # Generate and verify known_hosts on $RUNNERVPS for $PRODVPS. Keep it
      # target-specific, for example <app-name>_prod_known_hosts.
      KNOWN_HOSTS_SECRET: ${{ secrets.PRODVPS_DAGGERER_KNOWN_HOSTS_SECRET }}

    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release
        shell: bash
        run: dagger -W github.com/ninesl/daggerer@master api call release
          --source=.
          --registry="$REGISTRY_URL"
          --app-name="$APP_NAME"
          --ssh-target="$SSH_TARGET"
          --ssh-key="file://$HOME/$SECRETS_DIR/$SSH_KEY_SECRET"
          --known-hosts="file://$HOME/$SECRETS_DIR/$KNOWN_HOSTS_SECRET"
          --deploy-directory="prod/$APP_NAME"
          --deploy-container-runtime="$DEPLOY_CONTAINER_RUNTIME"
          --registry-username="$REGISTRY_USERNAME"
          --registry-password="file://$HOME/$SECRETS_DIR/$REGISTRY_PASSWORD_SECRET"
```

For this action, `release` first calls `build`.

`build`:

1. Uses an ephemeral Docker CLI container to run `docker login` against the provided registry. It fails before building when the registry is unavailable or authentication fails.
2. Calls `build-only` with `--source=.` and the default `Dockerfile`.
3. `build-only` calls [`Directory.DockerBuild`][dagger-build] and returns the resulting [`*dagger.Container`][dagger-container].
4. Creates the image name directly from the provided values: `$REGISTRY_URL/$APP_NAME:latest`.
5. Calls [`WithRegistryAuth`](https://docs.dagger.io/reference/api/container#withRegistryAuth) on that container with `REGISTRY_USERNAME` and the registry password Secret, authorizing the Dagger engine to push.
6. Calls [`Publish`](https://docs.dagger.io/reference/api/container#publish) on the authenticated container to upload the tagged image.
7. Returns the published image reference.

`release` then calls `deploy` with the same `REGISTRY_URL`, `APP_NAME`, and `latest` tag. `deploy` constructs the image name from those values.

Publishing to `latest` replaces the previous `latest` tag in that image repository (the registry must allow tag updates). Deploy pulls whatever that tag references at pull time. Use `--tag="$GITHUB_SHA"` to publish and deploy a commit-specific tag instead.

`deploy` (Quick Start explicitly selects Docker):

1. Mounts the SSH key, `known_hosts`, and registry password in an ephemeral SSH helper container.
2. Connects to `SSH_TARGET` and changes to `--deploy-directory` under the SSH user's home.
3. Checks that the selected Compose file exists.
4. Sends the mounted registry password to the deployment host's `docker login` through `--password-stdin`. It fails with `registry unavailable or authentication failed` before pull or Compose when login fails.
5. Runs `docker pull` with the exact registry, application name, and tag passed to `deploy`.
6. Sets `APP_IMAGE` to that image reference.
7. Runs `docker compose up -d --force-recreate --remove-orphans`.

### How Compose Selects The App Image

`--deploy-directory` points to the directory containing the existing Compose file under the SSH user's home. Docker or Podman stores the pulled images.

The pull is also the image-availability check. A failed pull returns `image unavailable` and stops deployment. Use `release` to build, publish, and deploy, or `deploy` to use an image already in the registry.

Daggerer supplies `APP_IMAGE=registry/app-name:tag` to the remote Compose process. We recommend referencing that variable for the app service:

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
```

Compose substitutes the supplied image reference when it reads this file. Each service referencing `APP_IMAGE` receives the selected app image; other services use their own image references. Configure the app service with `image:` as shown.

For example, with Docker selected, an app named `my-app`, and tag `latest`, the final remote command has this shape:

```bash
cd "$HOME/prod/my-app" && \
  APP_IMAGE=registry.example.com/team/my-app:latest \
  docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

This runs the selected Compose project. Compose may pull other service images according to their configuration. Daggerer pulls the app image before this command.

### Application Runtime Values And Secrets

Build inputs, app runtime configuration, and deployment credentials have separate jobs:

| Purpose | Examples | Supplied through | Used where |
| --- | --- | --- | --- |
| Build values | `GO_VERSION`, `APP_PACKAGE`, `FOO` | `buildEnvFile` or `buildValues` | Dockerfile `ARG` instructions in Dagger |
| Build secrets | Dependency PAT, `BAR`, private build `.env` | `buildSecrets` or `buildSecretEnv` | Dockerfile secret mounts in Dagger |
| App runtime values | `LOG_LEVEL`, service URLs | Compose `environment` or `env_file` | The running app on the deployment host |
| App runtime secrets | App API token, database password | Compose `secrets` | The running app on the deployment host |
| SSH authentication | Deployment private key and `known_hosts` | `sshKey` and `knownHosts` | Daggerer's SSH client |
| Registry authentication | Registry username and token | `registryUsername` and `registryPassword` | Image publishing and remote login/pull |

Daggerer passes `APP_IMAGE` to Compose to select the release. The Compose file supplies the application's runtime values and secrets. The Dockerfile can also place public defaults in the image, such as this example's `ENV FOO`.

For an app with runtime configuration, place these files beside its Compose file on each deployment host:

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
    # Loaded from the Compose project on the SSH target.
    env_file: ./runtime.env
    secrets:
      - app_token

secrets:
  app_token:
    # Host file mounted at /run/secrets/app_token inside the running app.
    file: ./secrets/app_token
```

For example, each target can define its own service URL or log level in `runtime.env` and its own `secrets/app_token` value. Provision these files with the Compose project on each target. See the Compose docs for [`env_file`](https://docs.docker.com/reference/compose-file/services/#env_file) and [`secrets`](https://docs.docker.com/reference/compose-file/secrets/).

`--deploy-container-runtime` and `--compose-file` are independent. Deploy runs:

```text
<deploy-container-runtime> compose -f <compose-file> up -d --force-recreate --remove-orphans
```

Each call selects `podman` or `docker` for login, pull, and Compose on its SSH target. The examples select Podman on the development target and Docker on the production target.

### Supported Deployment Tools

`--deploy-container-runtime` accepts **`docker` or `podman`** and selects the command-line tool on the SSH target.

| Argument | Required on the SSH target |
| --- | --- |
| `--deploy-container-runtime=docker` | Docker and its Compose plugin. |
| `--deploy-container-runtime=podman` | Podman and a compatible external Compose provider. |

The selected runtime and its Compose support must be installed on the deployment host. If a command fails, deployment stops.

### How Deploy Works

The self-hosted runner could run `ssh`, `cd`, and `docker compose` directly in the workflow. Daggerer intentionally keeps that work inside the `deploy` API so `release` and standalone `deploy` use the same implementation.

[`WithExec`][dagger-exec] executes inside a Dagger container. Daggerer creates a temporary Alpine container containing `openssh-client` and uses it as an SSH client:

```text
self-hosted runner
    -> Dagger engine
        -> temporary SSH client container
            -> SSH deployment host
                -> Docker or Podman Compose
```

For deployment, Dagger mounts the SSH key, `known_hosts`, and registry password inside that temporary container using [`WithMountedSecret`][dagger-mount-secret]. It then evaluates four explicit [`WithExec`][dagger-exec] steps:

```text
1. ssh <target> "check compose.yml"
2. ssh <target> "container-cli login --password-stdin"
3. ssh <target> "container-cli pull <registry/app:tag>"
4. ssh <target> "APP_IMAGE=<registry/app:tag> container-cli compose up ..."
```

The first command enters the deployment directory and checks its Compose file. The next two authenticate and pull the image. The fourth enters the deployment directory again and runs Compose. A failed command stops the sequence. The temporary client container and its mounted Secrets are discarded after the Dagger call. Registry login can persist credentials in the deployment user's Docker/Podman authentication storage.

See [Caching: build reuse and live deployment](#caching-build-reuse-and-live-deployment) for how cached image builds are combined with fresh SSH deployment steps.

Every deployment uses one SSH form:

```text
--ssh-target=<user>@<host>
--ssh-key=<private-key-secret>
--known-hosts=<verified-known-hosts-secret>
```

The private key authenticates Daggerer to the SSH user. `known_hosts` verifies the target server fingerprint. Both are required, and strict host-key checking uses the supplied file.

These examples trust every workflow running as `$RUNNER_USER` to read that account's deployment credentials. Restrict access to the credential directory and control which workflows use the runner. Separate `known_hosts` files select the trusted server identity for each target.

The deployment is remote from the temporary SSH client's point of view, even when the GitHub runner and deployment directory are on the same VPS. The SSH target must therefore be reachable from the Dagger engine's container network. It may use public, private, or VPN networking; it only needs an SSH address reachable from that container.

The runner service, workflow shell, and Dagger CLI use `$RUNNER_USER` and its `$HOME`. The runner-local Dagger CLI opens each path passed through [`file://`](https://docs.dagger.io/reference/api/secret#api-reference) using normal Linux permissions and creates a [`*dagger.Secret`][dagger-secret]. Daggerer [mounts those Secret values inside its ephemeral containers](https://docs.dagger.io/reference/api/container#withMountedSecret).

The default helper-container mount paths are `/run/secrets/ssh_key` for the SSH key, `/run/secrets/known_hosts` for host keys, and `/run/secrets/registry_password` for registry authentication. Change them with `--ssh-key-mount-path`, `--known-hosts-mount-path`, and `--registry-password-mount-path`.

For `deploy` and `release`, supply `--ssh-key`, `--known-hosts`, and `--registry-password` as required [`*dagger.Secret`][dagger-secret] inputs. The caller chooses each source, such as `file:///path/on/RUNNERVPS`; the mount-path settings choose where those values appear inside Dagger's helper containers.

`--deploy-directory` is relative to the SSH user's `$HOME`. Provision the selected Compose file there before deploying.

## Caching: Build Reuse And Live Deployment

| Work | Cache behavior |
| --- | --- |
| `build-only` / [`DockerBuild`][dagger-build] | Reuses eligible cached build work for unchanged inputs. |
| Build-time registry authentication | Runs each invocation to check current authentication. |
| [`Publish`][dagger-publish] | Performs the registry publication; existing image blobs can be reused. |
| Deployment SSH commands | Execute afresh, including server verification and client authentication. |
| Remote image pull | Contacts the registry for the requested tag; existing local image layers can be reused. |

Image builds reuse Dagger's cache. Registry authentication, publishing, and SSH deployment still run for each release, so an unchanged application can be deployed again without rebuilding it.

Every deployment therefore connects to the explicitly selected `--ssh-target`, authenticates, pulls `registry/app-name:tag`, and runs Compose there. With `latest`, it pulls the image currently assigned to that tag in the registry. Changing the deployment host does not require rebuilding the application.

## Shared Dockerfile And Build Inputs

Keep one `Dockerfile` in the application repository. The staging and production examples below build this shared file. Daggerer is language-independent; this small Go app has a main package at the repository root, committed `go.mod` and `go.sum` files, and a private GitHub module listed in `go.mod` (for example, `github.com/your-org/private-lib`). Your GitHub account must have read access to that dependency repository.

### Private Dependency And PAT Requirements

Private dependency downloads use a GitHub personal access token (PAT) belonging to an account that can read each dependency repository.

1. Read GitHub's [personal access token documentation](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens).
2. [Create a fine-grained PAT](https://github.com/settings/personal-access-tokens/new). Select the dependency repository owner, grant access to the required dependency repositories, and grant **Contents: Read-only**. Complete organization approval if required. If your account/repository arrangement is not supported by fine-grained tokens, follow GitHub's documented classic-token requirements, including SSO authorization where applicable.
3. Save the raw token in a file readable by the runner service account, such as `$HOME/actions/secrets/github_token`. Set `RUNNERVPS_SECRETS_DIR` to `actions/secrets` and the GitHub Actions secret `GITHUB_TOKEN_SECRET` to `github_token` for this example.
4. Set the Dockerfile's `GOPRIVATE` pattern to your private module namespace. It tells Go to bypass the public module proxy and checksum database for matching modules; the PAT supplies Git authentication.

The staging and production workflows pass that file as a Dagger Secret named `github_token`. The Dockerfile mounts it only for dependency download, using temporary Git configuration rather than embedding the token in a build argument or image configuration. Missing required credentials or denied repository access fails the build.

```dockerfile
# syntax=docker/dockerfile:1.7

# Supplied from the application's go.mod by the workflow.
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
# BAR demonstrates a second build secret. Replace the nonempty check with the
# build tool that needs it; keep it in the secret mount.
RUN --mount=type=secret,id=BAR,required=true \
    test -s /run/secrets/BAR && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -o /app/server "$APP_PACKAGE"

FROM alpine:3.24.1
# Public application default baked into the image.
ARG FOO=hello
ENV FOO=$FOO
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/server /usr/local/bin/server
ENTRYPOINT ["/usr/local/bin/server"]
```

### Public Values: Native EnvFile Or File Input

`build-only`, `build`, and `release` accept `buildEnvFile` as a Dagger [`File`](https://docs.dagger.io/reference/api/file) and `buildValues` as a native Dagger [`EnvFile`](https://docs.dagger.io/reference/api/env-file). Daggerer parses the file with [`AsEnvFile`](https://docs.dagger.io/reference/api/file#asEnvFile), reads its variables, applies explicit `buildValues` last, and passes the result to [`DockerBuild`][dagger-build]. Explicit values override matching file entries; otherwise Dagger's native dotenv parsing rules apply. Neither input is secret.

From the CLI, pass a file with `--build-env-file=path`. In a Go pipeline, construct `buildValues` using Dagger's `EnvFile` API:

```go
// source is the application Directory; goVersion was read from its go.mod.
values := dag.EnvFile().
    WithVariable("GO_VERSION", goVersion).
    WithVariable("APP_PACKAGE", ".").
    WithVariable("FOO", "hello")

// A consumer using the installed module's generated client:
image := dag.Daggerer().BuildOnly(source, dagger.DaggererBuildOnlyOpts{
    BuildEnvFile: source.File(".env"), // Optional public file.
    BuildValues: values,              // Explicit values override that file.
    BuildSecretIds: []string{"github_token", "BAR"},
    BuildSecrets: []*dagger.Secret{pat, bar}, // Existing typed Secret inputs.
})
```

Explicit `buildValues` override matching values from `buildEnvFile`. Omitted values use the Dockerfile's `ARG` defaults. Our workflow supplies `GO_VERSION` from the app's `go.mod`.

`--tag` names the published image. The example workflows use `$GITHUB_SHA` for development branches and `latest` for production branches, with the same Dockerfile. Choose any tag policy appropriate for your workflow. Changing a consumed build argument can invalidate the affected build cache.

`GO_VERSION` comes from the application's `go` directive. The Dockerfile uses it to select `golang:<version>-alpine`; a missing directive or unavailable image tag fails the build.

`FOO` enters the Dockerfile as an `ARG` value and is copied to `ENV` in the final stage so the app can read it at runtime. `BAR` is mounted at `/run/secrets/BAR` for the `RUN` instruction that requests it. This small example checks that BAR is nonempty to demonstrate delivery.

### Passing A Public `.env` From The CLI

Instead of writing the values directly in the workflow, the application repository can contain this non-secret `.env` file:

```dotenv
FOO=hello
```

Pass the file directly rather than sourcing it as shell code. For our Go example, the workflow creates a temporary public env file with the version from `go.mod` and explicit application values:

```bash
set -euo pipefail
go_version="$(awk '$1 == "go" { print $2 }' go.mod)"
: "${go_version:?go.mod must contain a go directive}"
build_env_file="$(mktemp)"
trap 'rm -f "$build_env_file"' EXIT
printf 'GO_VERSION=%s\nAPP_PACKAGE=.\nFOO=hello\n' "$go_version" > "$build_env_file"
secrets_path="$HOME/$SECRETS_DIR"

# Build only here; use the same three input flags in the release workflows.
dagger -W github.com/ninesl/daggerer@master api call build-only \
  --source=. \
  --build-env-file="$build_env_file" \
  --build-secret-ids=github_token,BAR \
  --build-secrets="file://${secrets_path}/${GITHUB_TOKEN_SECRET},file://${secrets_path}/${BAR_SECRET}" \
  sync
```

For an existing file containing all required public values, use `--build-env-file=.env`. Every variable in that file becomes a build argument; the Dockerfile consumes the `ARG` names it declares. Store the PAT and BAR in separate files in the runner's secrets directory, alongside the SSH private key, known-hosts file, and registry token. Set `GITHUB_TOKEN_SECRET` and `BAR_SECRET` to their filenames. The Dockerfile gets the PAT and BAR through separate secret mounts; SSH and registry credentials serve their respective pipeline steps.

Since the Dockerfile uses `COPY . .`, add `.env` to the application's `.dockerignore` so the source file is excluded from the build context. Its public values still reach the Dockerfile through the separately supplied `--build-env-file`:

```dockerignore
.env
.git
```

### Private Values: Named Secrets

Credentials use two explicit lists: `--build-secret-ids` gives the Dockerfile mount IDs, and `--build-secrets` gives Dagger Secret sources in the same order. Both lists have equal lengths and unique IDs.

```bash
# Example inputs; the actual files must already exist on the runner.
--build-secret-ids='github_token,BAR' \
--build-secrets="file://${secrets_path}/${GITHUB_TOKEN_SECRET},file://${secrets_path}/${BAR_SECRET}"
```

Daggerer reads each [`Secret`][dagger-secret], gives it the supplied ID with [`SetSecret`](https://docs.dagger.io/reference/api/query#setSecret), and passes it to [`DockerBuild`][dagger-build]. The Dockerfile consumes it with `RUN --mount=type=secret,id=...`.

The shared Go Dockerfile requires both `github_token` and `BAR`; the staging and production workflows supply their IDs and Secret sources in matching order. The PAT authenticates private dependency downloads, and BAR demonstrates another build-time credential. Neither is used for SSH deployment or registry publishing. Build secrets are optional in Daggerer's generic API, but these required Dockerfile mounts make both inputs mandatory for this example.

### Private Values: A Secret `.env` File

`--build-secret-env` accepts a Dagger Secret containing an entire private dotenv file. Daggerer mounts it intact under the BuildKit ID `build_env`. The caller supplies the filename like the other credentials:

```bash
# Relevant argument for a Dockerfile that consumes the private file below:
--build-secret-env="file://${secrets_path}/${BUILD_SECRET_ENV_FILE}"
```

For example, the runner-local private file can contain `BAR='your-private-value'`. A Dockerfile can consume that secret file with the following instruction. Shell sourcing requires a trusted, shell-compatible file:

```dockerfile
RUN --mount=type=secret,id=build_env,required=true \
    set -eu; \
    . /run/secrets/build_env; \
    test -n "$BAR"
```

To use this file in place of the individual BAR mount in our Go Dockerfile, replace that build instruction's `id=BAR` mount and `test -s /run/secrets/BAR` with `id=build_env` and the sourcing/check above, then run the same `go build` command. Supply the PAT as a named secret as before. A private file may also contain the PAT when the dependency-download instruction mounts the file and uses its token variable.

The private file can coexist with other named secrets. When `--build-secret-env` is supplied, `build_env` is reserved for that file. In a Go pipeline, set `BuildSecretEnv: privateEnv` in the options above, where `privateEnv` is a `*dagger.Secret`.

## Example GitHub Actions Workflows

These are application-specific GitHub Actions workflows. Their branch filters choose which direct `release` call runs:

```text
push to a branch other than master/main
    -> staging.yml
    -> release --tag=$GITHUB_SHA --ssh-target=runnervpsuser@runnerhost --deploy-container-runtime=podman ...

push to master/main
    -> prod.yml
    -> release --tag=latest --ssh-target=prodvpsuser@prodhost --deploy-container-runtime=docker ...
```

`release` has the same API in both workflows. The consumer changes ordinary arguments and credentials for each target. Add, rename, or remove workflows to match the application architecture.

| Workflow choice | Non-master/main branches | Master/main branches |
| --- | --- | --- |
| SSH target | `runnervpsuser@runnerhost` | `prodvpsuser@prodhost` |
| Deployment runtime | `podman` | `docker` |
| Image tag | `$GITHUB_SHA` | `latest` |
| Remote Compose file | `staging/<app-name>/dev.compose.yml` under the SSH user's home | `prod/<app-name>/prod.compose.yml` under the SSH user's home |
| SSH key source | Explicit shared staging/dev key filename | Explicit production app key filename |
| Server trust source | Explicit runner/staging known-hosts filename | Explicit production known-hosts filename |

The different settings demonstrate API inputs rather than required environment architecture. A project may use one host, one runtime, one credential set, different environment names, or additional deployment workflows. `--ssh-target` selects the host, `file://` selects each source file, and the mount options select paths inside the helper container.

### `staging.yml`

```yaml
name: Deploy staging

# Illustrative filesystem layout. Choose your own directory and filenames.
# Here $RUNNER_USER = runnervpsuser, APP_NAME = my-app, SECRETS_DIR = actions/secrets.
# runnerhost is both the GitHub runner and the staging target, running Podman:
# /home/runnervpsuser/
#   actions/secrets/
#     runner_deploy_key          # Private SSH key for staging/dev.
#     runner_deploy_key.pub      # Its public key; authorize this on runnerhost.
#     my-app_runner_known_hosts  # Verified runnerhost server identity.
#     registry_token            # Registry password/token shared by these examples.
#     github_token              # Required PAT: reads the private dependency in go.mod.
#     bar_secret                # Separate build secret mounted under ID BAR.
#   .ssh/authorized_keys        # Contains the public key from runner_deploy_key.pub.
#   staging/my-app/
#     dev.compose.yml           # App service: image: ${APP_IMAGE:?APP_IMAGE is required}
#     runtime.env               # App settings, e.g. LOG_LEVEL=debug, if using env_file.
#     secrets/app_token         # Staging app credential, if using Compose secrets.
#                               # These are NOT the PAT/BAR build secret files above.
#
# Dagger's temporary SSH helper has a SEPARATE filesystem:
# /run/secrets/
#   runner_ssh_key              # Mounted contents of actions/secrets/runner_deploy_key.
#   app_runner_known_hosts      # Mounted contents of my-app_runner_known_hosts.
#   registry_password           # Mounted contents of registry_token (default mount).
#
# file:// reads from the runner's filesystem. SSH reaches runnerhost over the
# network even though staging lives on that same machine. Remote Podman stores
# pulled images itself; staging/my-app contains the Compose project.

on:
  push:
    # Consumer trigger: every branch except master/main calls the staging target.
    branches-ignore: [master, main]

permissions:
  contents: read

jobs:
  deploy:
    runs-on: [self-hosted, linux, x64]
    env:
      APP_NAME: my-app # Replace with your app name; matches the filesystem above.

      # Staging uses Podman on the SSH target.
      DEPLOY_CONTAINER_RUNTIME: podman

      # Registry hostname[:port]. Credentials need push/pull
      # access. Login checks authentication; publish/pull check image permissions.
      REGISTRY_URL: ${{ secrets.REGISTRY_URL }}
      REGISTRY_USERNAME: ${{ secrets.REGISTRY_USERNAME }}

      # Custom workflow variable: example directory under the runner user's home.
      SECRETS_DIR: ${{ secrets.RUNNERVPS_SECRETS_DIR }}
      # Custom GitHub secret name: its value is the filename registry_token.
      REGISTRY_PASSWORD_SECRET: ${{ secrets.RUNNERVPS_DAGGERER_REGISTRY_AUTH_SECRET }}

      # Custom GitHub secret name: its value is the filename github_token.
      GITHUB_TOKEN_SECRET: ${{ secrets.GITHUB_TOKEN_SECRET }}
      # Custom GitHub secret name: its value is the filename bar_secret.
      BAR_SECRET: ${{ secrets.BAR_SECRET }}

      # Direct SSH target. known_hosts contains: runnerhost ssh-ed25519 <key>
      SSH_TARGET: runnervpsuser@runnerhost

      # Supply the staging/dev key filename under the runner's secrets directory.
      # Its public key is authorized for runnervpsuser on runnerhost.
      # Trusted repos can share this key by supplying the same filename.
      # Custom GitHub secret name: its value is the filename runner_deploy_key.
      SSH_KEY_SECRET: ${{ secrets.RUNNERVPS_DAGGERER_SSH_KEY_SECRET }}

      # This GitHub secret contains a runner-host filename such as
      # <app-name>_runner_known_hosts. That file contains only runnerhost's
      # verified key for runnerhost.
      # Custom GitHub secret name: its value is my-app_runner_known_hosts.
      KNOWN_HOSTS_SECRET: ${{ secrets.STAGEVPS_DAGGERER_KNOWN_HOSTS_SECRET }}

      # These are optional paths inside the temporary SSH helper container.
      # They are deliberately different from the runner-host filenames above:
      # file:// selects the source; these values select the mount destination.
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

          # Derive the builder image version from this checkout's go.mod.
          go_version="$(awk '$1 == "go" { print $2 }' go.mod)"
          : "${go_version:?go.mod must contain a go directive}"
          build_env_file="$(mktemp)"
          trap 'rm -f "$build_env_file"' EXIT
          # Same public values as production; credentials use Secret inputs below.
          printf 'GO_VERSION=%s\nAPP_PACKAGE=.\nFOO=hello\n' "$go_version" > "$build_env_file"

          # Checks registry authentication before using the default Dockerfile
          # and reusing eligible cached build work.
          # SSH authentication, registry login, pull, and Compose execute afresh.
          # Publishes the checkout's exact commit SHA instead of latest.
          # staging/$APP_NAME/dev.compose.yml must exist under runnervpsuser's home.
          # Podman is used only for login, pull, and Compose on that target.
          # Same Go Dockerfile/ARG values as production; --tag labels the output.
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

### `prod.yml`

```yaml
name: Deploy production

# Illustrative layout using the same runner and another VPS. Choose your own paths.
# APP_NAME = my-app and SECRETS_DIR = actions/secrets for these illustrative paths.
# runnerhost still executes this GitHub workflow as runnervpsuser:
# /home/runnervpsuser/actions/secrets/
#   app_prod_ssh_key            # DIFFERENT private key from runner_deploy_key.
#   app_prod_ssh_key.pub        # Public half installed on prodhost, below.
#   my-app_prod_known_hosts    # Verified prodhost server key, not runnerhost's.
#   registry_token            # Same registry credential file as staging here.
#   github_token              # Same required private-dependency PAT as staging.
#   bar_secret                # Build-only secret, not copied to prodhost.
#
# prodhost is the SSH destination; it does not run this workflow:
# /home/prodvpsuser/
#   .ssh/authorized_keys      # Contains the public key from app_prod_ssh_key.pub.
#   prod/my-app/
#     prod.compose.yml        # App service: image: ${APP_IMAGE:?APP_IMAGE is required}
#     runtime.env             # App settings, e.g. LOG_LEVEL=info, if using env_file.
#     secrets/app_token       # Production app credential, if using Compose secrets.
#                             # These stay on prodhost; build PAT/BAR stay on runnerhost.
#
# Dagger's temporary SSH helper mounts the production inputs at:
# /run/secrets/
#   prod_ssh_key              # Contents of runnerhost's app_prod_ssh_key.
#   app_prod_known_hosts      # Contents of runnerhost's my-app_prod_known_hosts.
#   registry_password         # Contents of runnerhost's registry_token.
#
# The private key and known_hosts SOURCE files stay on runnerhost; prodhost only
# needs the authorized public key and its existing Compose project. Daggerer sends
# the registry password through SSH to docker login, which may persist auth there.
# Docker pulls the image into its own storage on prodhost. Daggerer passes APP_IMAGE
# to Compose; it does not copy or rewrite prod.compose.yml.

on:
  push:
    # Consumer trigger: master/main calls the production target with latest.
    branches: [master, main]

permissions:
  contents: read

jobs:
  deploy:
    runs-on: [self-hosted, linux, x64]
    env:
      APP_NAME: my-app # Replace with your app name; matches the filesystem above.

      # Explicit production choice instead of staging's Podman preset.
      # If both targets use Docker, set docker in both workflows.
      DEPLOY_CONTAINER_RUNTIME: docker

      # Registry hostname[:port]. Credentials need push/pull
      # access. Login checks authentication; publish/pull check image permissions.
      REGISTRY_URL: ${{ secrets.REGISTRY_URL }}
      REGISTRY_USERNAME: ${{ secrets.REGISTRY_USERNAME }}

      # Custom workflow variable: example directory under the runner user's home.
      SECRETS_DIR: ${{ secrets.RUNNERVPS_SECRETS_DIR }}
      # Custom GitHub secret name: value is registry_token in this example.
      REGISTRY_PASSWORD_SECRET: ${{ secrets.RUNNERVPS_DAGGERER_REGISTRY_AUTH_SECRET }}

      # Build still runs through Dagger from runnerhost, so its PAT source is here.
      # Custom GitHub secret name: value is the runner-local filename github_token.
      GITHUB_TOKEN_SECRET: ${{ secrets.GITHUB_TOKEN_SECRET }}
      # Custom GitHub secret name: value is the runner-local filename bar_secret.
      BAR_SECRET: ${{ secrets.BAR_SECRET }}

      # Direct SSH target. SSH connects as prodvpsuser to prodhost.
      # The known_hosts file contains: prodhost ssh-ed25519 <verified-key>
      SSH_TARGET: prodvpsuser@prodhost

      # Supply a separate production app key filename, such as app_prod_ssh_key.
      # The file lives on $RUNNERVPS; its public key is authorized for prodvpsuser
      # on prodhost. This custom GitHub secret supplies the filename.
      # Custom GitHub secret name: value is the filename app_prod_ssh_key.
      SSH_KEY_SECRET: ${{ secrets.PRODVPS_DAGGERER_SSH_KEY_SECRET }}

      # This GitHub secret contains a runner-host filename such as
      # <app-name>_prod_known_hosts. That separate file contains only prodhost's
      # verified key, so production trust is explicit and target-specific.
      # Custom GitHub secret name: value is my-app_prod_known_hosts.
      KNOWN_HOSTS_SECRET: ${{ secrets.PRODVPS_DAGGERER_KNOWN_HOSTS_SECRET }}

      # Custom helper mount paths are optional. They show that a Secret's source
      # filename and its path inside the ephemeral helper are independent.
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

          # Same go.mod-derived builder version as staging for the same source.
          go_version="$(awk '$1 == "go" { print $2 }' go.mod)"
          : "${go_version:?go.mod must contain a go directive}"
          build_env_file="$(mktemp)"
          trap 'rm -f "$build_env_file"' EXIT
          # Dockerfile inputs stay the same; the tag and SSH target change.
          printf 'GO_VERSION=%s\nAPP_PACKAGE=.\nFOO=hello\n' "$go_version" > "$build_env_file"

          # Checks registry authentication, then reuses cached Dockerfile build work.
          # Publishing updates latest; live SSH pulls its current registry value.
          # Explicitly selects Docker on prodhost, regardless of the runner runtime.
          # Same Go Dockerfile/ARG values as staging; the output tag defaults to latest.
          # prod/$APP_NAME/prod.compose.yml must exist under prodvpsuser's home.
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

## Workspace Usage

The recommended form loads Daggerer from Git:

```bash
dagger -W github.com/ninesl/daggerer@master api call release --help
```

The consumer needs no `dagger.toml` or `dagger.lock`. The Dagger engine loads the workspace and reuses its cache.

Dagger manages the source loaded through [`-W`][dagger-workspace] and caches it for reuse on the runner. When the Git reference resolves to the same commit, it can reuse that source; when it resolves to a different commit, it retrieves the newer source. This is handled internally by Dagger, separately from the application build cache.

To install Daggerer into a consumer workspace:

```bash
dagger init
dagger module install github.com/ninesl/daggerer@master
dagger api call daggerer release --help
```

The installed module is namespaced as `daggerer`. The `-W` form selects it as the entrypoint, so `release` is called directly.

[dagger-build]: https://docs.dagger.io/reference/api/directory#dockerBuild
[dagger-workspace]: https://docs.dagger.io/reference/cli#dagger-api-call
[compose-interpolation]: https://docs.docker.com/reference/compose-file/interpolation/
[dagger-container]: https://docs.dagger.io/reference/api/container
[dagger-auth]: https://docs.dagger.io/reference/api/container#withRegistryAuth
[dagger-publish]: https://docs.dagger.io/reference/api/container#publish
[dagger-exec]: https://docs.dagger.io/reference/api/container#withExec
[dagger-sync]: https://docs.dagger.io/reference/api/container#sync
[dagger-secret]: https://docs.dagger.io/reference/api/secret
[dagger-mount-secret]: https://docs.dagger.io/reference/api/container#withMountedSecret
