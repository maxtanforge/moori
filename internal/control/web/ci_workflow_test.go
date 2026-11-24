package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestGeneratedCIWorkflowsParseAsYAMLAndShell(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required to evaluate generated workflows")
	}
	js := `const stubElement=()=>({querySelectorAll(){return[]},querySelector:stubElement,addEventListener(){},removeEventListener(){},setAttribute(){},removeAttribute(){},append(){},prepend(){},replaceChildren(){},closest(){return null},classList:{add(){},remove(){},toggle(){}},dataset:{},hidden:true,value:'',textContent:'',focus(){},isConnected:true,getBoundingClientRect(){return{}},getClientRects(){return[]}});
globalThis.window={addEventListener(){},removeEventListener(){}};
globalThis.document={querySelectorAll(){return[]},querySelector:stubElement,getElementById:stubElement,addEventListener(){},removeEventListener(){}};
const mod=await import('./ci-management.js');
process.stdout.write(JSON.stringify(['container','compose'].map(kind=>mod.giteaDeployStep({kind,project:'app',target:'production'}))));`
	output, err := exec.Command(node, "--input-type=module", "-e", js).CombinedOutput()
	if err != nil {
		t.Fatalf("generate workflows: %v\n%s", err, output)
	}
	var snippets []string
	if err := json.Unmarshal(output, &snippets); err != nil {
		t.Fatal(err)
	}
	bash, _ := exec.LookPath("bash")
	if bash == "" {
		if git, err := exec.LookPath("git"); err == nil {
			candidate := filepath.Join(filepath.Dir(filepath.Dir(git)), "bin", "bash.exe")
			if _, err := os.Stat(candidate); err == nil {
				bash = candidate
			}
		}
	}
	for _, snippet := range snippets {
		var steps []struct {
			Env map[string]string `yaml:"env"`
			Run string            `yaml:"run"`
		}
		if err := yaml.Unmarshal([]byte(snippet), &steps); err != nil {
			t.Fatal(err)
		}
		if len(steps) != 1 || steps[0].Env["MOORI_TOKEN"] != "${{ secrets.MOORI_TOKEN }}" || steps[0].Run == "" {
			t.Fatal("invalid CI step", steps)
		}
		if bash != "" {
			cmd := exec.Command(bash, "-n")
			cmd.Stdin = strings.NewReader(steps[0].Run)
			if result, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("shell syntax: %v\n%s", err, result)
			}
		}
	}
}
