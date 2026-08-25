package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MegaThorx/deployron/common"
)

type sentMessage struct {
	identifier string
	parameter  string
}

func setupTest(t *testing.T) *[]sentMessage {
	t.Helper()

	config = &common.Config{
		Deployments: []common.Deployment{
			{Name: "test", Secret: "s3cret"},
		},
	}
	authFailures = newAuthThrottle()

	var sent []sentMessage
	sendMessageToService = func(identifier string, parameter string) error {
		sent = append(sent, sentMessage{identifier: identifier, parameter: parameter})
		return nil
	}
	t.Cleanup(func() { sendMessageToService = sendMessageToServiceOverSocket })

	return &sent
}

func doRequest(method, target, secretHeader, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if secretHeader != "" {
		req.Header.Set("X-API-Secret", secretHeader)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	res := httptest.NewRecorder()
	newRouter().ServeHTTP(res, req)
	return res
}

func TestDeployHappyPath(t *testing.T) {
	sent := setupTest(t)

	res := doRequest(http.MethodPost, "/deploy/test", "s3cret", "")

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", res.Code, http.StatusOK, res.Body.String())
	}
	if len(*sent) != 1 || (*sent)[0] != (sentMessage{identifier: "EXC_DEPLOY", parameter: "test"}) {
		t.Errorf("sent messages = %+v, want one EXC_DEPLOY for \"test\"", *sent)
	}
}

func TestDeployRejectsQueryParameterSecret(t *testing.T) {
	sent := setupTest(t)

	res := doRequest(http.MethodPost, "/deploy/test?APISecret=s3cret", "", "")

	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if len(*sent) != 0 {
		t.Errorf("sent %d messages, want 0", len(*sent))
	}
}

func TestDeployRejectsGet(t *testing.T) {
	sent := setupTest(t)

	res := doRequest(http.MethodGet, "/deploy/test", "s3cret", "")

	if res.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
	if len(*sent) != 0 {
		t.Errorf("sent %d messages, want 0", len(*sent))
	}
}

func TestDeployWrongSecret(t *testing.T) {
	sent := setupTest(t)

	res := doRequest(http.MethodPost, "/deploy/test", "wrong", "")

	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if len(*sent) != 0 {
		t.Errorf("sent %d messages, want 0", len(*sent))
	}
}

func TestDeployUnknownNameMatchesWrongSecretResponse(t *testing.T) {
	setupTest(t)

	unknown := doRequest(http.MethodPost, "/deploy/nosuch", "s3cret", "")
	wrongSecret := doRequest(http.MethodPost, "/deploy/test", "wrong", "")

	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown name status = %d, want %d", unknown.Code, http.StatusNotFound)
	}
	if unknown.Code != wrongSecret.Code || unknown.Body.String() != wrongSecret.Body.String() {
		t.Errorf("unknown name response (%d, %q) differs from wrong secret response (%d, %q)",
			unknown.Code, unknown.Body.String(), wrongSecret.Code, wrongSecret.Body.String())
	}
}

func TestDeployThrottlesRepeatedFailures(t *testing.T) {
	setupTest(t)

	attacker := "203.0.113.7:4444"
	for i := 0; i < maxAuthFailures; i++ {
		if res := doRequest(http.MethodPost, "/deploy/test", "wrong", attacker); res.Code != http.StatusNotFound {
			t.Fatalf("attempt %d: status = %d, want %d", i, res.Code, http.StatusNotFound)
		}
	}

	if res := doRequest(http.MethodPost, "/deploy/test", "s3cret", attacker); res.Code != http.StatusTooManyRequests {
		t.Errorf("throttled request status = %d, want %d", res.Code, http.StatusTooManyRequests)
	}

	// Other addresses are unaffected.
	if res := doRequest(http.MethodPost, "/deploy/test", "s3cret", "198.51.100.1:1234"); res.Code != http.StatusOK {
		t.Errorf("unthrottled request status = %d, want %d", res.Code, http.StatusOK)
	}
}
