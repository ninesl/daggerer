# Secrets

Daggerer keeps build secrets, deployment environment secrets, and infrastructure credentials on separate paths. The examples assume these runner-local files:

```text
$HOME/secrets/my-app/
├── registry_password
├── github_pat
├── staging.env
├── production.env
├── staging_ssh_key
├── staging_known_hosts
├── production_ssh_key
└── production_known_hosts
```

Restrict these files to the runner service user, for example with `chmod 600`.

## Credentials

`--registry-password`, `--ssh-key`, and `--known-hosts` are Dagger Secrets used only by their authentication helpers. They accept `file://` or `env://` Secret sources.

The private build and deployment inputs below intentionally require `file://`. This keeps them as explicit runner-local files rather than public command values.

## Dockerfile Build Secrets

Each `with-build-secret` maps one raw file to the ID used by a Dockerfile secret mount:

```dockerfile
RUN --mount=type=secret,id=github_pat,required=true \
    GITHUB_PAT="$(cat /run/secrets/github_pat)" && \
    use-private-dependency "$GITHUB_PAT"
```

Call `with-build-secret` repeatedly when the Dockerfile needs more than one secret:

```text
dagger -W github.com/ninesl/daggerer@master api call \
  with-build-secret \
  --id=github_pat \
  --secret=file://$HOME/secrets/my-app/github_pat \
  with-build-secret \
  --id=npm_token \
  --secret=file://$HOME/secrets/my-app/npm_token \
  release ...
```

The IDs must be unique and must exactly match the Dockerfile mount IDs.

### DockerBuild Warning

Dagger `v1.0.0-beta.13` does not let `Directory.DockerBuild` pair an arbitrary ID directly with a `Secret`. Daggerer therefore uses Dagger's documented [`Plaintext()` then `SetSecret()` workaround](https://docs.dagger.io/0.21/cookbook/#use-secret-in-dockerfile-build). The value briefly exists in module memory while Daggerer assigns the requested BuildKit ID. It is not logged, parsed, written, or included in errors.

This workaround may be deprecated or removed when DockerBuild accepts native `{id, secret}` inputs. Track [dagger/dagger#7358](https://github.com/dagger/dagger/issues/7358), [dagger/dagger#9437](https://github.com/dagger/dagger/issues/9437), and [PR #8058](https://github.com/dagger/dagger/pull/8058).

Using Dockerfiles this way gives up some native Dagger programmability, composition, and direct `WithMountedSecret` handling. Daggerer accepts that tradeoff to use existing Dockerfiles while retaining useful parts of the Dagger Engine: portable execution, graph caching, observability, publishing, and release orchestration.

## Deployment Environment

`--deploy-secret-env-file` accepts one private dotenv file through `file://`:

```dotenv
# staging.env or production.env
DATABASE_URL=postgres://app:password@db.internal:5432/my_app
APP_SECRET=replace-me
```

Daggerer mounts the original Secret for validation and streams its values over SSH stdin to the remote Compose process. Values do not return through the Dagger API, become command-line arguments, or get written to the checkout or deployment target.

The Compose file explicitly chooses which values enter the running container:

```yaml
environment:
  DATABASE_URL: ${DATABASE_URL:?DATABASE_URL is required}
  APP_SECRET: ${APP_SECRET:?APP_SECRET is required}
```

Runtime users with sufficient container-engine access can inspect a container's environment. Daggerer cannot prevent the application, Dockerfile, or Compose configuration from disclosing a supplied value.

## Public Values

`--build-env-file`, `--build-values`, `--deploy-env-file`, and `--deploy-values` are public inputs. Never put credentials in them.

File values load first and explicit `--*-values` override matching names. Before deployment, Daggerer rejects any public deployment name also present in `--deploy-secret-env-file`. Collision errors contain the variable name, never its value.
