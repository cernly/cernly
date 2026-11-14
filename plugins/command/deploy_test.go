package command

import (
	"strings"
	"testing"
)

func args(s string) []arg {
	var out []arg
	for _, w := range strings.Fields(s) {
		out = append(out, arg{text: w, literal: !strings.HasPrefix(w, "$")})
	}
	return out
}

func TestDeployCommands(t *testing.T) {
	for _, c := range []struct {
		cmd, tool, paths, name string
		starts                 bool
	}{
		{"kubectl apply -k deploy/overlays/prod", "kubernetes", "deploy/overlays/prod", "prod", true},
		{"kubectl -n shop apply -f k8s/api.yaml -f k8s/db.yaml", "kubernetes", "k8s/api.yaml k8s/db.yaml", "db", true},
		{"kubectl diff --filename=k8s", "kubernetes", "k8s", "k8s", false},
		{"kustomize build overlays/staging", "kubernetes", "overlays/staging", "staging", false},
		{"helm upgrade --install shop charts/shop -f charts/shop/values-prod.yaml --set image.tag=$TAG", "helm", "charts/shop charts/shop/values-prod.yaml", "prod", true},
		{"helm install shop ./chart", "helm", "./chart", "shop", true},
	} {
		a := args(c.cmd)
		var fd found
		if !recognise(a, &fd) || fd.deploy == nil {
			t.Fatalf("%s: not recognised", c.cmd)
		}
		d := fd.deploy
		if d.tool != c.tool || strings.Join(d.paths, " ") != c.paths || d.starts != c.starts || deployName(d, d.paths) != c.name {
			t.Errorf("%s: %+v name %s", c.cmd, *d, deployName(d, d.paths))
		}
	}
	for _, cmd := range []string{"kubectl get pods", "kubectl apply", "helm repo add x y", "kustomize version"} {
		var fd found
		if recognise(args(cmd), &fd) && fd.deploy != nil {
			t.Errorf("%s: recognised as a deploy", cmd)
		}
	}
}
