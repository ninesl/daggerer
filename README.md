**Daggerer** builds a `Dockerfile`, publishes its image to an OCI registry, and deploys it over `ssh` with `docker compose` or `podman compose` [using Dagger](https://dagger.io).

## Quick Reference

Daggerer exposes four API functions:

- `build-only` builds an input `Dockerfile` with [`Directory.DockerBuild`][dagger-build] and returns a cached [`Container`][dagger-container].
- `build` validates registry authentication, calls `build-only`, applies [`WithRegistryAuth`](https://docs.dagger.io/reference/api/container#withRegistryAuth), and [`Publish`](https://docs.dagger.io/reference/api/container#publish) publishes the image.
- `deploy` connects to the `ssh` target, pulls an existing image, and runs the file selected by `--compose-file` (`compose.yml` by default) with `docker compose` or `podman compose`.
- `release` calls `build` and then `deploy` with the same registry, application name, and tag simplifying the top level API.

```bash
# List the available Daggerer functions.
dagger -W github.com/ninesl/daggerer@master api functions
# Inspect each function's parameters and defaults.
dagger -W github.com/ninesl/daggerer@master api call build-only --help
dagger -W github.com/ninesl/daggerer@master api call build --help
dagger -W github.com/ninesl/daggerer@master api call deploy --help
dagger -W github.com/ninesl/daggerer@master api call release --help

# The command shape is always:
dagger -W github.com/ninesl/daggerer@master api call function --arguments
```

## Contents

The examples describe our sample application, `my-app`, using GitHub Actions on a self-hosted runner.

> Read through the documentation before adapting an example. The examples favor [grug-brained](https://grugbrain.dev/) development and [locality of behavior](https://htmx.org/essays/locality-of-behaviour/): each workflow passes its choices directly to the Daggerer API.

- [Quick Reference](#quick-reference)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Staging And Production](#staging-and-production)
- [Environment Input Contracts](#environment-input-contracts)
- [Build Without Deployment](#build-without-deployment)
- [GitHub Actions Secrets As Environment Inputs](#github-actions-secrets-as-environment-inputs)
- [How Deployment Works](#how-deployment-works)
- [Caching](#caching)
- [Workspace Usage](#workspace-usage)

## Prerequisites

`deploy` and `release` require either:

- [`docker`](https://docs.docker.com/engine/install/) and [`docker compose`](https://docs.docker.com/compose/install/linux/) 

or 

- [`podman`](https://podman.io/docs/installation) and [`podman compose`](https://docs.podman.io/en/latest/markdown/podman-compose.1.html) 

installed on the `ssh` target. This does NOT need to match Dagger's runtime, Dagger just needs to build and publish an OCR image.

Daggerer needs a `Dockerfile` to build the image.

`build-only` and `build` **DO NOT** require `docker` or `podman`, they use the self-hosted runner's Dagger Engine directly.

> [TODO: An external Dagger Engine target API is planned](link to dagger.io docs on how a dagger cli can connect to an external (like remote) dagger engine)

Create a self-hosted runner for your application's repository at `https://github.com/<owner>/<repo>/settings/actions/runners/new`. 

Install and start the runner according to GitHub's instructions. This location is your choice; Daggerer can be run anywhere that has access to the Dagger Engine.

From the directory where you installed the runner, the GitHub setup instructions use for persistence:

```bash
# Ran from the runner installation directory
# The current user (which will use Dagger CLI) is our runner's user
sudo ./svc.sh install "$USER"
# Start the runner
sudo ./svc.sh start
# Inspect the runner's status
sudo ./svc.sh status
```

[Install the Dagger CLI](https://docs.dagger.io/getting-started/install) for the same `$USER` as your runner service. 

Daggerer was built with Dagger `v1.0.0-beta.13`; run `dagger version` as the user to verify access to the CLI and Dagger Engine.

## Quick Start

This example uses a VPS that serves multiple roles:
- self-hosted GitHub Actions runner
- deploy the image via `docker compose`

> The environment is essentially an [AWS EC2](https://aws.amazon.com/ec2/) instance or similar VPS.

The following layout is **not** required by Daggerer, or really reccommended. My intention with this example is to highlight how inputs can be sourced from anywhere to be used by Daggerer.

These filesystem paths become Daggerer's API inputs.

```bash
# VPS filesystem
/home/runner/
├── secrets/
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
        └── compose.yml     # using Daggerer's default
```

```yaml
# /home/runner/apps/my-app/compose.yml on the `ssh` target (the runner VPS).
services:
  app:
    # docker compose -f compose.yml substitutes APP_IMAGE; missing/empty values fail.
    # APP_IMAGE is unqiue to our compose.yml, explicitly passed with --deploy-values below.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      - "5000:5000"
```

> In actual production you should use a reverse proxy like [`caddy`](https://caddyserver.com/docs/quick-starts/reverse-proxy) or [`nginx`](https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/)

```yaml
# my-repo/.github/workflows/release.yml
name: Release

# Release the main/master checkout on pushes to either branch.
on:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      # Our example application name matches the my-app paths on the VPS.
      APPLICATION_NAME: my-app
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy
        env:
          # Our application's deployment variable; this is the image reference we publish
          # Daggerer forwards these public values to the --ssh-target's --compose-file
          DEPLOY_VALUES: |
            APP_IMAGE=registry.example.com/team/${{ env.APPLICATION_NAME }}:latest
        run: |
          dagger -W github.com/ninesl/daggerer@master api call release 
            # Supply the step's current directory as the Dockerfile build context
            # . contains the repository files placed by actions/checkout
            --source=.

            # Registry host and image namespace
            --registry=registry.example.com/team

            # The image will be registry.example.com/team/my-app:latest
            --app-name="$APPLICATION_NAME"

            # Account used for publishing and remote registry login
            --registry-username=registry-user

            # load the runner-local file as a Dagger Secret
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password"

            # For this example the target is the same VPS as the self-hosted runner, using the runner user
            --ssh-target=runner@runner.example.com

            # `ssh` key whose public key is preauthorized for the ssh target user
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/ssh_key"

            # `known_hosts` file containing the verified host key for the ssh target
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/known_hosts"

            # This VPS uses docker login, docker pull, and docker compose -f compose.yml up
            # release currently only supports `docker` or `podman`
            --deploy-container-runtime=docker

            # Relative to the `ssh` target user's home, not the runner's checkout
            # Selects /home/runner/apps/my-app on this `ssh` target, /my-app contains compose.yml
            --deploy-directory="apps/$APPLICATION_NAME"

            # Forward our variables to `docker compose -f compose.yml up` on the ssh target
            # APP_IMAGE is used for our unique `compose.yml` specific to my-app
            # This variable only exists here because we pass it; Daggerer does not require it
            --deploy-values="$DEPLOY_VALUES"
```

- Omitted `--dockerfile`: uses default `Dockerfile` at the root of `--source`
- Omitted `--tag`: uses default `latest`
- Omitted `--compose-file`: uses default `compose.yml`

```bash
# What the final deployment looks like on --ssh-target
# $HOME belongs to the --ssh-target user, /apps/my-app has the compose.yml
# Our workflow explicitly passed APP_IMAGE required for /my-app/compose.yml through --deploy-values
# Recreate the service in the background and remove services no longer in compose.yml.
cd "$HOME/apps/my-app" && \
  APP_IMAGE=registry.example.com/team/my-app:latest \
  docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

Any failed registry, build, publish, `ssh`, pull, or `docker compose -f compose.yml up` step stops this release.

## Staging And Production

This example uses two `.yml` workflows to call the same Daggerer `release` function.

- Both use shared public build defaults
- a private dependency PAT
- and a private runtime `DATABASE_URL`
- Staging overrides the default `APP_MODE=PRODUCTION` with `APP_MODE=STAGING` and independently gets `STAGING_SECRET_KEY`
- Production keeps the default `APP_MODE` and independently gets `PROD_SECRET_TOKEN`.

Our example application uses `APP_MODE` internally to decide which environment-specific runtime secrets are required.

### Shared Application Files

```dockerfile
# syntax=docker/dockerfile:1.7
# myapp.Dockerfile is in our application's checkout, selected by both Daggerer release workflows.

FROM golang:alpine AS builder

RUN apk add --no-cache ca-certificates git
WORKDIR /app
COPY go.mod go.sum ./

ARG GOPRIVATE=github.com/your-org/*
ENV GOPRIVATE=$GOPRIVATE

# Daggerer supplies the merged private build values through this temporary mount.
# Reads GITHUB_PAT for private dependency downloads.
# The secret mount is not copied into the image.
RUN --mount=type=secret,id=build_env,required=true \
    set -eu; \
    . /run/secrets/build_env; \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.https://x-access-token:${GITHUB_PAT}@github.com/.insteadOf" \
    GIT_CONFIG_VALUE_0="https://github.com/" \
    go mod download

COPY . .
# Public package selection from build.env or --build-values.
ARG APP_PACKAGE=.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -o /app/server "$APP_PACKAGE"

FROM alpine:3.24.1

ARG APP_MODE=PRODUCTION # Staging overrides it with STAGING; production keeps PRODUCTION from build.env.
ENV APP_MODE=$APP_MODE

RUN apk add --no-cache ca-certificates
COPY --from=builder /app/server /usr/local/bin/server
ENTRYPOINT ["/usr/local/bin/server"]
```

```dotenv
# build.env in our application's checkout; public build defaults for both workflows.
# Our application's main Go package.
APP_PACKAGE=.
# Public application mode; staging overrides it, production keeps PRODUCTION.
APP_MODE=PRODUCTION
```

```dotenv
# $HOME/secrets/my-app/secretbuild.env on the runner VPS; shared by both builds.
# --build-secret-env-file parses this private dotenv and mounts the merged result at
# /run/secrets/build_env during the build. It never becomes a Docker build argument.
# Example PAT with read access to our private dependency repository; not a runtime token.
GITHUB_PAT=example-token
```

```dotenv
# $HOME/secrets/my-app/secretdeploy.env on the runner VPS.
# Shared private runtime value for both environments. Daggerer forwards it to the
# remote Compose process over SSH without writing a file on the deployment target.
DATABASE_URL=postgres://app:example-password@db.internal:5432/my_app
```

```dockerignore
# .dockerignore in our application's checkout: exclude metadata and local secrets.
.git
.env
secretbuild.env
secretdeploy.env
secrets/
# build.env remains available as public build configuration.
```

```dotenv
# deploy.env in our application's checkout on the runner VPS.
# --deploy-env-file forwards public deployment process variables to docker/podman compose.
# Our application chooses this standard Compose setting; Daggerer treats every key generically.
COMPOSE_PARALLEL_LIMIT=4
# Each workflow explicitly supplies APP_IMAGE through --deploy-values, not through Daggerer defaults.
# Application runtime variables and secrets are selected separately by dev.compose.yml/prod.compose.yml.
```

For our application, the runner VPS also serves as the staging `ssh` target. This is our chosen filesystem layout:

```text
# Our chosen runner VPS layout: credential files are read by the runner service user.
/home/runner/
├── secrets/
│   └── my-app/
│       ├── registry_password
│       ├── secretbuild.env   # Shared private build file containing GITHUB_PAT
│       ├── secretdeploy.env  # Shared private runtime DATABASE_URL
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
        └── dev.compose.yml
```

### Staging: Runner VPS As The `ssh` Target With Podman

The staging workflow executes on the runner VPS and connects to the staging `ssh` target on that same runner VPS. `deploy` uses the same `ssh` connection flow for both staging and production.

```yaml
# my-repo/.github/workflows/staging.yml
# GitHub selects this workflow by branch; Daggerer receives the explicit inputs below.
name: Deploy staging

# Branch pushes update the same staging deployment using their exact commit tags.
on:
  push:
    # Branch pushes except main/master invoke this staging release.
    branches-ignore: [master, main]

permissions:
  contents: read

jobs:
  release:
    # Execute this workflow on the runner VPS; --ssh-target selects where deployment runs.
    runs-on: [self-hosted, linux, x64]
    env:
      # Same application name as the my-app paths on the VPS.
      APPLICATION_NAME: my-app
    steps:
      # Place the triggering commit's repository files in the runner workspace.
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release to staging
        env:
          # Public dotenv text: override build.env's APP_MODE=PRODUCTION for this image.
          BUILD_VALUES: |
            APP_MODE=STAGING
          # Our application's deployment variable, explicitly matching the image built below.
          # Use the full commit hash here as well as in --tag; neither input sets the other.
          DEPLOY_VALUES: |
            APP_IMAGE=registry.example.com/team/${{ env.APPLICATION_NAME }}:${{ github.sha }}
          # Private dotenv text from a GitHub Secret containing STAGING_SECRET_KEY=...
          # Our STAGING application logic requires this key; production uses another key.
          DEPLOY_SECRET_VALUES: ${{ secrets.STAGING_DEPLOY_ENV }}
        run: |
          dagger -W github.com/ninesl/daggerer@master api call release
            # Supply the step's current directory as the Docker build context.
            # Here it contains the branch commit's files placed by actions/checkout.
            --source=.

            # Select myapp.Dockerfile relative to --source.
            # Daggerer builds the image using this file's instructions; production selects it too.
            --dockerfile=myapp.Dockerfile

            # Public build defaults from the application checkout.
            --build-env-file=build.env

            # APP_MODE=STAGING overrides build.env's APP_MODE=PRODUCTION.
            --build-values="$BUILD_VALUES"

            # Shared private build dotenv containing GITHUB_PAT.
            --build-secret-env-file="file://$HOME/secrets/$APPLICATION_NAME/secretbuild.env"

            # Publish and pull from this registry and namespace.
            --registry=registry.example.com/team

            # Our application name becomes the image name: my-app.
            --app-name="$APPLICATION_NAME"

            # Use this exact full commit hash; no shortening or prefix.
            # This controls the image published and pulled, not variables in dev.compose.yml.
            --tag=${{ github.sha }}

            # Registry account used for publish and deployment.
            --registry-username=registry-user

            # Shared registry password file on the runner VPS.
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password"

            # Staging `ssh` target on the runner VPS; must be reachable from Dagger.
            --ssh-target=runner@runner.example.com

            # Staging `ssh` key whose public key is authorized for the staging `ssh` target user.
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/staging_ssh_key"

            # `known_hosts` file containing the verified host key for the staging `ssh` target.
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/staging_known_hosts"

            # Run podman login, podman pull, and podman compose -f dev.compose.yml up.
            --deploy-container-runtime=podman

            # Relative to the staging `ssh` target user's home, not the runner's checkout.
            # Selects /home/runner/staging/my-app on the staging `ssh` target.
            # All branch releases update this directory; tags do not create per-branch deployments.
            --deploy-directory="staging/$APPLICATION_NAME"

            # Select dev.compose.yml inside --deploy-directory.
            # It explicitly forwards the staging runtime variables shown below.
            --compose-file=dev.compose.yml

            # Public process env defaults from our checkout's deploy.env; parsed by Dagger.
            # This is a runner-side input for the remote Compose process.
            --deploy-env-file=deploy.env

            # Forward our APP_IMAGE to podman compose -f dev.compose.yml up.
            # Overrides matching deploy.env entries; no application variable names are automatic.
            --deploy-values="$DEPLOY_VALUES"

            # Shared private runtime dotenv containing DATABASE_URL.
            --deploy-secret-env-file="file://$HOME/secrets/$APPLICATION_NAME/secretdeploy.env"

            # Add STAGING_SECRET_KEY from the workflow's protected environment.
            # env:// keeps the multiline dotenv input typed as a Dagger Secret.
            --deploy-secret-values=env://DEPLOY_SECRET_VALUES

```

Defaults and omissions:

- `--dockerfile`: uses `Dockerfile` instead of `myapp.Dockerfile`.
- `--build-env-file`: no public build file is read; explicit build values still apply.
- `--build-values`: no overrides; the public build file's values still apply.
- `--build-secret-env-file`: no private build base is read; our Dockerfile requires `GITHUB_PAT`.
- Omitted `--build-secret-values`: no private build overrides are merged; the PAT file still applies.
- `--tag`: uses `latest` instead of the commit hash.
- `--compose-file`: uses `compose.yml` instead of `dev.compose.yml`.
- `--deploy-env-file`: no public deployment file is read; explicit deployment values still apply.
- `--deploy-values`: no overrides; the deployment file's values still apply. Our example would need another source for `APP_IMAGE`.
- `--deploy-secret-env-file`: no private deployment base is read, so `DATABASE_URL` would be absent.
- `--deploy-secret-values`: no private deployment overrides are merged, so `STAGING_SECRET_KEY` would be absent.

Daggerer does not auto-discover any of these inputs. `dev.compose.yml` explicitly chooses which forwarded values enter the running application.

```yaml
# /home/runner/staging/my-app/dev.compose.yml on the staging `ssh` target.
services:
  app:
    # Public deployment value selects the image; it is not an application environment variable.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    # Restart unless explicitly stopped.
    restart: unless-stopped
    ports:
      # Publish staging port 5000 to our application's listening port 5000.
      - "5000:5000"
    environment:
      # Daggerer forwards these private values only to this Compose invocation.
      # Compose places them in the running container environment, not the image.
      DATABASE_URL: ${DATABASE_URL:?DATABASE_URL is required}
      STAGING_SECRET_KEY: ${STAGING_SECRET_KEY:?STAGING_SECRET_KEY is required}
```

### Production: Production VPS As The `ssh` Target With Docker

The production workflow executes on the runner VPS to build and publish, then connects to the production `ssh` target on the production VPS, where Docker runs the application.

Both workflows run on the runner VPS and share its registry password, private build file, and private `DATABASE_URL` file. Their `ssh` targets have separate SSH credentials, and each workflow supplies its own private runtime dotenv override. Those choices are explicit inputs to our application's workflows.

```yaml
# my-repo/.github/workflows/production.yml
# GitHub selects this workflow by branch; Daggerer receives the explicit inputs below.
name: Deploy production

# Main/master pushes update the production deployment using latest.
on:
  push:
    # Only main/master branch pushes invoke this production release.
    branches: [master, main]

permissions:
  contents: read

jobs:
  release:
    # Build on the same runner VPS; --ssh-target deploys to the separate production VPS.
    runs-on: [self-hosted, linux, x64]
    env:
      # Same application name as the my-app paths on both VPSs.
      APPLICATION_NAME: my-app
    steps:
      # Place the triggering main/master commit's repository files in the runner workspace.
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release to production
        env:
          # Our application's deployment variable, explicitly matching production's latest tag.
          # Setting --tag does not create or change this variable.
          DEPLOY_VALUES: |
            APP_IMAGE=registry.example.com/team/${{ env.APPLICATION_NAME }}:latest
          # Private dotenv text from a GitHub Secret containing PROD_SECRET_TOKEN=...
          # Our PRODUCTION application logic requires this token; staging uses another key.
          DEPLOY_SECRET_VALUES: ${{ secrets.PRODUCTION_DEPLOY_ENV }}
        run: |
          dagger -W github.com/ninesl/daggerer@master api call release
            # Supply the step's current directory as the Docker build context.
            # Here it contains the main/master commit's files placed by actions/checkout.
            --source=.

            # Select myapp.Dockerfile relative to --source.
            # Daggerer builds the image using this file's instructions; staging selects it too.
            --dockerfile=myapp.Dockerfile

            # Public build values, including APP_MODE=PRODUCTION; no override is passed.
            --build-env-file=build.env

            # The same private build dotenv used by staging, containing GITHUB_PAT.
            --build-secret-env-file="file://$HOME/secrets/$APPLICATION_NAME/secretbuild.env"

            # Same registry and namespace used by staging.
            --registry=registry.example.com/team

            # Produces registry.example.com/team/my-app:latest.
            --app-name="$APPLICATION_NAME"

            # Publish and deploy latest.
            # This controls the image published and pulled, not variables in prod.compose.yml.
            --tag=latest

            # Registry account used for publish and deployment.
            --registry-username=registry-user

            # Registry password file on the runner VPS, used to publish and log in on the production `ssh` target.
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password"

            # Production `ssh` target on the production VPS, using the deploy user.
            --ssh-target=deploy@prod.example.com

            # Production `ssh` key whose public key is authorized for the production `ssh` target user.
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/production_ssh_key"

            # `known_hosts` file containing the verified host key for the production `ssh` target.
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/production_known_hosts"

            # Run docker login, docker pull, and docker compose -f prod.compose.yml up.
            --deploy-container-runtime=docker

            # Relative to the production `ssh` target user's home, not the runner's home.
            # Selects /home/deploy/prod/my-app on the production `ssh` target.
            --deploy-directory="prod/$APPLICATION_NAME"

            # Select prod.compose.yml inside --deploy-directory.
            # It explicitly forwards the production runtime variables shown below.
            --compose-file=prod.compose.yml

            # Public process env defaults from our checkout's deploy.env; parsed by Dagger.
            # This is a runner-side input for the remote Compose process.
            --deploy-env-file=deploy.env

            # Forward our APP_IMAGE to docker compose -f prod.compose.yml up.
            # Overrides matching deploy.env entries; no application variable names are automatic.
            --deploy-values="$DEPLOY_VALUES"

            # Shared private runtime dotenv containing DATABASE_URL.
            --deploy-secret-env-file="file://$HOME/secrets/$APPLICATION_NAME/secretdeploy.env"

            # Add PROD_SECRET_TOKEN from the workflow's protected environment.
            --deploy-secret-values=env://DEPLOY_SECRET_VALUES

```

Defaults and omissions:

- `--dockerfile`: if omitted, uses `Dockerfile` instead of `myapp.Dockerfile`.
- `--build-env-file`: if omitted, no public build file is read.
- Omitted `--build-values`: no overrides; production keeps `APP_MODE=PRODUCTION` from `build.env`.
- `--build-secret-env-file`: if omitted, no private build base is read; our Dockerfile requires `GITHUB_PAT`.
- Omitted `--build-secret-values`: no private build overrides are merged; the PAT file still applies.
- `--tag`: if omitted, uses `latest`, the same tag selected here.
- `--compose-file`: uses `compose.yml` instead of `prod.compose.yml`.
- `--deploy-env-file`: no public deployment file is read; explicit deployment values still apply.
- `--deploy-values`: no overrides; the deployment file's values still apply. Our example would need another source for `APP_IMAGE`.
- `--deploy-secret-env-file`: no private deployment base is read, so `DATABASE_URL` would be absent.
- `--deploy-secret-values`: no private deployment overrides are merged, so `PROD_SECRET_TOKEN` would be absent.

Daggerer does not auto-discover any of these inputs. `prod.compose.yml` explicitly chooses which forwarded values enter the running application.

```text
# Production `ssh` target: runtime files are separate from the runner VPS's build credentials.
/home/deploy/
└── prod/
    └── my-app/
        └── prod.compose.yml
```

```yaml
# /home/deploy/prod/my-app/prod.compose.yml on the production `ssh` target.
services:
  app:
    # Public deployment value selects the image; it is not an application environment variable.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    # Restart unless explicitly stopped.
    restart: unless-stopped
    ports:
      # Publish production port 5000 to our application's listening port 5000.
      - "5000:5000"
    environment:
      # Daggerer forwards these private values only to this Compose invocation.
      # Compose places them in the running container environment, not the image.
      DATABASE_URL: ${DATABASE_URL:?DATABASE_URL is required}
      PROD_SECRET_TOKEN: ${PROD_SECRET_TOKEN:?PROD_SECRET_TOKEN is required}
```

## Environment Input Contracts

### Explicit Inputs, Not Workspace Discovery

Daggerer does not automatically read your workspace's `.env`, import the runner's environment, or create a `.env` in your checkout. Choose each input explicitly:

- `--build-env-file`: a caller-selected public dotenv file; this can be your workspace's `.env` if its contents are suitable for public build arguments.
- `--build-values`: public dotenv text, supplied directly or through a runner environment variable such as `BUILD_VALUES`.
- `--build-secret-env-file`: a caller-selected private dotenv Secret used as the private build base.
- `--build-secret-values`: private dotenv Secret text that overrides the private build base.
- `--deploy-env-file`: a caller-selected public dotenv file for the remote Compose process.
- `--deploy-values`: public dotenv text, supplied directly or through a runner environment variable such as `DEPLOY_VALUES`.
- `--deploy-secret-env-file`: a caller-selected private dotenv Secret used as the private deployment base.
- `--deploy-secret-values`: private dotenv Secret text that overrides the private deployment base.

The workflow's `BUILD_VALUES`, `DEPLOY_VALUES`, and `DEPLOY_SECRET_VALUES` are ordinary runner environment variables containing multiline dotenv text—like in-memory files. Their names are our workflow's choice. Daggerer receives them only through an explicit API argument. Defining one under GitHub's `env:` does not automatically inject it into an image or running container.

### Merge And Collision Rules

Build and deployment each have an independent public merge and private merge:

1. Read the selected base dotenv input, if supplied.
2. Read the explicit dotenv input, if supplied.
3. Merge by variable name; explicit input wins, including an explicitly empty value.
4. Keep base-only keys. With neither input supplied, that category is empty.
5. Reject any variable name present in both the merged public and merged private categories.

Public files and text use Dagger's dotenv parser. Private inputs use the same dotenv syntax but are parsed on the private path because Dagger's `EnvFile` API accepts a public `File`, not a `Secret`. Table-driven tests cover dotenv syntax, base/override permutations, explicit empty overrides, and same-value or different-value public/private collisions.

A collision fails before the build or deployment operation and names only the conflicting variable. Daggerer never compares or prints the values in the error. Private values do not silently overwrite public values because that could accidentally downgrade a secret into a public build argument or command value.

In our examples:

- **Staging build:** `build.env` supplies `APP_MODE=PRODUCTION`; `BUILD_VALUES` overrides it with `APP_MODE=STAGING`.
- **Production build:** no public override is passed, so `APP_MODE=PRODUCTION` remains.
- **Both builds:** `secretbuild.env` supplies the private `GITHUB_PAT`; no private build override is needed.
- **Both deployments:** `deploy.env` supplies the public `COMPOSE_PARALLEL_LIMIT`; `DEPLOY_VALUES` adds the public `APP_IMAGE`; `secretdeploy.env` supplies the private `DATABASE_URL`.
- **Staging deployment:** private inline values add `STAGING_SECRET_KEY`.
- **Production deployment:** private inline values add `PROD_SECRET_TOKEN`.

Our Dockerfile explicitly turns `APP_MODE` into image environment configuration with `ENV APP_MODE=$APP_MODE`. Our application uses that mode to require `STAGING_SECRET_KEY` or `PROD_SECRET_TOKEN`. These names and that business logic belong to the sample application; Daggerer treats every variable generically.

### Secret Sources And Mounts

- `file://`: Dagger loads a Secret from a caller-local file.
- `env://`: Dagger loads a Secret from a caller environment variable, including one populated from GitHub Actions Secrets.

These are interchangeable sources for any Secret parameter. They do not change merge precedence or turn the input into a public value. Putting a GitHub secret into a public values argument bypasses that separation; use a Secret parameter for private contents.

Private values stay on phase-specific paths:

- **Build secrets:** Daggerer merges the private build inputs and creates one BuildKit Secret. Our Dockerfile temporarily mounts it at `/run/secrets/build_env` during `go mod download`. It is never passed as a Docker build argument.
- **Deployment secrets:** Daggerer merges the private deployment inputs and streams a quoted export script over SSH stdin for the selected Compose invocation. Secret values are not command-line arguments, and Daggerer does not write a dotenv file in the workspace or on the target.
- **Deployment credentials:** registry passwords and SSH credentials are used by authentication helpers. They are not application build arguments or public deployment values.

Daggerer does not materialize Secrets into your checkout's `.env`. Its build-secret mount is not automatically saved into an image layer, and deployment secrets never enter the image build. The sample Compose files deliberately place selected deployment secrets into the running container environment. Users with sufficient container-runtime access can inspect runtime environment variables, and application or Dockerfile logic can disclose values; Daggerer cannot guarantee what arbitrary application, Compose, or Dockerfile instructions do with an input.

### Target-Side Runtime Files

Compose has its own environment handling. Daggerer supplies the merged deployment values to the remote Compose process; each Compose file explicitly chooses which values become runtime application environment variables. The values are available for that deployment command and the resulting container configuration without Daggerer writing a remote dotenv file.

Selecting a dotenv file as an API input does not control whether files already inside the build context are copied by your Dockerfile. Keep secret files outside the checkout where practical and exclude local secret files with `.dockerignore` independently.

## Build Without Deployment

The same inputs also work with `build-only`, without publishing or deploying:

```yaml
# my-repo/.github/workflows/build.yml
# Build step excerpt, after checkout in a self-hosted runner job.
- name: Build our sample application
  env:
    # Hardcoded public overrides for our application's staging build.
    # Keep credentials in the private build file, never in these public values.
    # GitHub Actions uses env:, not env_file:; --build-values passes these explicitly.
    BUILD_VALUES: |
      APP_MODE=STAGING
      APP_PACKAGE=.
  # build-only has no deployment or registry inputs because it only builds the image.
  run: |
    dagger -W github.com/ninesl/daggerer@master api call build-only
      # Supply the step's current directory as the Docker build context.
      # This example assumes actions/checkout has placed our application's files there.
      --source=.

      # Select our application's myapp.Dockerfile relative to --source, as both release workflows do.
      --dockerfile=myapp.Dockerfile

      # Dagger parses our caller-selected public file into Dockerfile build arguments.
      --build-env-file=build.env

      # Explicit public overrides; APP_MODE overrides build.env's PRODUCTION.
      --build-values="$BUILD_VALUES"

      # Our private build file containing GITHUB_PAT; its filename and path are our choice.
      --build-secret-env-file=file://$HOME/secrets/my-app/secretbuild.env

```

Defaults and omissions:

- `--dockerfile`: uses `Dockerfile` instead of `myapp.Dockerfile`.
- `--build-env-file`: no public build file is read; explicit build values still apply.
- `--build-values`: no overrides; the public build file's values still apply.
- `--build-secret-env-file`: no private build base is read; our Dockerfile requires `GITHUB_PAT`.
- Omitted `--build-secret-values`: no private build overrides are merged.

## GitHub Actions Secrets As Environment Inputs

Alternative credential inputs for our release workflows:

```yaml
# my-repo/.github/workflows/staging.yml
# Credential excerpt: replace the file:// arguments in either annotated release workflow.
# Other build/deployment arguments remain the ones chosen by that workflow.
- name: Release our sample application using GitHub Actions Secrets
  env:
    # Registry password configured in GitHub Actions Secrets.
    REGISTRY_PASSWORD: ${{ secrets.REGISTRY_PASSWORD }}
    # Staging `ssh` key configured in GitHub; production selects its production key instead.
    SSH_KEY: ${{ secrets.STAGING_SSH_KEY }}
    # Verified host key for the staging `ssh` target; production selects its own value.
    KNOWN_HOSTS: ${{ secrets.STAGING_KNOWN_HOSTS }}
    # Entire private build dotenv, including GITHUB_PAT, configured in GitHub.
    BUILD_PRIVATE_ENV: ${{ secrets.BUILD_PRIVATE_ENV }}
    # Entire shared private deployment dotenv, including DATABASE_URL.
    DEPLOY_PRIVATE_ENV: ${{ secrets.DEPLOY_PRIVATE_ENV }}
  run: |
    dagger -W github.com/ninesl/daggerer@master api call release
      # Use the GitHub Secret as the private build base instead of file://.
      --build-secret-env-file=env://BUILD_PRIVATE_ENV

      # Load the registry password from the step environment as a Dagger Secret.
      --registry-password=env://REGISTRY_PASSWORD

      # env:// loads the step's staging `ssh` key as a Dagger Secret.
      --ssh-key=env://SSH_KEY

      # Strict host-key verification for the staging `ssh` target.
      --known-hosts=env://KNOWN_HOSTS

      # Use the GitHub Secret as the private deployment base instead of file://.
      --deploy-secret-env-file=env://DEPLOY_PRIVATE_ENV

```

Arguments absent from this credential excerpt remain as shown in the full staging or production workflow; their defaults and optional-input behavior are described below those workflows.

## How Deployment Works

`deploy` uses the supplied credentials to execute these commands on the `ssh` target:

```text
# Remote deployment operations; runtime and filename come from API inputs.
1. Verify <deploy-directory>/<compose-file> exists.
2. Run docker|podman login with the registry password on stdin.
3. Pull <registry>/<app-name>:<tag>.
4. Stream private deployment values over SSH stdin, combine them with public values only
   in the Compose process environment, and run <runtime> compose -f <compose-file> up.
```

The resulting path is:

```text
# Execution path for our application: build/publish in Dagger, runtime commands on the `ssh` target.
self-hosted runner
    -> Dagger Engine
        -> `ssh` target
            -> podman compose -f dev.compose.yml up (staging)
               or docker compose -f prod.compose.yml up (production)
```

Strict `ssh` host-key checking uses the supplied `--known-hosts` Secret. The `ssh` target must be reachable from Dagger's container network even when it is the runner VPS itself.

## Caching

- `build-only` uses [`DockerBuild`][dagger-build] to reuse eligible cached work for unchanged inputs.
- `build` and `release` repeat registry authentication and [`Publish`][dagger-publish]; existing image blobs may be reused.
- `deploy` and `release` repeat the remote file check, registry login, image pull, and selected `docker compose` or `podman compose` command.

## Workspace Usage

Load Daggerer directly from Git:

```bash
# Load Daggerer from Git and inspect release parameters without installing a workspace module.
dagger -W github.com/ninesl/daggerer@master api call release --help
```

The consumer needs no `dagger.toml` or `dagger.lock`. To install Daggerer in a consumer workspace instead:

```bash
# Initialize a consumer workspace.
dagger init
# Install Daggerer under the daggerer namespace.
dagger module install github.com/ninesl/daggerer@master
# Call the installed module; -W calls the selected workspace's entrypoint directly instead.
dagger api call daggerer release --help
```

[dagger-build]: https://docs.dagger.io/reference/api/directory#dockerBuild
[dagger-container]: https://docs.dagger.io/reference/api/container
[dagger-publish]: https://docs.dagger.io/reference/api/container#publish
[dagger-secret]: https://docs.dagger.io/reference/api/secret
