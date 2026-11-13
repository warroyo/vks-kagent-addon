package acd

// Renders chart/addon-config-definition.yaml the way the addon controller does
// (Go text/template, sprig, Helm's toYaml), then feeds the result to helm
// template. The controller reports a template error only per cluster after
// install, in a chart version that cannot be re-published, so this is the fast
// loop. It checks the values the template derives, and that they agree with
// the chart's own defaults and with what the chart renders.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"sigs.k8s.io/yaml"
)

type acdFile struct {
	Spec struct {
		TemplateOutputResources []struct {
			Template            string         `json:"template"`
			TargetClusterOutput map[string]any `json:"targetClusterOutput"`
		} `json:"templateOutputResources"`
	} `json:"spec"`
}

// valuesTemplate returns the template that renders the guest values Secret.
func valuesTemplate(t *testing.T) *template.Template {
	t.Helper()
	raw, err := os.ReadFile("../../chart/addon-config-definition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var acd acdFile
	if err := yaml.Unmarshal(raw, &acd); err != nil {
		t.Fatal(err)
	}
	funcs := sprig.TxtFuncMap()
	funcs["toYaml"] = func(v any) string {
		b, err := yaml.Marshal(v)
		if err != nil {
			return ""
		}
		return strings.TrimSuffix(string(b), "\n")
	}
	funcs["fromYaml"] = func(s string) map[string]any {
		out := map[string]any{}
		if err := yaml.Unmarshal([]byte(s), &out); err != nil {
			out["Error"] = err.Error()
		}
		return out
	}
	for _, o := range acd.Spec.TemplateOutputResources {
		if o.TargetClusterOutput != nil && o.TargetClusterOutput["referenceType"] == "ValuesRef" {
			tpl, err := template.New("values").Funcs(funcs).Option("missingkey=error").Parse(o.Template)
			if err != nil {
				t.Fatal(err)
			}
			return tpl
		}
	}
	t.Fatal("no ValuesRef output in the ACD")
	return nil
}

// clusterWith is the Cluster resource as the controller hands it to the
// template: an unstructured object.
func clusterWith(spec map[string]any) map[string]any {
	c := map[string]any{"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "Cluster", "metadata": map[string]any{"name": "c1"}}
	if spec != nil {
		c["spec"] = spec
	}
	return c
}

// render returns the helm values the ACD renders for cluster c1.
func render(t *testing.T, values map[string]any, deps map[string]any) map[string]any {
	t.Helper()
	ctx := map[string]any{
		"Values":       values,
		"Dependencies": deps,
		"Cluster":      map[string]any{"name": "c1", "namespace": "ns1"},
		"Addon":        map[string]any{"name": "kagent"},
	}
	var out bytes.Buffer
	if err := valuesTemplate(t).Execute(&out, ctx); err != nil {
		t.Fatalf("render: %v", err)
	}
	var secret struct {
		StringData map[string]string `json:"stringData"`
	}
	if err := yaml.Unmarshal(out.Bytes(), &secret); err != nil {
		t.Fatalf("output is not a Secret body: %v\n%s", err, out.String())
	}
	var helmValues map[string]any
	if err := yaml.Unmarshal([]byte(secret.StringData["values.yaml"]), &helmValues); err != nil {
		t.Fatalf("values.yaml: %v\n%s", err, out.String())
	}
	return helmValues
}

func get(m map[string]any, path ...string) any {
	var v any = m
	for _, p := range path {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[p]
	}
	return v
}

func strings0(v any) []string {
	var out []string
	items, _ := v.([]any)
	for _, i := range items {
		out = append(out, i.(string))
	}
	return out
}

func schemaDefaults() map[string]any {
	return map[string]any{"helmOptions": map[string]any{"targetNamespace": "ate-system"}}
}

func clusterDeps(serviceDomain string) map[string]any {
	spec := map[string]any{"clusterNetwork": map[string]any{"serviceDomain": serviceDomain}}
	return map[string]any{"ClusterCR": clusterWith(spec)}
}

func TestACDDerivesEndpointsAndIdentities(t *testing.T) {
	v := render(t, schemaDefaults(), clusterDeps("cluster.local"))

	checks := map[string]any{
		"kagent apiEndpoint":    get(v, "kagent", "config", "substrate", "apiEndpoint"),
		"kagent routerUrl":      get(v, "kagent", "config", "substrate", "routerUrl"),
		"kagent actorJwtIssuer": get(v, "kagent", "config", "substrate", "actorJwtIssuer"),
		"clusterDomain":         get(v, "global", "clusterDomain"),
		"podcert namespace":     get(v, "substrate-podcert", "postgresClientNamespace"),
		"podcert service acct":  get(v, "substrate-podcert", "postgresClientServiceAccount"),
	}
	want := map[string]any{
		"kagent apiEndpoint":    "dns:///kagent-api.ate-system.svc:443",
		"kagent routerUrl":      "https://kagent-atenet-router.ate-system.svc:443",
		"kagent actorJwtIssuer": "https://kagent-idp.ate-system.svc",
		"clusterDomain":         "cluster.local",
		"podcert namespace":     "ate-system",
		"podcert service acct":  "kagent-ate-api-server",
	}
	for name, got := range checks {
		if got != want[name] {
			t.Errorf("%s = %v, want %v", name, got, want[name])
		}
	}

	controller := "spiffe://cluster.local/ns/kagent/sa/kagent-controller"
	lists := map[string][]string{
		"preview":           {"PerTemplateSnapshotLocation", "GoldenEgressPolicy"},
		"bootstrapOwners":   {controller, "system:serviceaccount:ate-system:kagent-ate-client"},
		"ingressAllowed":    {controller},
		"postgresClients":   {"kagent/kagent-controller=kagent_owner_user", "kagent/kagent-controller=kagent_readwrite_user"},
		"atelet extra args": {"--ateapi-address=k8s:///kagent-api.ate-system.svc:443", "--ateapi-server-name=kagent-api.ate-system.svc"},
	}
	got := map[string][]string{
		"preview":           strings0(get(v, "substrate", "preview")),
		"bootstrapOwners":   strings0(get(v, "substrate", "ateApi", "authorization", "bootstrapOwners")),
		"ingressAllowed":    strings0(get(v, "substrate", "atenetRouter", "ingressAllowedSPIFFEIDs")),
		"postgresClients":   strings0(get(v, "substrate-podcert", "postgresClients")),
		"atelet extra args": strings0(get(v, "substrate", "atelet", "extraArgs")),
	}
	for name, w := range lists {
		if !reflect.DeepEqual(got[name], w) {
			t.Errorf("%s = %v, want %v", name, got[name], w)
		}
	}
	policies, _ := get(v, "substrate", "credentialProvider", "namespacePolicies").([]any)
	if len(policies) != 1 || get(policies[0].(map[string]any), "atespace") != "kagent" ||
		!reflect.DeepEqual(strings0(get(policies[0].(map[string]any), "allowedNamespaces")), []string{"kagent"}) {
		t.Errorf("namespacePolicies = %v", policies)
	}
}

// The DNS domain follows the Cluster resource; the SPIFFE trust domain does
// not, because Substrate's charts hardcode cluster.local in the identities.
func TestACDClusterDomain(t *testing.T) {
	for name, tc := range map[string]struct {
		deps map[string]any
		want string
	}{
		"custom service domain":   {clusterDeps("corp.example"), "corp.example"},
		"no inputs at all":        {nil, "cluster.local"},
		"empty inputs":            {map[string]any{}, "cluster.local"},
		"another input only":      {map[string]any{"other": map[string]any{}}, "cluster.local"},
		"cluster without a spec":  {map[string]any{"ClusterCR": clusterWith(nil)}, "cluster.local"},
		"spec without networking": {map[string]any{"ClusterCR": clusterWith(map[string]any{"paused": false})}, "cluster.local"},
		"networking without a domain": {map[string]any{"ClusterCR": clusterWith(map[string]any{
			"clusterNetwork": map[string]any{"pods": map[string]any{}}})}, "cluster.local"},
		"empty service domain": {clusterDeps(""), "cluster.local"},
	} {
		t.Run(name, func(t *testing.T) {
			v := render(t, schemaDefaults(), tc.deps)
			if got := get(v, "global", "clusterDomain"); got != tc.want {
				t.Fatalf("clusterDomain = %v, want %v", got, tc.want)
			}
			for _, id := range strings0(get(v, "substrate", "atenetRouter", "ingressAllowedSPIFFEIDs")) {
				if !strings.HasPrefix(id, "spiffe://cluster.local/") {
					t.Errorf("SPIFFE ID %q left the cluster.local trust domain", id)
				}
			}
		})
	}
}

func TestACDWithoutAnyValues(t *testing.T) {
	// A fresh AddonConfig the controller has not defaulted: no helmOptions.
	v := render(t, nil, nil)
	if get(v, "kagent", "config", "substrate", "apiEndpoint") != "dns:///kagent-api.ate-system.svc:443" {
		t.Fatalf("default namespace not applied: %v", get(v, "kagent", "config", "substrate"))
	}
}

func TestACDUsesTheTargetNamespace(t *testing.T) {
	v := render(t, map[string]any{"helmOptions": map[string]any{"targetNamespace": "agents"}}, nil)
	if got := get(v, "kagent", "config", "substrate", "actorJwtIssuer"); got != "https://kagent-idp.agents.svc" {
		t.Errorf("issuer %v", got)
	}
	if got := get(v, "substrate-podcert", "postgresClientNamespace"); got != "agents" {
		t.Errorf("postgresClientNamespace %v", got)
	}
	if owners := strings0(get(v, "substrate", "ateApi", "authorization", "bootstrapOwners")); owners[1] != "system:serviceaccount:agents:kagent-ate-client" {
		t.Errorf("owners %v", owners)
	}
}

func TestACDTenantFields(t *testing.T) {
	values := map[string]any{
		"helmOptions":         map[string]any{"targetNamespace": "ate-system"},
		"workerPools":         []any{map[string]any{"name": "vks-demo", "namespace": "vks-demo", "replicas": 3}, map[string]any{"name": "shared", "namespace": "kagent", "replicas": 2}},
		"snapshotStorageSize": "50Gi",
		"postgresStorageSize": "20Gi",
		"storageClass":        "vsan-default",
		"imageRegistry":       "harbor.example/proxy",
		"modelProvider": map[string]any{
			"baseUrl": "https://pais.example/v1", "model": "qwen", "apiKeySecretName": "pais-key",
		},
		"helmValues": map[string]any{
			// The AddonConfig's own values survive...
			"substrate": map[string]any{"ateApi": map[string]any{"logLevel": "debug"}},
			"global":    map[string]any{"imagePullSecrets": []any{map[string]any{"name": "pull"}}},
			// ...but a derived key is overwritten.
			"kagent": map[string]any{"config": map[string]any{"substrate": map[string]any{"apiEndpoint": "dns:///wrong:443"}}},
		},
	}
	v := render(t, values, clusterDeps("cluster.local"))
	// ACD_DUMP_VALUES=<file> keeps the rendered values for a manual helm template.
	if path := os.Getenv("ACD_DUMP_VALUES"); path != "" {
		b, _ := yaml.Marshal(v)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := get(v, "global", "imageRegistry"); got != "harbor.example/proxy" {
		t.Errorf("imageRegistry %v", got)
	}
	if get(v, "rustfs", "storageSize") != "50Gi" || get(v, "postgres", "storageSize") != "20Gi" ||
		get(v, "rustfs", "storageClass") != "vsan-default" || get(v, "postgres", "storageClass") != "vsan-default" {
		t.Errorf("storage: rustfs %v postgres %v", get(v, "rustfs"), get(v, "postgres"))
	}
	pools, _ := get(v, "workerPools").([]any)
	if len(pools) != 2 || get(pools[0].(map[string]any), "name") != "vks-demo" {
		t.Errorf("workerPools %v", pools)
	}
	if get(v, "kagent", "providers", "default") != "openAI" ||
		get(v, "kagent", "providers", "openAI", "model") != "qwen" ||
		get(v, "kagent", "providers", "openAI", "apiKeySecretRef") != "pais-key" ||
		get(v, "kagent", "providers", "openAI", "config", "baseUrl") != "https://pais.example/v1" {
		t.Errorf("providers %v", get(v, "kagent", "providers"))
	}
	if get(v, "substrate", "ateApi", "logLevel") != "debug" {
		t.Error("the AddonConfig's helmValues were lost")
	}
	if get(v, "global", "imagePullSecrets") == nil {
		t.Error("global.imagePullSecrets from helmValues was lost")
	}
	if get(v, "kagent", "config", "substrate", "apiEndpoint") != "dns:///kagent-api.ate-system.svc:443" {
		t.Error("a derived key was not overwritten")
	}
	helmTemplate(t, v)
}

// Leaving a model field empty must not set the key: the chart's defaults stay.
func TestACDEmptyModelProviderSetsNothing(t *testing.T) {
	v := render(t, map[string]any{
		"helmOptions":   map[string]any{"targetNamespace": "ate-system"},
		"modelProvider": map[string]any{"baseUrl": "", "model": "", "apiKeySecretName": ""},
		"storageClass":  "",
	}, nil)
	if get(v, "kagent", "providers", "openAI", "config") != nil || get(v, "kagent", "providers", "openAI", "apiKeySecretRef") != nil {
		t.Errorf("providers %v", get(v, "kagent", "providers"))
	}
	if get(v, "postgres", "storageClass") != nil {
		t.Errorf("storageClass set to %v", get(v, "postgres", "storageClass"))
	}
}

// The values the ACD derives for the default cluster must equal the ones in
// the chart's values.yaml, which `helm template` without the ACD uses.
func TestACDAgreesWithChartDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../chart/kagent/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var defaults map[string]any
	if err := yaml.Unmarshal(raw, &defaults); err != nil {
		t.Fatal(err)
	}
	v := render(t, schemaDefaults(), clusterDeps("cluster.local"))
	for _, path := range [][]string{
		{"global", "clusterDomain"},
		{"substrate", "preview"},
		{"substrate", "ateApi", "authorization", "bootstrapOwners"},
		{"substrate", "atenetRouter", "ingressAllowedSPIFFEIDs"},
		{"substrate", "credentialProvider", "namespacePolicies"},
		{"substrate", "atelet", "extraArgs"},
		{"substrate-podcert", "postgresClientNamespace"},
		{"substrate-podcert", "postgresClientServiceAccount"},
		{"substrate-podcert", "postgresClients"},
		{"kagent", "config", "substrate", "apiEndpoint"},
		{"kagent", "config", "substrate", "routerUrl"},
		{"kagent", "config", "substrate", "actorJwtIssuer"},
	} {
		if !reflect.DeepEqual(get(v, path...), get(defaults, path...)) {
			t.Errorf("%s: ACD %v, values.yaml %v", strings.Join(path, "."), get(v, path...), get(defaults, path...))
		}
	}
}

// helmTemplate renders the chart with these values, as the helm-controller
// does (release kagent in ate-system), and checks what lands where.
func helmTemplate(t *testing.T, values map[string]any) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Log("helm not installed; skipping helm template")
		return
	}
	b, err := yaml.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	f := t.TempDir() + "/values.yaml"
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(context.Background(), "helm", "template", "kagent", "../../chart/kagent", "-n", "ate-system", "--kube-version", "v1.37.1", "-f", f).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "rustfsadmin") {
		t.Error("the chart's plain-text RustFS key is in the render")
	}
	services := map[string]bool{}
	for _, doc := range strings.Split(string(out), "\n---\n") {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if yaml.Unmarshal([]byte(doc), &obj) != nil || obj.Kind != "Service" {
			continue
		}
		services[obj.Metadata.Namespace+"/"+obj.Metadata.Name] = true
	}
	for _, want := range []string{"ate-system/kagent-api", "ate-system/kagent-idp", "ate-system/kagent-atenet-router"} {
		if !services[want] {
			t.Errorf("Service %s is not in the render; kagent's derived endpoints would dangle", want)
		}
	}
}

func TestACDDefaultRenderIsAValidChart(t *testing.T) {
	helmTemplate(t, render(t, schemaDefaults(), clusterDeps("cluster.local")))
}
