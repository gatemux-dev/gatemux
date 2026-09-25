package api

import (
	"context"
	"github.com/gatemux-dev/gatemux/internal/apidocs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOfficialOpenAISDKContracts(t *testing.T) {
	if os.Getenv("GATEMUX_SDK_TEST") != "1" {
		t.Skip("set GATEMUX_SDK_TEST=1 with uv installed; only local fixture calls")
	}
	f, _, _ := newResponsesFixture(t)
	key := f.issueKey(f.createTeam("sdk"), nil)
	if err := apidocs.Mount(f.Router); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(f.Router)
	defer gateway.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "uv", "run", "--no-project", "--with-requirements", filepath.Join(root, "tests/sdk/requirements.txt"), "python", filepath.Join(root, "tests/sdk/openai_contract.py"))
	command.Env = append(os.Environ(), "GATEMUX_SDK_FIXTURE_URL="+gateway.URL, "GATEMUX_SDK_FIXTURE_KEY="+key, "GATEMUX_SDK_FIXTURE_ALIAS="+f.Alias)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("SDK contracts: %v\n%s", err, output)
	}
	t.Log(string(output))
	if _, err := os.Stat(filepath.Join(root, "tests/sdk/node_modules/openai/package.json")); err != nil {
		t.Fatal("Node SDK missing: run npm ci --prefix tests/sdk")
	}
	node := exec.CommandContext(ctx, "node", filepath.Join(root, "tests/sdk/openai_contract.mjs"))
	node.Env = command.Env
	output, err = node.CombinedOutput()
	if err != nil {
		t.Fatalf("Node SDK contracts: %v\n%s", err, output)
	}
	t.Log(string(output))
}
