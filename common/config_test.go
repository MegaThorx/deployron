package common

import "testing"

func TestFindDeploymentByName(t *testing.T) {
	config := &Config{
		Deployments: []Deployment{
			{Name: "first", Secret: "a"},
			{Name: "second", Secret: "b"},
		},
	}

	deployment := config.FindDeploymentByName("second")
	if deployment == nil {
		t.Fatal("FindDeploymentByName(\"second\") = nil, want deployment")
	}
	if deployment.Secret != "b" {
		t.Errorf("Secret = %q, want %q", deployment.Secret, "b")
	}

	if deployment := config.FindDeploymentByName("missing"); deployment != nil {
		t.Errorf("FindDeploymentByName(\"missing\") = %+v, want nil", deployment)
	}
}
