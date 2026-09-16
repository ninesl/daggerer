**Daggerer** builds a `Dockerfile`, publishes its image to an OCI registry, and deploys it over `ssh` with `docker compose` or `podman compose` [using Dagger](https://dagger.io).

## Quick Reference

Daggerer exposes four API functions:

- `build-only` builds the file selected by `--dockerfile` (`Dockerfile` by default) with [`Directory.DockerBuild`][dagger-build] and returns a cached [`Container`][dagger-container].
- `build` validates registry authentication, calls `build-only`, applies [`WithRegistryAuth`](https://docs.dagger.io/reference/api/container#withRegistryAuth), and [`Publish`](https://docs.dagger.io/reference/api/container#publish) publishes the image.
- `deploy` connects to `--ssh-target`, pulls an existing image, and uses the runtime selected by `--deploy-container-runtime` to run `--compose-file` (`compose.yml` by default).
- `release` calls `build` and then `deploy` with the same `--registry`, `--app-name`, and `--tag`, simplifying the top-level API.

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
- [Secrets](SECRETS.md)

## Prerequisites

`deploy` and `release` require either:

- [`docker`](https://docs.docker.com/engine/install/) and [`docker compose`](https://docs.docker.com/compose/install/linux/) 

or 

- [`podman`](https://podman.io/docs/installation) and [`podman compose`](https://docs.podman.io/en/latest/markdown/podman-compose.1.html) 

installed on `--ssh-target`. This does NOT need to match Dagger's runtime; Dagger only needs to build and publish an OCI image.

Daggerer needs the file selected by `--dockerfile` (`Dockerfile` by default) to build the image.

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

The following layout is **not** required by Daggerer, or really recommended. My intention with this example is to highlight how inputs can be sourced from anywhere to be used by Daggerer.

These filesystem paths become Daggerer's API inputs.

See [Credentials](SECRETS.md#credentials) for the registry password, SSH key, and known-hosts files used below.

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
# /home/runner/apps/my-app/compose.yml on `--ssh-target` (the runner VPS).
services:
  app:
    # docker compose runs the file selected by --compose-file and substitutes APP_IMAGE;
    # missing/empty values fail. APP_IMAGE is unique to our compose.yml and is explicitly
    # passed with --deploy-values below.
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
      # APPLICATION_NAME supplies --app-name and the my-app path segments below.
      APPLICATION_NAME: my-app
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy
        env:
          # Our application's --deploy-values input; this is the image reference we publish.
          # Daggerer forwards these public values to --compose-file on --ssh-target.
          DEPLOY_VALUES: |
            APP_IMAGE=registry.example.com/team/${{ env.APPLICATION_NAME }}:latest
        run: dagger -W github.com/ninesl/daggerer@master api call release 
            # Supply the step's current directory as the --source build context.
            # . contains the repository files placed by actions/checkout
            --source=.

            # --registry supplies the registry host and image namespace.
            --registry=registry.example.com/team

            # --registry, --app-name, and --tag form registry.example.com/team/my-app:latest.
            --app-name="$APPLICATION_NAME"

            # --registry-username is used for publishing and remote registry login.
            --registry-username=registry-user

            # --registry-password loads the runner-local file as a Dagger Secret.
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password"

            # --ssh-target uses the runner user on the same VPS as the self-hosted runner.
            --ssh-target=runner@runner.example.com

            # --ssh-key loads the key preauthorized for the --ssh-target user.
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/ssh_key"

            # --known-hosts loads the verified host key for --ssh-target.
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/known_hosts"

            # --deploy-container-runtime selects docker login, pull, and compose.
            # release currently supports only `docker` or `podman`.
            --deploy-container-runtime=docker

            # Relative to the --ssh-target user's home, not the runner's checkout.
            # --deploy-directory selects /home/runner/apps/my-app on this --ssh-target.
            --deploy-directory="apps/$APPLICATION_NAME"

            # --deploy-values forwards our variables to --compose-file on --ssh-target.
            # APP_IMAGE is used for our unique `compose.yml` specific to my-app
            # This variable only exists here because we pass it; Daggerer does not require it
            --deploy-values="$DEPLOY_VALUES"
```

- Omitted `--dockerfile`: uses default `Dockerfile` at the root of `--source`.
- Omitted `--tag`: uses default `latest`.
- Omitted `--compose-file`: uses default `compose.yml`.

```bash
# What the final deployment looks like on --ssh-target
# $HOME belongs to the --ssh-target user; --deploy-directory selects /apps/my-app,
# which contains the file selected by --compose-file.
# Our workflow passes the APP_IMAGE required by --compose-file through --deploy-values.
# Recreate the service in the background and remove services no longer in --compose-file.
cd "$HOME/apps/my-app" && \
  APP_IMAGE=registry.example.com/team/my-app:latest \
  docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

Any failed registry, build, publish, `ssh`, pull, or `docker compose -f compose.yml up` step stops this release.

## Staging And Production

This example calls the same Daggerer `release` function from two workflows:

- Staging builds on the self-hosted runner and deploys back to that VPS with Podman.
- Production builds on the same runner and deploys to a separate VPS with Docker.

Unlike Quick Start, neither target uses the default `compose.yml`. Our application exposes a different host port in each environment and may apply different resource limits, so each workflow explicitly selects its target's file with `--compose-file`.

### Filesystem

```text
# Self-hosted runner and staging --ssh-target
/home/runner/
├── secrets/my-app/
│   ├── registry_password
│   ├── github_pat
│   ├── staging.env
│   ├── production.env
│   ├── staging_ssh_key
│   ├── staging_known_hosts
│   ├── production_ssh_key
│   └── production_known_hosts
├── actions-runner/my-app/
└── staging/my-app/
    └── staging.compose.yml

# Production --ssh-target
/home/deploy/
└── apps/my-app/
    └── production.compose.yml

# Application repository checked out by GitHub Actions
my-repo/
├── .github/workflows/
│   ├── staging.yml
│   └── production.yml
├── Dockerfile
└── .dockerignore
```

The files under `/home/runner/secrets/my-app` are caller-local inputs, not files in the application checkout. [Secrets](SECRETS.md) explains what each file contains and how Daggerer handles it.

### Staging

Branches other than `main` and `master` publish an immutable commit tag and deploy it to the staging `--ssh-target`.

```yaml
# /home/runner/staging/my-app/staging.compose.yml on the staging --ssh-target.
services:
  app:
    # --deploy-values supplies the exact commit image published by this workflow.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      # Keep staging available on a separate host port from our production convention.
      - "5001:5000"
    environment:
      # --deploy-secret-env-file supplies these values only to the Compose process.
      DATABASE_URL: ${DATABASE_URL:?DATABASE_URL is required}
      APP_SECRET: ${APP_SECRET:?APP_SECRET is required}
    # Optional staging limits; enable these only when the target's Compose implementation
    # supports and enforces deploy resources.
    # deploy:
    #   resources:
    #     limits:
    #       cpus: "0.50"
    #       memory: 256M
```

The workflow sets `--compose-file=staging.compose.yml` because Daggerer otherwise looks for the default `compose.yml` inside `--deploy-directory`.

```yaml
# .github/workflows/staging.yml
name: Deploy staging

on:
  push:
    branches-ignore: [main, master]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      APPLICATION_NAME: my-app
      DEPLOY_IMAGE: registry.example.com/team/my-app:${{ github.sha }}
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy staging
        run: |
          # with-build-secret maps our runner-local PAT to the Dockerfile's github_pat ID.
          # release builds this checkout, publishes the commit tag, and deploys it with
          # the target-side staging.compose.yml selected by --compose-file.
          dagger -W github.com/ninesl/daggerer@master api call \
            with-build-secret \
            --id=github_pat \
            --secret="file://$HOME/secrets/$APPLICATION_NAME/github_pat" \
            release \
            --source=. \
            --registry=registry.example.com/team \
            --app-name="$APPLICATION_NAME" \
            --tag=${{ github.sha }} \
            --registry-username=registry-user \
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password" \
            --ssh-target=runner@runner.example.com \
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/staging_ssh_key" \
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/staging_known_hosts" \
            --deploy-container-runtime=podman \
            --deploy-directory="staging/$APPLICATION_NAME" \
            --compose-file=staging.compose.yml \
            --deploy-values="APP_IMAGE=$DEPLOY_IMAGE" \
            --deploy-secret-env-file="file://$HOME/secrets/$APPLICATION_NAME/staging.env"
```

### Production

Pushes to `main` or `master` publish `latest` and deploy it to the production `--ssh-target`.

```yaml
# /home/deploy/apps/my-app/production.compose.yml on the production --ssh-target.
services:
  app:
    # --deploy-values supplies the latest image published by this workflow.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      # Production keeps the public host port used by our application.
      - "5000:5000"
    environment:
      # --deploy-secret-env-file supplies these values only to the Compose process.
      DATABASE_URL: ${DATABASE_URL:?DATABASE_URL is required}
      APP_SECRET: ${APP_SECRET:?APP_SECRET is required}
    # Optional production limits can be larger than staging. Enable these only when
    # the target's Compose implementation supports and enforces deploy resources.
    # deploy:
    #   resources:
    #     limits:
    #       cpus: "1.0"
    #       memory: 512M
```

The workflow sets `--compose-file=production.compose.yml`; this file is independent of `staging.compose.yml` and the default `compose.yml`.

```yaml
# .github/workflows/production.yml
name: Deploy production

on:
  push:
    branches: [main, master]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      APPLICATION_NAME: my-app
      DEPLOY_IMAGE: registry.example.com/team/my-app:latest
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy production
        run: |
          # Build on the runner, then use the production SSH credentials and target-side
          # production.compose.yml for the deployment.
          dagger -W github.com/ninesl/daggerer@master api call \
            with-build-secret \
            --id=github_pat \
            --secret="file://$HOME/secrets/$APPLICATION_NAME/github_pat" \
            release \
            --source=. \
            --registry=registry.example.com/team \
            --app-name="$APPLICATION_NAME" \
            --tag=latest \
            --registry-username=registry-user \
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password" \
            --ssh-target=deploy@prod.example.com \
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/production_ssh_key" \
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/production_known_hosts" \
            --deploy-container-runtime=docker \
            --deploy-directory="apps/$APPLICATION_NAME" \
            --compose-file=production.compose.yml \
            --deploy-values="APP_IMAGE=$DEPLOY_IMAGE" \
            --deploy-secret-env-file="file://$HOME/secrets/$APPLICATION_NAME/production.env"
```

Both releases stop on registry, build, publish, SSH, pull, or Compose errors. Daggerer does not discover these files or environment variables: every path and value is an explicit workflow input.

[dagger-build]: https://docs.dagger.io/reference/api/directory#dockerBuild
[dagger-container]: https://docs.dagger.io/reference/api/container
