# Secrets

[Back to the Daggerer README](README.md)

Our examples keep build secrets, deployment environment secrets, and infrastructure credentials in separate runner-local files. Each file has one explicit job in the workflow:

```text
# Files read by the self-hosted runner service user.
$HOME/secrets/my-app/
├── registry_password        # --registry-password for publishing and remote registry login
├── github_pat               # with-build-secret --secret for private build dependencies
├── staging.env              # staging --deploy-secret-env-file
├── production.env           # production --deploy-secret-env-file
├── staging_ssh_key          # staging --ssh-key
├── staging_known_hosts      # staging --known-hosts
├── production_ssh_key       # production --ssh-key
└── production_known_hosts   # production --known-hosts
```

These paths are our chosen layout, Daggerer does not know these paths automatically.

Keep the files outside the application checkout and restrict them to the runner service user with `chmod`, etc.

## Deployment Credentials

The release workflows use three Dagger Secret inputs for infrastructure access:

- `--registry-password` authenticates both the image publish and registry login on `--ssh-target`
- `--ssh-key` authenticates the `ssh` user selected by `--ssh-target`
- `--known-hosts` supplies the verified host key used for strict `ssh` host checking; its host entry must match `--ssh-target` and `--ssh-target-port` (`22` by default)

These inputs accept either `file://` or `env://`. [README.md](README.md) uses runner-local `file://` paths so the source of each credential is visible beside the API call.

The build and deployment files below intentionally require `file://`; do not pass their values through public command arguments.

## Dockerfile Build Secrets

`with-build-secret` maps one runner-local file to a BuildKit secret ID. `--id` chooses that ID and `--secret` selects the file:

```dockerfile
# The workflow uses with-build-secret --id=github_pat before build or release.
RUN --mount=type=secret,id=github_pat,required=true \
    GITHUB_PAT="$(cat /run/secrets/github_pat)" && \
    use-private-dependency "$GITHUB_PAT"
```

Chain `with-build-secret` once for every secret ID required by the file selected by `--dockerfile`:

```yaml
run: dagger -W github.com/ninesl/daggerer@master api call \
  with-build-secret \
  --id=github_pat \
  --secret=file://$HOME/secrets/my-app/github_pat \
  with-build-secret \
  --id=npm_token \
  --secret=file://$HOME/secrets/my-app/npm_token \
  release ...
```

Each `--id` must be unique in the chain. It does not need to match the source filename or the `github_pat` name in this example.

When the Dockerfile consumes that secret, DockerBuild uses the ID to pair it with the `RUN --mount=type=secret,id=...` that requests it.

### Dagger Build Secret Workaround

Dagger `v1.0.0-beta.13` does not let `Directory.DockerBuild` directly pair an arbitrary ID with a `Secret`.

Daggerer creates the build secret mapping with Dagger's [`Plaintext()` then `SetSecret()` workaround](https://docs.dagger.io/0.21/cookbook/#use-secret-in-dockerfile-build). Native `{id, secret}` support for `DockerBuild` is discussed in [dagger/dagger#7358](https://github.com/dagger/dagger/issues/7358), [dagger/dagger#9437](https://github.com/dagger/dagger/issues/9437), and [dagger/dagger#8058](https://github.com/dagger/dagger/pull/8058). Native support would let Daggerer remove the workaround and its `Plaintext()` call.

`Plaintext()` returns the secret value to the Daggerer module process so it can register a new Secret under the requested BuildKit ID. Daggerer does not log, parse, write, or include that value in errors, but all code running in the module process shares access to its memory; only use module revisions you trust.

Dockerfile build steps remain inside BuildKit instead of becoming individually programmable Dagger API operations, so those steps cannot directly use features such as `Container.WithMountedSecret`. The surrounding build and release workflow still uses the rest of the Dagger Engine.

## Deployment Environment Secrets

`--deploy-secret-env-file` accepts one private dotenv file through `file://`. Our workflows choose a different file for each environment:

```dotenv
# $HOME/secrets/my-app/staging.env
DATABASE_URL=postgres://app:staging-password@staging-db.internal:5432/my_app
STAGING_SECRET_KEY=replace-with-staging-secret
```

```dotenv
# $HOME/secrets/my-app/production.env
DATABASE_URL=postgres://app:production-password@prod-db.internal:5432/my_app
PROD_SECRET_TOKEN=replace-with-production-secret
```

Daggerer validates the Secret, then streams it over `ssh` stdin. Staging passes `staging.env` to `podman compose`; production passes `production.env` to `docker compose`. The values do not return through the Dagger API or get written to either target.

These names come from our application, not Daggerer. [`staging.compose.yml`](README.md#staging-target-yaml) requires the staging `DATABASE_URL` and `STAGING_SECRET_KEY`. [`production.compose.yml`](README.md#production-target-yaml) requires the production `DATABASE_URL` and `PROD_SECRET_TOKEN`.

Users with sufficient `podman` or `docker` access can inspect a container's runtime environment. Daggerer also cannot prevent application, Dockerfile, `podman compose`, or `docker compose` logic from disclosing a value it receives.

## Public Values

`--build-env-file`, `--build-values`, `--deploy-env-file`, and `--deploy-values` are public inputs. Never put credentials in them.

Daggerer does not define the names or meanings of these values. The build pair becomes Docker build arguments, and the deployment pair becomes the `compose` runtime environment; Daggerer does not merge build values with deployment values.

Within each pair, Daggerer merges in low-to-high precedence order:

1. Load the caller-mounted `.env` supplied by `--build-env-file` or `--deploy-env-file`, when present.
2. Load the literal dotenv text supplied by `--build-values` or `--deploy-values` into an in-memory file. These values take precedence and overwrite matching names from the mounted `.env`.
3. Keep names that occur in only one source.

Our [staging workflow](README.md#staging-workflow) and [production workflow](README.md#production-workflow) take advantage of this behavior. These are application choices in our example, not Daggerer defaults:

| Workflow | Checked-out `.env` | In-memory `--deploy-values` | Final public value |
| --- | --- | --- | --- |
| Staging | `APP_ENV=DEV` | `APP_ENV=STAGING` | `APP_ENV=STAGING` |
| Production | `APP_ENV=DEV` | `APP_ENV=PRODUCTION` | `APP_ENV=PRODUCTION` |

Each workflow also adds its public `APP_IMAGE` through `--deploy-values`.

Only public inputs participate in this overwrite model. Secrets never overwrite public values, and public values never overwrite secrets. Daggerer rejects name collisions instead:

- A build variable name that exactly matches a chained `with-build-secret --id` is rejected before DockerBuild starts.
- A deployment variable name that exactly matches a key in `--deploy-secret-env-file` is rejected before deployment starts.

Both collision errors identify the conflicting name without including either value.
