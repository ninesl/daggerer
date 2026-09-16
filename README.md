**Daggerer** enables automation for building a `Dockerfile`, publishing the image to an OCR registry, and deploying the image hosted on that OCR registry using `ssh` and `docker compose` [with Dagger](https://dagger.io).

## Quick Reference

Daggerer exposes four API functions:

- `build-only` builds an input `Dockerfile` with [`Directory.DockerBuild`][dagger-build] and returns a cached [`Container`][dagger-container].
- `build` validates registry authentication, calls `build-only`, applies [`WithRegistryAuth`](https://docs.dagger.io/reference/api/container#withRegistryAuth), and [`Publish`](https://docs.dagger.io/reference/api/container#publish) publishes the image.
- `deploy` connects to a target `ssh` server, pulls an existing image, and uses Compose to serve the image.
- `release` calls `build` and then `deploy` with the same registry, application name, and tag simplifying the top level API.

```bash
dagger -W github.com/ninesl/daggerer@master api functions
dagger -W github.com/ninesl/daggerer@master api call build-only --help
dagger -W github.com/ninesl/daggerer@master api call build --help
dagger -W github.com/ninesl/daggerer@master api call deploy --help
dagger -W github.com/ninesl/daggerer@master api call release --help

# The command shape is always:
dagger -W github.com/ninesl/daggerer@master api call function --arguments
```

## Contents

The documentation below uses Github Action examples on a self-hosted GitHub runner.

- [Quick Reference](#quick-reference)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Staging And Production](#staging-and-production)
- [Build Inputs](#build-inputs)
- [How Deployment Works](#how-deployment-works)
- [Caching](#caching)
- [Workspace Usage](#workspace-usage)

## Prerequisites

> Read through the documentation before adapting an example. The examples favor [grug-brained](https://grugbrain.dev/) development and [locality of behavior](https://htmx.org/essays/locality-of-behaviour/): each workflow passes its choices directly to the Daggerer API.

 `deploy` and `release` require either [`docker`](https://docs.docker.com/engine/install/) and [`docker compose`](https://docs.docker.com/compose/install/linux/) or [`podman`](https://podman.io/docs/installation) and [`podman compose`](https://docs.podman.io/en/latest/markdown/podman-compose.1.html) on the deployment host


Daggerer needs a `Dockerfile` to build the image. `build-only` and `build` do not require `docker` or `podman`, they use the Dagger Engine directly.

Create a self-hosted runner for your application's repository at `https://github.com/<owner>/<repo>/settings/actions/runners/new`. Install and start it according to GitHub's instructions. This location is your choice; Daggerer does not require a runner filesystem layout.

From the directory where you installed the runner, the GitHub setup instructions use:

```bash
sudo ./svc.sh install "$USER"
sudo ./svc.sh start
sudo ./svc.sh status
```

[Install the Dagger CLI](https://docs.dagger.io/getting-started/install) for the Linux user that owns the runner service. Daggerer was built with Dagger `v1.0.0-beta.13`; run `dagger version` as the user to verify access to the CLI and Dagger Engine.

## Quick Start

This example builds and publishes the checked-out repository as OCR image, then deploys it on the same VPS that runs the GitHub runner. 

For the Quick Start only, we use the following example filesystem. These paths are ordinary API inputs, not a layout required by Daggerer (or even recommended, these examples are to highlight how inputs can be sourced from anywhere).

```text
/home/runner/
├── secrets-actions/
│   └── my-app/
│       ├── registry_password
│       ├── ssh_key
│       └── known_hosts
├── actions-runner/
│   └── my-app/              # GitHub Actions self-hosted runner for my-app
│       ├── run.sh
│       └── svc.sh
└── apps/
    └── my-app/
        └── compose.yml
```

Place this file at `/home/runner/apps/my-app/compose.yml`:

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      - "5000:5000"
```

The application must listen on port `5000` inside its container. The service is then available on port `5000` of the VPS, this is normal `docker compose` behavior.

> In production I use [`caddy`](https://caddyserver.com/docs/quick-starts/reverse-proxy) (simplified replacement for [`nginx`](https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/)) as a reverse proxy.

The example keeps credential files in `$HOME/secrets-actions/my-app` on the VPS. The runner service user must be allowed to read them. The typed `Secret` arguments use Dagger's [`file://` secret provider](https://docs.dagger.io/adopting/secrets). Plain string settings are declared directly in the workflow.

```yaml
name: Release

on:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy
        run: dagger -W github.com/ninesl/daggerer@master api call release 
        # The checked-out application and Docker build context
          --source=.
        #  OCR registry host and namespace 
          --registry=registry.example.com/team
        # app name used with registry to create image reference
        # registry.example.com/team/<app-name>:latest
          --app-name=${{ github.event.repository.name }}
        # used to credential the OCR pull for deployment
          --registry-username=registry-user
          --registry-password=file://$HOME/secrets-actions/${{ github.event.repository.name }}/registry_password
        # the ssh user and host hosting the container from the compose.yml
          --ssh-target=runner@runner.example.com
          --ssh-key=file://$HOME/secrets-actions/${{ github.event.repository.name }}/ssh_key
          --known-hosts=file://$HOME/secrets-actions/${{ github.event.repository.name }}/known_hosts
          --deploy-directory=apps/${{ github.event.repository.name }}
        # the runtime being used on the ssh target.
          --deploy-container-runtime=docker
```

Our Github Action workflow is using these Daggerer defaults:

```yaml
--tag=latest
--dockerfile=Dockerfile
--compose-file=compose.yml 
```

`release` uses the same `--registry`, `--app-name`, and `--tag` for both `build` and `deploy` api calls internally.

This workflow builds and publishes `registry.example.com/team/my-app:latest`, `ssh`s with secret credenttials to `--ssh-target`, and does our application specific sets `APP_IMAGE=registry.example.com/team/my-app:latest` that our application-specific `compose.yml`. This essentially looks like this on the `--ssh-target`:

```bash
cd "$HOME/apps/my-app" && \
  APP_IMAGE=registry.example.com/team/my-app:latest \
  docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

Any failed registry, build, publish, `ssh`, pull, or Compose step stops the release.

## Staging And Production

The following pair is an extension of Quick Start:

| | Staging | Production |
| --- | --- | --- |
| Trigger | Branches other than `master`/`main` | `master` and `main` |
| Deployment host | The local runner VPS | An external `ssh` server |
| Runtime | `podman` | `docker` |
| Tag | Commit SHA | `latest` |
| Directory | `$HOME/staging/my-app/` | `$HOME/prod/my-app/` |


Provision `runtime.env` and `secrets/app_token` independently on each host. They configure the running application and never enter the Dagger build. See Compose documentation for [`env_file`](https://docs.docker.com/reference/compose-file/services/#env_file) and [`secrets`](https://docs.docker.com/reference/compose-file/secrets/).

In this example, the runner VPS also hosts staging. We chose the following filesystem to keep each application's runner. Daggerer does not require these directory names:

```text
/home/runner/
├── secrets-actions/
│   └── my-app/
│       ├── registry_password
│       ├── staging_ssh_key
│       ├── staging_known_hosts
│       ├── production_ssh_key
│       └── production_known_hosts
├── actions-runner/
│   └── my-app/              # GitHub Actions self-hosted runner for my-app
│       ├── run.sh
│       └── svc.sh
└── staging/
    └── my-app/
        ├── dev.compose.yml
        ├── .env
        └── secrets/
            └── app_token
```

```yaml
# dev.compose.yml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      - "5000:5000"
    env_file:
      - .env
    secrets:
      - app_token

secrets:
  app_token:
    file: ./secrets/app_token
```


The external production host contains only its independently provisioned `prod.compose.yml` and runtime secrets/data:

```text
/home/deploy/
└── apps/
    └── my-app/
        └── production/
            ├── prod.compose.yml
            ├── prod.env
            └── secrets/
                └── app_token
```

The staging public key is authorized for the runner user on the runner VPS. The production public key is authorized for the deployment user on the external VPS. Each `known_hosts` file contains the verified key for only its target.

### Staging: Local Runner VPS With Podman

The staging workflow executes on the runner VPS and connects back to that VPS with `ssh`. The `ssh` hop is intentional: `deploy` follows the same path for local staging and remote production.

```yaml
name: Deploy staging

on:
  push:
    branches-ignore: [master, main]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release to staging
        run: dagger -W github.com/ninesl/daggerer@master api call release
          --source=.
          --registry=registry.example.com/team
          --app-name=${{ github.event.repository.name }}
        # gives each staging build an immutable commit-specific image tag.
          --tag=${{ github.sha }}
          --registry-username=registry-user
          --registry-password=file://$HOME/secrets-actions/${{ github.event.repository.name }}/registry_password
        # points back to the runner VPS using an address reachable from Dagger's container network.
          --ssh-target=runner@runner.example.com
          --ssh-key=file://$HOME/secrets-actions/${{ github.event.repository.name }}/staging_ssh_key
          --known-hosts=file://$HOME/secrets-actions/${{ github.event.repository.name }}/staging_known_hosts
          --deploy-directory=staging/${{ github.event.repository.name }}
          --deploy-container-runtime=podman
```


### Production: External `ssh` Server With Docker

Production uses the same runner to build and publish, then connects with `ssh` to another server where Docker runs the application.

The staging and production workflows use separate target, key, and `known_hosts` files on the example VPS. They share the registry files because one `release` uses the same registry to publish and pull its image.

```yaml
name: Deploy production

on:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release to production
        run: dagger -W github.com/ninesl/daggerer@master api call release
          --source=.
          --registry=registry.example.com/team
          --app-name=${{ github.event.repository.name }}
          --tag=${{ github.sha }}
          --registry-username=registry-user
          --registry-password=file://$HOME/secrets-actions/${{ github.event.repository.name }}/registry_password
          --ssh-target=deploy@prod.example.com
          --ssh-key=file://$HOME/secrets-actions/${{ github.event.repository.name }}/production_ssh_key
          --known-hosts=file://$HOME/secrets-actions/${{ github.event.repository.name }}/production_known_hosts
          --deploy-directory=apps/${{ github.event.repository.name }}/production
          --deploy-container-runtime=docker
```

- `--source`, registry arguments, `--app-name`, and `--tag` build the same application image policy as staging.
- `--ssh-target` names the external production user and server.
- `--ssh-key` is a production-only private key whose public key is authorized on that server.
- `--known-hosts` verifies the external server instead of the local runner VPS.
- `--deploy-directory` selects the production Compose project under the production `ssh` user's home.
- `--deploy-container-runtime=docker` runs `docker login`, `docker pull`, and `docker compose` on production.

The workflow reads deployment inputs from the example VPS filesystem. The Compose projects, `runtime.env`, and application runtime secrets exist on their respective deployment hosts.

## Build Inputs

The examples above need no additional Dockerfile inputs. Daggerer also supports public build values, named build secrets, and a private dotenv secret.

| Argument | Purpose |
| --- | --- |
| `--build-env-file` | Public dotenv file converted to Dockerfile build arguments |
| `--build-values` | Public Dagger `EnvFile` supplied by an API client; overrides duplicate file values |
| `--build-secret-ids` / `--build-secrets` | BuildKit secret IDs paired with Dagger Secrets by position |
| `--build-secret-env` | Private dotenv mounted with the fixed BuildKit ID `build_env` |
| `--dockerfile` | Dockerfile path relative to `--source`; defaults to `Dockerfile` |

Build values are not secrets. Runtime values and secrets belong in the deployment host's Compose project, not in Docker build arguments.

### Shared Dockerfile Example

This Go example derives `GO_VERSION` from the application's `go.mod`, downloads a private GitHub module with a PAT, accepts another build secret named `BAR`, and bakes the public `FOO` value into the image:

```dockerfile
# syntax=docker/dockerfile:1.7

ARG GO_VERSION
FROM golang:${GO_VERSION}-alpine AS builder

RUN apk add --no-cache ca-certificates git
WORKDIR /app

COPY go.mod go.sum ./
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

Create a [fine-grained GitHub PAT](https://github.com/settings/personal-access-tokens/new) with read access to the private dependency repository. Store its raw value and the `BAR` value in separate runner-local files. Then add these arguments to either release call:

```bash
go_version="$(awk '$1 == "go" { print $2 }' go.mod)"
: "${go_version:?go.mod must contain a go directive}"
build_env_file="$(mktemp)"
trap 'rm -f "$build_env_file"' EXIT
printf 'GO_VERSION=%s\nAPP_PACKAGE=.\nFOO=hello\n' "$go_version" > "$build_env_file"

dagger -W github.com/ninesl/daggerer@master api call release \
  --source=. \
  --build-env-file="$build_env_file" \
  --build-secret-ids=github_token,BAR \
  --build-secrets=file://$HOME/secrets-actions/my-app/github_token,file://$HOME/secrets-actions/my-app/bar \
  <the registry and deployment arguments from the selected workflow>
```

The two secret lists must have equal lengths. `github_token` maps to the first file and `BAR` maps to the second. The IDs must match the Dockerfile's secret mounts. Add `.env`, `.git`, and any runner credential paths to `.dockerignore` when applicable.

### Private Dotenv Example

`--build-secret-env` mounts one private dotenv file under BuildKit ID `build_env`. A Dockerfile can consume it like this:

```dockerfile
RUN --mount=type=secret,id=build_env,required=true \
    set -eu; \
    . /run/secrets/build_env; \
    test -n "$BAR"
```

Pass it as a Secret:

```bash
--build-secret-env=file://$HOME/secrets-actions/my-app/private.env
```

The mount exists only for the `RUN` instruction that requests it. It is not copied into the resulting image.

## How Deployment Works

`deploy` creates a temporary Alpine `ssh` client inside Dagger and mounts the `ssh` key, `known_hosts`, and registry password as Dagger [`Secret`][dagger-secret] values. It then runs four remote steps:

```text
1. Verify <deploy-directory>/<compose-file> exists.
2. Run docker|podman login with the registry password on stdin.
3. Pull <registry>/<app-name>:<tag>.
4. Set APP_IMAGE and run docker|podman compose up.
```

The resulting path is:

```text
self-hosted runner
    -> Dagger Engine
        -> temporary `ssh` client container
            -> `ssh` deployment host
                -> Docker or Podman Compose
```

`--deploy-directory` is relative to the `ssh` user's home. `--compose-file` defaults to `compose.yml`. Daggerer passes `APP_IMAGE=<registry>/<app-name>:<tag>` to Compose; the examples consume it with `image: ${APP_IMAGE:?APP_IMAGE is required}`.

Strict `ssh` host-key checking uses the supplied `--known-hosts` Secret. The `ssh` target must be reachable from Dagger's container network even when it is the runner VPS itself.

## Caching

| Work | Behavior |
| --- | --- |
| `build-only` / [`DockerBuild`][dagger-build] | Reuses eligible cached work for unchanged inputs |
| Registry authentication | Runs on every build or release |
| [`Publish`][dagger-publish] | Publishes on every build or release; existing blobs may be reused |
| `ssh` checks, login, pull, and Compose | Run on every deploy or release |

This keeps expensive image builds cacheable while ensuring registry and deployment side effects happen for every requested release.

## Workspace Usage

Load Daggerer directly from Git:

```bash
dagger -W github.com/ninesl/daggerer@master api call release --help
```

The consumer needs no `dagger.toml` or `dagger.lock`. To install Daggerer in a consumer workspace instead:

```bash
dagger init
dagger module install github.com/ninesl/daggerer@master
dagger api call daggerer release --help
```

The installed module is namespaced as `daggerer`; the `-W` form selects Daggerer as the entrypoint and calls `release` directly.

[dagger-build]: https://docs.dagger.io/reference/api/directory#dockerBuild
[dagger-container]: https://docs.dagger.io/reference/api/container
[dagger-publish]: https://docs.dagger.io/reference/api/container#publish
[dagger-secret]: https://docs.dagger.io/reference/api/secret
