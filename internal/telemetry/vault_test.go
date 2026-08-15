package telemetry

import (
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// workspaceID is the seeded one — every fresh store has exactly one.
func workspaceID(t *testing.T, store *Store) int64 {
	t.Helper()
	spaces, err := store.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 1 {
		t.Fatalf("a fresh store has %+v", spaces)
	}
	return spaces[0].ID
}

func envID(t *testing.T, store *Store, name string) int64 {
	t.Helper()
	envs, err := store.Envs(workspaceID(t, store))
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range envs {
		if env.Name == name {
			return env.ID
		}
	}
	t.Fatalf("no environment called %q in %+v", name, envs)
	return 0
}

func TestVaultSeedsAWorkspaceWithFourEnvironments(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	envs, err := store.Envs(workspaceID(t, store))
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != len(model.DefaultEnvs) {
		t.Fatalf("seeded %+v", envs)
	}
	for _, env := range envs {
		if env.Secrets != 0 || env.Keys != 0 || env.Revision != 0 {
			t.Fatalf("a fresh environment is not empty: %+v", env)
		}
		if env.Workspace != model.DefaultWorkspace {
			t.Fatalf("environment %q is not in the default workspace: %+v", env.Name, env)
		}
	}
}

// The reason workspaces exist: two applications both have a production, and
// neither knows about the other's.
func TestTwoWorkspacesMayBothHaveAProduction(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	hushkey, err := store.SaveWorkspace(model.Workspace{Name: "hushkey"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := store.SaveWorkspace(model.Workspace{Name: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	// Each arrives with the four stages already in it.
	if hushkey.Envs != len(model.DefaultEnvs) || auth.Envs != len(model.DefaultEnvs) {
		t.Fatalf("seeded %+v and %+v", hushkey, auth)
	}
	if _, err := store.SaveWorkspace(model.Workspace{Name: "HUSHKEY"}); err == nil {
		t.Fatal("two workspaces differing only in case were stored")
	}

	production := func(space model.Workspace) model.Env {
		t.Helper()
		envs, err := store.Envs(space.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, env := range envs {
			if env.Name == "production" {
				return env
			}
		}
		t.Fatalf("no production in %q", space.Name)
		return model.Env{}
	}
	one, two := production(hushkey), production(auth)
	if one.ID == two.ID {
		t.Fatal("the two productions are the same row")
	}

	// The same key name in both, with different values, and neither leaks.
	if _, err := store.SaveSecret(model.Secret{EnvID: one.ID, Key: "DATABASE_URL", Value: "hushkey-db"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSecret(model.Secret{EnvID: two.ID, Key: "DATABASE_URL", Value: "auth-db"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Secret(one.ID, "DATABASE_URL"); got.Value != "hushkey-db" {
		t.Fatalf("hushkey production reads %q", got.Value)
	}
	if got, _ := store.Secret(two.ID, "DATABASE_URL"); got.Value != "auth-db" {
		t.Fatalf("auth production reads %q", got.Value)
	}
	// And one workspace's environments are not the other's.
	for _, env := range mustEnvs(t, store, hushkey.ID) {
		if env.WorkspaceID != hushkey.ID {
			t.Fatalf("hushkey listed %+v", env)
		}
	}
}

func mustEnvs(t *testing.T, store *Store, workspace int64) []model.Env {
	t.Helper()
	envs, err := store.Envs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	return envs
}

// Deleting an application takes everything under it — otherwise a key would
// outlive the environment it reads, which is a token nobody thinks to revoke.
func TestDeletingAWorkspaceTakesTheTreeWithIt(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	space, err := store.SaveWorkspace(model.Workspace{Name: "pack"})
	if err != nil {
		t.Fatal(err)
	}
	envs := mustEnvs(t, store, space.ID)
	if _, err := store.SaveSecret(model.Secret{EnvID: envs[0].ID, Key: "TOKEN", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	minted, err := store.CreateAPIKey(model.APIKey{EnvID: envs[0].ID, Name: "pack app"})
	if err != nil {
		t.Fatal(err)
	}
	if minted.Workspace != "pack" {
		t.Fatalf("the key does not name its workspace: %+v", minted)
	}

	if err := store.DeleteWorkspace(space.ID); err != nil {
		t.Fatal(err)
	}
	if left := mustEnvs(t, store, space.ID); len(left) != 0 {
		t.Fatalf("environments outlived the workspace: %+v", left)
	}
	if left, err := store.Secrets(envs[0].ID); err != nil || len(left) != 0 {
		t.Fatalf("secrets outlived the workspace: %+v %v", left, err)
	}
	keys, err := store.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if key.ID == minted.ID {
			t.Fatalf("a key outlived its workspace: %+v", key)
		}
	}
}

func TestSecretRoundTripsAndCounts(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	production := envID(t, store, "production")

	saved, err := store.SaveSecret(model.Secret{EnvID: production, Key: "DATABASE_URL", Value: "postgres://localhost/app"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Value != "postgres://localhost/app" {
		t.Fatalf("read back %q", saved.Value)
	}

	// Writing the same key again is an update rather than a second row: an
	// import is a loop over this, and a duplicate would be two answers to one
	// name.
	if _, err := store.SaveSecret(model.Secret{EnvID: production, Key: "DATABASE_URL", Value: "postgres://db/app"}); err != nil {
		t.Fatal(err)
	}
	secrets, err := store.Secrets(production)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0].Value != "postgres://db/app" {
		t.Fatalf("after the second write: %+v", secrets)
	}

	for _, env := range mustEnvs(t, store, workspaceID(t, store)) {
		if env.ID != production {
			continue
		}
		if env.Secrets != 1 || env.Revision == 0 {
			t.Fatalf("the group did not count its secret: %+v", env)
		}
	}

	if err := store.DeleteSecret(secrets[0].ID); err != nil {
		t.Fatal(err)
	}
	if left, err := store.Secrets(production); err != nil || len(left) != 0 {
		t.Fatalf("after delete: %+v %v", left, err)
	}
}

func TestSecretKeysAreHeldToWhatAnEnvironmentCanCarry(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	local := envID(t, store, "local")

	for _, key := range []string{"", "with space", "2FA_CODE", "lower-case-dash", "quote\"d"} {
		if _, err := store.SaveSecret(model.Secret{EnvID: local, Key: key, Value: "x"}); err == nil {
			t.Fatalf("stored a secret called %q", key)
		}
	}
	for _, key := range []string{"A", "_UNDER", "MIXED_case9"} {
		if _, err := store.SaveSecret(model.Secret{EnvID: local, Key: key, Value: "x"}); err != nil {
			t.Fatalf("refused %q: %v", key, err)
		}
	}
}

func TestImportReportsBeforeItWrites(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	develop := envID(t, store, "develop")

	if _, err := store.SaveSecret(model.Secret{EnvID: develop, Key: "KEEP", Value: "same"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSecret(model.Secret{EnvID: develop, Key: "MOVE", Value: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSecret(model.Secret{EnvID: develop, Key: "GONE", Value: "stale"}); err != nil {
		t.Fatal(err)
	}

	text := "KEEP=same\nMOVE=new\nNEW_ONE=fresh\nnot a pair\n"
	dry, err := store.ImportSecrets(model.Import{EnvID: develop, Text: text, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Added) != 1 || len(dry.Changed) != 1 || len(dry.Unchanged) != 1 || len(dry.Skipped) != 1 {
		t.Fatalf("dry run said %+v", dry)
	}
	// A dry run that wrote something is the one bug this feature cannot have.
	if current, _ := store.Secret(develop, "MOVE"); current.Value != "old" {
		t.Fatalf("the dry run wrote: %q", current.Value)
	}
	if _, err := store.Secret(develop, "NEW_ONE"); err == nil {
		t.Fatal("the dry run added a secret")
	}

	live, err := store.ImportSecrets(model.Import{EnvID: develop, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Added) != 1 || len(live.Changed) != 1 || len(live.Pruned) != 0 {
		t.Fatalf("the import said %+v", live)
	}
	if moved, _ := store.Secret(develop, "MOVE"); moved.Value != "new" {
		t.Fatalf("MOVE is %q", moved.Value)
	}
	// Untouched keys survive a paste that does not mention them, unless asked.
	if _, err := store.Secret(develop, "GONE"); err != nil {
		t.Fatalf("an unmentioned key was dropped: %v", err)
	}

	pruned, err := store.ImportSecrets(model.Import{EnvID: develop, Text: text, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned.Pruned) != 1 || pruned.Pruned[0] != "GONE" {
		t.Fatalf("prune removed %+v", pruned.Pruned)
	}
	if _, err := store.Secret(develop, "GONE"); err == nil {
		t.Fatal("prune left the key behind")
	}
}

func TestAPIKeyIsShownOnceAndScopedToOneEnvironment(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	staging := envID(t, store, "staging")

	minted, err := store.CreateAPIKey(model.APIKey{EnvID: staging, Name: "hushkey-web"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(minted.Token, "gsk_default_staging_") {
		t.Fatalf("minted %q", minted.Token)
	}
	if minted.Prefix == "" || strings.Contains(minted.Token[len(minted.Prefix):], minted.Prefix) {
		t.Fatalf("the prefix is not the head of the token: %q %q", minted.Prefix, minted.Token)
	}

	keys, err := store.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Token != "" {
		t.Fatalf("the listing carried a token back: %+v", keys)
	}
	if keys[0].EnvName != "staging" {
		t.Fatalf("the key lost its environment: %+v", keys[0])
	}
	if !keys[0].Live(keys[0].CreatedAt) {
		t.Fatal("a fresh key is not live")
	}

	if err := store.RevokeAPIKey(minted.ID); err != nil {
		t.Fatal(err)
	}
	keys, err = store.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	// Revoked, and still listed: the row is the record that it existed.
	if len(keys) != 1 || keys[0].RevokedAt.IsZero() || keys[0].Live(keys[0].RevokedAt) {
		t.Fatalf("after revoke: %+v", keys)
	}
}

func TestDeletingAnEnvironmentTakesItsSecretsAndKeys(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	env, err := store.SaveEnv(model.Env{WorkspaceID: workspaceID(t, store), Name: "  preview  "})
	if err != nil {
		t.Fatal(err)
	}
	if env.Name != "preview" {
		t.Fatalf("stored %q", env.Name)
	}
	if _, err := store.SaveEnv(model.Env{WorkspaceID: workspaceID(t, store), Name: "PREVIEW"}); err == nil {
		t.Fatal("two environments differing only in case were stored")
	}
	if _, err := store.SaveSecret(model.Secret{EnvID: env.ID, Key: "TOKEN", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAPIKey(model.APIKey{EnvID: env.ID, Name: "preview app"}); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteEnv(env.ID); err != nil {
		t.Fatal(err)
	}
	if left, err := store.Secrets(env.ID); err != nil || len(left) != 0 {
		t.Fatalf("secrets outlived the environment: %+v %v", left, err)
	}
	keys, err := store.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	// A token pointing at nothing is a token nobody thinks to revoke.
	if len(keys) != 0 {
		t.Fatalf("keys outlived the environment: %+v", keys)
	}
}
