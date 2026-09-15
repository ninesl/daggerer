package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"dagger/daggerer/internal/dagger"
)

func TestReadmeGoDockerfile(t *testing.T) {
	ctx := context.Background()
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	// Use the actual documented Dockerfile, not a separately maintained copy.
	blocks := strings.Split(string(readme), "```dockerfile\n")
	if len(blocks) < 3 {
		t.Fatal("expected shared Go Dockerfile and private env example")
	}
	dockerfile := strings.SplitN(blocks[1], "```", 2)[0]
	privateEnvInstruction := strings.SplitN(blocks[2], "```", 2)[0]
	mod, err := os.ReadFile("testdata/go-app/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile("testdata/go-app/main.go")
	if err != nil {
		t.Fatal(err)
	}
	var version string
	for _, line := range strings.Split(string(mod), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" {
			version = fields[1]
		}
	}
	if version == "" {
		t.Fatal("fixture must declare its Go version")
	}
	// The fixture has no external dependency: dummy PATs exercise secret delivery,
	// not real GitHub authorization. The production example needs real PAT access.
	source := dag.Directory().WithNewFile("go.mod", string(mod)).WithNewFile("go.sum", "").WithNewFile("main.go", string(main))
	file := dag.Directory().WithNewFile("public.env", "GO_VERSION="+version+"\nAPP_PACKAGE=.\nFOO=hello\n").File("public.env")
	secrets := []*dagger.Secret{dag.SetSecret("test-pat", "test-pat"), dag.SetSecret("test-bar", "test-bar")}
	for _, tc := range []struct {
		name        string
		values      *dagger.EnvFile
		private     *dagger.Secret
		instruction string
		want        string
	}{
		{"workflow-file-and-named-secrets", nil, nil, "", "hello\n"},
		{"explicit-override-and-private-env", dag.EnvFile().WithVariable("FOO", "override"), dag.SetSecret("test-private-env", "BAR=private-value\n"), privateEnvInstruction, "override\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container, err := (&Daggerer{}).BuildOnly(ctx, source.WithNewFile("Dockerfile", dockerfile+tc.instruction), file, tc.values, tc.private, []string{"github_token", "BAR"}, secrets, "Dockerfile")
			if err != nil {
				t.Fatal(err)
			}
			// DockerBuild keeps secret bindings on the Dagger object. Verify the
			// exported image, which is what gets published/deployed, has no mounts.
			image := dag.Container().Import(container.AsTarball())
			out, err := image.WithExec([]string{"/usr/local/bin/server"}).Stdout(ctx)
			if err != nil || out != tc.want {
				t.Fatalf("app output = %q, error = %v", out, err)
			}
			_, err = image.WithExec([]string{"sh", "-ec", "test ! -e /run/secrets/build_env; test ! -e /run/secrets/github_token; test ! -e /run/secrets/BAR"}).Sync(ctx)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
