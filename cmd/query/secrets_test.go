package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMaskValue(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"sa":                     "••••",
		"1433":                   "••••",
		"password":               "••••", // 8 chars → fully masked
		"sql-server.example.com": "sql-••••.com",
		"https://elastic:9200":   "http••••9200",
	}
	for in, want := range cases {
		if got := maskValue(in); got != want {
			t.Errorf("maskValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListSecretResources(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
			Data:       map[string][]byte{"password": []byte("hunter2"), "host": []byte("sql-server.example.com")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "elastic", Namespace: "default"},
			Data:       map[string][]byte{"apiKey": []byte("k")},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
			Data:       map[string]string{"web_url": "https://app.example.com/web"},
		},
	)

	secrets, err := listSecretResources(context.Background(), client, "secret", "default")
	if err != nil {
		t.Fatalf("listSecretResources: %v", err)
	}
	if len(secrets) != 2 || secrets[0].Name != "db" || secrets[1].Name != "elastic" {
		t.Fatalf("secrets sorted by name expected [db elastic], got %+v", secrets)
	}
	if got := secrets[0].Keys; len(got) != 2 || got[0] != "host" || got[1] != "password" {
		t.Errorf("db keys sorted = %v, want [host password]", got)
	}

	cms, err := listSecretResources(context.Background(), client, "configmap", "default")
	if err != nil {
		t.Fatalf("listSecretResources(configmap): %v", err)
	}
	if len(cms) != 1 || cms[0].Name != "app" {
		t.Fatalf("configmaps expected [app], got %+v", cms)
	}
}

func TestListSecretResourcesServiceAccounts(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "writer", Namespace: "default"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "default"}},
	)

	sas, err := listSecretResources(context.Background(), client, "serviceaccount", "default")
	if err != nil {
		t.Fatalf("listSecretResources(serviceaccount): %v", err)
	}
	if len(sas) != 2 || sas[0].Name != "reader" || sas[1].Name != "writer" {
		t.Fatalf("service accounts sorted by name expected [reader writer], got %+v", sas)
	}
	if sas[0].Keys != nil {
		t.Errorf("service accounts must be name-only, got keys %v", sas[0].Keys)
	}
}

func TestListHelmReleasesDedupesRevisions(t *testing.T) {
	// Helm keeps one release Secret per revision; the picker lists each release once.
	release := func(rev string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sh.helm.release.v1.mysql.v" + rev,
				Namespace: "default",
				Labels:    map[string]string{"name": "mysql", "status": "deployed", "version": rev},
			},
			Type: helmReleaseSecretType,
		}
	}
	client := fake.NewSimpleClientset(release("1"), release("2"))

	releases, err := listSecretResources(context.Background(), client, "helm", "default")
	if err != nil {
		t.Fatalf("listSecretResources(helm): %v", err)
	}
	if len(releases) != 1 || releases[0].Name != "mysql" {
		t.Fatalf("helm releases expected [mysql] (deduped across revisions), got %+v", releases)
	}
}

func TestPreviewSecretKeysMasksValues(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
		Data:       map[string][]byte{"host": []byte("sql-server.example.com"), "password": []byte("hunter2")},
	})

	previews, err := previewSecretKeys(context.Background(), client, "secret", "db", "prod")
	if err != nil {
		t.Fatalf("previewSecretKeys: %v", err)
	}
	got := map[string]string{}
	for _, p := range previews {
		got[p.Key] = p.Value
	}
	if got["host"] != "sql-••••.com" {
		t.Errorf("host preview = %q, want sql-••••.com", got["host"])
	}
	if got["password"] != "••••" {
		t.Errorf("password preview = %q, want fully masked", got["password"])
	}
}
